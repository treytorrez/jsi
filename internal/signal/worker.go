package signal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/treyt/jsi/internal/token"
)

// SP/1 error codes (proto/SIGNALING.md §Errors — closed set).
const (
	ErrCodeSessionNotFound = "session_not_found"
	ErrCodeAnswerNotReady  = "answer_not_ready"
	ErrCodeAnswerExists    = "answer_exists"
	ErrCodeInvalidToken    = "invalid_token"
	ErrCodeInvalidBody     = "invalid_body"
	ErrCodeRateLimited     = "rate_limited"
	ErrCodeInternal        = "internal"
)

// ErrTimeout is returned by the Announce wait function when the session's
// expiresAt passes without an answer being posted (SP/1 "signaling
// timeout"; CLI exit code 2).
var ErrTimeout = errors.New("signal: timed out waiting for answer")

// Error is a typed SP/1 error returned by the worker as
// {"error":{"code","message"}} on non-2xx responses.
type Error struct {
	Code       string
	HTTPStatus int
}

// Error implements the error interface.
func (e *Error) Error() string {
	return fmt.Sprintf("signal: %s (HTTP %d)", e.Code, e.HTTPStatus)
}

// maxResponseBytes caps SP/1 response bodies (SPDs are ≤32 KiB per SP/1;
// this is generous headroom).
const maxResponseBytes = 1 << 20

// defaultPollInterval is the SP/1 normative sender poll interval (D13).
const defaultPollInterval = time.Second

// Worker is a Channel speaking SP/1 to the signaling worker. The zero
// value is usable once BaseURL is set: HTTPClient defaults to
// http.DefaultClient and PollInterval to 1 s.
type Worker struct {
	// BaseURL is the worker origin, e.g. "https://jsi-signal.example.workers.dev".
	BaseURL string
	// HTTPClient performs the requests; nil means http.DefaultClient.
	HTTPClient *http.Client
	// PollInterval is the delay between answer polls; ≤0 means 1 s.
	PollInterval time.Duration
}

var _ Channel = (*Worker)(nil)

func (w *Worker) base() string { return strings.TrimRight(w.BaseURL, "/") }

func (w *Worker) httpClient() *http.Client {
	if w.HTTPClient != nil {
		return w.HTTPClient
	}
	return http.DefaultClient
}

func (w *Worker) pollInterval() time.Duration {
	if w.PollInterval > 0 {
		return w.PollInterval
	}
	return defaultPollInterval
}

// ICEServers fetches ICE server configuration (GET /v1/ice, D6) — STUN
// plus freshly minted TURN credentials — for use before creating an
// offer/answer.
func (w *Worker) ICEServers(ctx context.Context) ([]webrtc.ICEServer, error) {
	var resp struct {
		ICEServers []struct {
			URLs       []string `json:"urls"`
			Username   string   `json:"username"`
			Credential string   `json:"credential"`
		} `json:"iceServers"`
	}
	if err := w.doJSON(ctx, http.MethodGet, "/v1/ice", nil, &resp); err != nil {
		return nil, err
	}
	servers := make([]webrtc.ICEServer, 0, len(resp.ICEServers))
	for _, s := range resp.ICEServers {
		servers = append(servers, webrtc.ICEServer{
			URLs:       s.URLs,
			Username:   s.Username,
			Credential: s.Credential,
		})
	}
	return servers, nil
}

// Announce implements Channel: POST /v1/sessions with the offer, then poll
// GET /v1/sessions/{token}/answer every PollInterval until expiresAt.
func (w *Worker) Announce(ctx context.Context, offer webrtc.SessionDescription) (string, func(context.Context) (webrtc.SessionDescription, error), error) {
	var resp struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	}
	err := w.doJSON(ctx, http.MethodPost, "/v1/sessions", struct {
		Offer webrtc.SessionDescription `json:"offer"`
	}{Offer: offer}, &resp)
	if err != nil {
		return "", nil, err
	}
	expiresAt, err := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err != nil {
		return "", nil, fmt.Errorf("signal: invalid expiresAt %q: %w", resp.ExpiresAt, err)
	}
	wait := func(waitCtx context.Context) (webrtc.SessionDescription, error) {
		return w.waitForAnswer(waitCtx, resp.Token, expiresAt)
	}
	return resp.Token, wait, nil
}

// Join implements Channel: the token is normalized client-side (no HTTP on
// invalid input), then GET /v1/sessions/{token}/offer. The returned respond
// POSTs the answer to /v1/sessions/{token}/answer.
func (w *Worker) Join(ctx context.Context, tok string) (webrtc.SessionDescription, func(context.Context, webrtc.SessionDescription) error, error) {
	norm, err := token.Normalize(tok)
	if err != nil {
		return webrtc.SessionDescription{}, nil, fmt.Errorf("signal: %w", err)
	}
	var resp struct {
		Offer webrtc.SessionDescription `json:"offer"`
	}
	if err := w.doJSON(ctx, http.MethodGet, "/v1/sessions/"+norm+"/offer", nil, &resp); err != nil {
		return webrtc.SessionDescription{}, nil, err
	}
	respond := func(rCtx context.Context, answer webrtc.SessionDescription) error {
		return w.doJSON(rCtx, http.MethodPost, "/v1/sessions/"+norm+"/answer", struct {
			Answer webrtc.SessionDescription `json:"answer"`
		}{Answer: answer}, nil)
	}
	return resp.Offer, respond, nil
}

// waitForAnswer polls GET /v1/sessions/{tok}/answer: 404 answer_not_ready
// keeps polling until expiresAt (then ErrTimeout); any other error
// propagates; ctx cancels the wait.
func (w *Worker) waitForAnswer(ctx context.Context, tok string, expiresAt time.Time) (webrtc.SessionDescription, error) {
	for {
		var resp struct {
			Answer webrtc.SessionDescription `json:"answer"`
		}
		err := w.doJSON(ctx, http.MethodGet, "/v1/sessions/"+tok+"/answer", nil, &resp)
		if err == nil {
			return resp.Answer, nil
		}
		var serr *Error
		if !errors.As(err, &serr) || serr.Code != ErrCodeAnswerNotReady {
			return webrtc.SessionDescription{}, err
		}
		if !time.Now().Before(expiresAt) {
			return webrtc.SessionDescription{}, fmt.Errorf("%w (token %s)", ErrTimeout, tok)
		}
		timer := time.NewTimer(w.pollInterval())
		select {
		case <-ctx.Done():
			timer.Stop()
			return webrtc.SessionDescription{}, ctx.Err()
		case <-timer.C:
		}
	}
}

// doJSON performs one SP/1 JSON request. A non-nil reqBody is JSON-encoded;
// a non-nil respBody is decoded from a 2xx response. Non-2xx responses are
// converted to *Error from the {"error":{"code","message"}} shape; a
// malformed error body yields ErrCodeInternal.
func (w *Worker) doJSON(ctx context.Context, method, path string, reqBody, respBody any) error {
	var body io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("signal: encode %s %s: %w", method, path, err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, w.base()+path, body)
	if err != nil {
		return fmt.Errorf("signal: build %s %s: %w", method, path, err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := w.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("signal: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("signal: read %s %s response: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeError(resp.StatusCode, data)
	}
	if respBody != nil {
		if err := json.Unmarshal(data, respBody); err != nil {
			return fmt.Errorf("signal: decode %s %s response: %w", method, path, err)
		}
	}
	return nil
}

// decodeError converts a non-2xx SP/1 response body to *Error.
func decodeError(status int, body []byte) error {
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err != nil || e.Error.Code == "" {
		return &Error{Code: ErrCodeInternal, HTTPStatus: status}
	}
	return &Error{Code: e.Error.Code, HTTPStatus: status}
}
