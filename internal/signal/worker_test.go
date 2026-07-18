package signal_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/treyt/jsi/internal/signal"
)

var (
	offerDesc  = webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: "v=0 fake-offer-sdp"}
	answerDesc = webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: "v=0 fake-answer-sdp"}
	tokenRe    = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{6}$`)
)

// fakeWorker is an in-memory SP/1 worker (proto/SIGNALING.md): sessions in a
// map keyed "s:{TOKEN}" (offer) / "a:{TOKEN}" (answer), exactly like the
// worker's KV layout.
type fakeWorker struct {
	ttl time.Duration

	mu       sync.Mutex
	sessions map[string]json.RawMessage
	seq      int

	requests atomic.Int64 // every HTTP request the server saw
	polls    atomic.Int64 // GET .../answer requests
}

func newFakeWorker(ttl time.Duration) *fakeWorker {
	return &fakeWorker{ttl: ttl, sessions: map[string]json.RawMessage{}}
}

func (f *fakeWorker) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ice", f.handleICE)
	mux.HandleFunc("POST /v1/sessions", f.handleCreateSession)
	mux.HandleFunc("GET /v1/sessions/{token}/offer", f.handleGetOffer)
	mux.HandleFunc("POST /v1/sessions/{token}/answer", f.handlePostAnswer)
	mux.HandleFunc("GET /v1/sessions/{token}/answer", f.handleGetAnswer)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// nextToken mints sequential Crockford Base32 tokens. Caller holds f.mu.
func (f *fakeWorker) nextToken() string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	n := f.seq
	f.seq++
	b := []byte("000000")
	for i := 5; i >= 0 && n > 0; i-- {
		b[i] = alphabet[n%32]
		n /= 32
	}
	return string(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": code},
	})
}

func (f *fakeWorker) handleICE(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"iceServers": []map[string]any{
			{"urls": []string{"stun:stun.cloudflare.com:3478"}},
			{
				"urls":       []string{"turn:turn.cloudflare.com:3478?transport=udp", "turns:turn.cloudflare.com:5349?transport=tcp"},
				"username":   "deadbeef",
				"credential": "cafebabe",
			},
		},
		"ttl": 3600,
	})
}

func (f *fakeWorker) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Offer webrtc.SessionDescription `json:"offer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Offer.Type != webrtc.SDPTypeOffer {
		writeErr(w, http.StatusBadRequest, "invalid_body")
		return
	}
	f.mu.Lock()
	tok := f.nextToken()
	raw, _ := json.Marshal(map[string]any{"offer": body.Offer})
	f.sessions["s:"+tok] = raw
	f.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]string{
		"token":     tok,
		"expiresAt": time.Now().Add(f.ttl).UTC().Format(time.RFC3339Nano),
	})
}

func (f *fakeWorker) handleGetOffer(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	if !tokenRe.MatchString(tok) {
		writeErr(w, http.StatusBadRequest, "invalid_token")
		return
	}
	f.mu.Lock()
	raw, ok := f.sessions["s:"+tok]
	f.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "session_not_found")
		return
	}
	writeRaw(w, http.StatusOK, raw)
}

func (f *fakeWorker) handlePostAnswer(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	if !tokenRe.MatchString(tok) {
		writeErr(w, http.StatusBadRequest, "invalid_token")
		return
	}
	var body struct {
		Answer webrtc.SessionDescription `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Answer.Type != webrtc.SDPTypeAnswer {
		writeErr(w, http.StatusBadRequest, "invalid_body")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sessions["s:"+tok]; !ok {
		writeErr(w, http.StatusNotFound, "session_not_found")
		return
	}
	if _, ok := f.sessions["a:"+tok]; ok {
		writeErr(w, http.StatusConflict, "answer_exists")
		return
	}
	raw, _ := json.Marshal(map[string]any{"answer": body.Answer})
	f.sessions["a:"+tok] = raw
	writeJSON(w, http.StatusCreated, map[string]string{})
}

func (f *fakeWorker) handleGetAnswer(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	f.polls.Add(1)
	if !tokenRe.MatchString(tok) {
		writeErr(w, http.StatusBadRequest, "invalid_token")
		return
	}
	f.mu.Lock()
	_, haveOffer := f.sessions["s:"+tok]
	raw, haveAnswer := f.sessions["a:"+tok]
	f.mu.Unlock()
	switch {
	case !haveOffer:
		writeErr(w, http.StatusNotFound, "session_not_found")
	case !haveAnswer:
		writeErr(w, http.StatusNotFound, "answer_not_ready")
	default:
		writeRaw(w, http.StatusOK, raw)
	}
}

func writeRaw(w http.ResponseWriter, status int, raw json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

type waitResult struct {
	sd  webrtc.SessionDescription
	err error
}

func descEqual(a, b webrtc.SessionDescription) bool {
	return a.Type == b.Type && a.SDP == b.SDP
}

// TestHandshake exercises the full SP/1 flow: ICEServers → Announce → Join
// (offer matches) → respond → wait returns the answer.
func TestHandshake(t *testing.T) {
	f := newFakeWorker(90 * time.Second)
	srv := f.server(t)
	w := &signal.Worker{BaseURL: srv.URL, PollInterval: 10 * time.Millisecond}
	ctx := context.Background()

	servers, err := w.ICEServers(ctx)
	if err != nil {
		t.Fatalf("ICEServers: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("ICEServers: got %d servers, want 2", len(servers))
	}
	if got := servers[0].URLs; len(got) != 1 || got[0] != "stun:stun.cloudflare.com:3478" {
		t.Errorf("ICEServers[0].URLs = %v", got)
	}
	if got := servers[1].URLs; len(got) != 2 {
		t.Errorf("ICEServers[1].URLs = %v, want 2 entries", got)
	}
	if servers[1].Username != "deadbeef" || servers[1].Credential != "cafebabe" {
		t.Errorf("ICEServers[1] creds = %q/%q", servers[1].Username, servers[1].Credential)
	}

	tok, wait, err := w.Announce(ctx, offerDesc)
	if err != nil {
		t.Fatalf("Announce: %v", err)
	}
	if !tokenRe.MatchString(tok) {
		t.Fatalf("Announce token %q does not match Crockford alphabet", tok)
	}

	gotOffer, respond, err := w.Join(ctx, tok)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if !descEqual(gotOffer, offerDesc) {
		t.Fatalf("Join offer = %+v, want %+v", gotOffer, offerDesc)
	}

	ch := make(chan waitResult, 1)
	go func() {
		sd, err := wait(ctx)
		ch <- waitResult{sd, err}
	}()

	if err := respond(ctx, answerDesc); err != nil {
		t.Fatalf("respond: %v", err)
	}

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("wait: %v", r.err)
		}
		if !descEqual(r.sd, answerDesc) {
			t.Fatalf("wait answer = %+v, want %+v", r.sd, answerDesc)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return")
	}
}

// TestWaitPollsUntilAnswer asserts wait blocks while no answer is posted
// (several poll intervals pass, server sees ≥3 polls) and returns promptly
// once respond posts the answer.
func TestWaitPollsUntilAnswer(t *testing.T) {
	f := newFakeWorker(90 * time.Second)
	srv := f.server(t)
	w := &signal.Worker{BaseURL: srv.URL, PollInterval: 20 * time.Millisecond}
	ctx := context.Background()

	tok, wait, err := w.Announce(ctx, offerDesc)
	if err != nil {
		t.Fatalf("Announce: %v", err)
	}

	ch := make(chan waitResult, 1)
	go func() {
		sd, err := wait(ctx)
		ch <- waitResult{sd, err}
	}()

	// >3 poll intervals with no answer: wait must not have returned.
	select {
	case r := <-ch:
		t.Fatalf("wait returned before any answer was posted: %+v", r)
	case <-time.After(70 * time.Millisecond):
	}
	deadline := time.Now().Add(2 * time.Second)
	for f.polls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := f.polls.Load(); got < 3 {
		t.Fatalf("server saw %d answer polls, want >= 3", got)
	}

	_, respond, err := w.Join(ctx, tok)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if err := respond(ctx, answerDesc); err != nil {
		t.Fatalf("respond: %v", err)
	}

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("wait: %v", r.err)
		}
		if !descEqual(r.sd, answerDesc) {
			t.Fatalf("wait answer = %+v, want %+v", r.sd, answerDesc)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not return after answer was posted")
	}
}

// TestJoinUnknownToken: well-formed but unknown token → typed 404.
func TestJoinUnknownToken(t *testing.T) {
	f := newFakeWorker(90 * time.Second)
	srv := f.server(t)
	w := &signal.Worker{BaseURL: srv.URL}

	_, _, err := w.Join(context.Background(), "AAAAAA")
	if err == nil {
		t.Fatal("Join: want error")
	}
	var serr *signal.Error
	if !errors.As(err, &serr) {
		t.Fatalf("Join error type = %T (%v), want *signal.Error", err, err)
	}
	if serr.Code != signal.ErrCodeSessionNotFound {
		t.Errorf("code = %q, want %q", serr.Code, signal.ErrCodeSessionNotFound)
	}
	if serr.HTTPStatus != http.StatusNotFound {
		t.Errorf("HTTPStatus = %d, want 404", serr.HTTPStatus)
	}
}

// TestRespondTwiceConflict: the second respond → typed 409 answer_exists.
func TestRespondTwiceConflict(t *testing.T) {
	f := newFakeWorker(90 * time.Second)
	srv := f.server(t)
	w := &signal.Worker{BaseURL: srv.URL}
	ctx := context.Background()

	tok, _, err := w.Announce(ctx, offerDesc)
	if err != nil {
		t.Fatalf("Announce: %v", err)
	}
	_, respond, err := w.Join(ctx, tok)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if err := respond(ctx, answerDesc); err != nil {
		t.Fatalf("first respond: %v", err)
	}
	err = respond(ctx, answerDesc)
	if err == nil {
		t.Fatal("second respond: want error")
	}
	var serr *signal.Error
	if !errors.As(err, &serr) {
		t.Fatalf("second respond error type = %T (%v), want *signal.Error", err, err)
	}
	if serr.Code != signal.ErrCodeAnswerExists {
		t.Errorf("code = %q, want %q", serr.Code, signal.ErrCodeAnswerExists)
	}
	if serr.HTTPStatus != http.StatusConflict {
		t.Errorf("HTTPStatus = %d, want 409", serr.HTTPStatus)
	}
}

// TestJoinInvalidTokenClientSide: invalid tokens are rejected before any
// HTTP request is made. (token.Normalize maps Crockford confusables
// I/L→1 and O→0, so the invalid cases use characters with no mapping.)
func TestJoinInvalidTokenClientSide(t *testing.T) {
	for _, tok := range []string{"", "12345", "1234567", "UUUUUU", "!!!!!!", "AB CD12"} {
		t.Run(fmt.Sprintf("token=%q", tok), func(t *testing.T) {
			f := newFakeWorker(90 * time.Second)
			srv := f.server(t)
			w := &signal.Worker{BaseURL: srv.URL}

			_, _, err := w.Join(context.Background(), tok)
			if err == nil {
				t.Fatalf("Join(%q): want error", tok)
			}
			if n := f.requests.Load(); n != 0 {
				t.Errorf("Join(%q): server saw %d requests, want 0", tok, n)
			}
		})
	}
}

// TestMalformedResponses: garbage on the wire must surface as errors, with
// malformed error bodies mapped to the "internal" code.
func TestMalformedResponses(t *testing.T) {
	ctx := context.Background()
	garbage := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{not json"))
	})

	tests := []struct {
		name     string
		handler  http.Handler
		run      func(*signal.Worker) error
		wantCode string // expected *signal.Error code; "" = plain (non-typed) error
	}{
		{
			name:    "ice 200 with garbage body",
			handler: garbage,
			run:     func(w *signal.Worker) error { _, err := w.ICEServers(ctx); return err },
		},
		{
			name: "announce 201 with garbage body",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte("garbage"))
			}),
			run: func(w *signal.Worker) error {
				_, _, err := w.Announce(ctx, offerDesc)
				return err
			},
		},
		{
			name: "announce 201 with unparseable expiresAt",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusCreated, map[string]string{"token": "ABCDEF", "expiresAt": "not-a-time"})
			}),
			run: func(w *signal.Worker) error {
				_, _, err := w.Announce(ctx, offerDesc)
				return err
			},
		},
		{
			name:    "offer 200 with garbage body",
			handler: garbage,
			run: func(w *signal.Worker) error {
				_, _, err := w.Join(ctx, "ABCDEF")
				return err
			},
		},
		{
			name: "non-2xx with garbage error body → internal",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("garbage"))
			}),
			run: func(w *signal.Worker) error {
				_, _, err := w.Join(ctx, "ABCDEF")
				return err
			},
			wantCode: signal.ErrCodeInternal,
		},
		{
			name: "non-2xx with JSON error body missing code → internal",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"error": map[string]string{"message": "boom"},
				})
			}),
			run: func(w *signal.Worker) error {
				_, _, err := w.Join(ctx, "ABCDEF")
				return err
			},
			wantCode: signal.ErrCodeInternal,
		},
		{
			name: "429 rate_limited parsed as typed error",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeErr(w, http.StatusTooManyRequests, "rate_limited")
			}),
			run: func(w *signal.Worker) error {
				_, _, err := w.Announce(ctx, offerDesc)
				return err
			},
			wantCode: signal.ErrCodeRateLimited,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			t.Cleanup(srv.Close)

			err := tc.run(&signal.Worker{BaseURL: srv.URL})
			if err == nil {
				t.Fatal("want error")
			}
			var serr *signal.Error
			if tc.wantCode == "" {
				if errors.As(err, &serr) {
					t.Fatalf("error = typed %v, want plain decode/parse error", serr)
				}
				return
			}
			if !errors.As(err, &serr) {
				t.Fatalf("error type = %T (%v), want *signal.Error", err, err)
			}
			if serr.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", serr.Code, tc.wantCode)
			}
		})
	}
}

// TestExpiresAtTimeout: with no answer posted, wait fails with ErrTimeout
// once expiresAt passes.
func TestExpiresAtTimeout(t *testing.T) {
	f := newFakeWorker(150 * time.Millisecond)
	srv := f.server(t)
	w := &signal.Worker{BaseURL: srv.URL, PollInterval: 20 * time.Millisecond}
	ctx := context.Background()

	_, wait, err := w.Announce(ctx, offerDesc)
	if err != nil {
		t.Fatalf("Announce: %v", err)
	}
	start := time.Now()
	_, err = wait(ctx)
	if !errors.Is(err, signal.ErrTimeout) {
		t.Fatalf("wait error = %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("wait returned after %v, want ~session TTL", elapsed)
	}
}

// TestWaitContextCancel: canceling the wait context stops polling promptly.
func TestWaitContextCancel(t *testing.T) {
	f := newFakeWorker(90 * time.Second)
	srv := f.server(t)
	w := &signal.Worker{BaseURL: srv.URL, PollInterval: 20 * time.Millisecond}

	_, wait, err := w.Announce(context.Background(), offerDesc)
	if err != nil {
		t.Fatalf("Announce: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() {
		_, err := wait(ctx)
		ch <- err
	}()
	time.Sleep(60 * time.Millisecond) // let a few polls happen
	cancel()
	select {
	case err := <-ch:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wait error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not return after context cancel")
	}
}
