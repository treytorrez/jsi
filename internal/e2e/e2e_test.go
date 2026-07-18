package e2e_test

// M2.8 integration: sender and receiver run as goroutines in this test
// process and meet through a REAL SP/1 signaling worker (wrangler dev, see
// scripts/e2e-local.sh): GET /v1/ice → offer → POST /v1/sessions → token →
// GET offer → answer → POST answer → sender polls answer → SetRemote →
// WaitOpen → TP/1 transfer of a 2 MiB crypto/rand file, SHA-256 verified.
// Afterwards the worker-side SP/1 contract is checked directly: the answer
// must still be retrievable (GET …/answer → 200) inside the 90 s KV TTL.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/treyt/jsi/internal/peer"
	"github.com/treyt/jsi/internal/signal"
	"github.com/treyt/jsi/internal/transfer"
)

const (
	// payloadSize is the transferred file size: 2 MiB per M2.8.
	payloadSize = 2 << 20
	// waitOpenTimeout bounds each side's WaitOpen (M2.8: 30 s).
	waitOpenTimeout = 30 * time.Second
	// testTimeout bounds the whole flow. It must stay comfortably under
	// the sum of both 90 s KV TTLs the flow depends on (the offer session
	// expires 90 s after Announce; ICE gathering against unreachable STUN
	// servers can add seconds of latency before that).
	testTimeout = 120 * time.Second
)

// sideResult reports a finished receiver; Token identifies the joined SP/1
// session so the main goroutine can re-check the worker afterwards.
type sideResult struct {
	Token string
	Err   error
}

// runSender performs the SP/1 sender flow then TP/1 Sends files.
func runSender(ctx context.Context, sig *signal.Worker, tokenCh chan<- string, files []transfer.File) error {
	ice, err := sig.ICEServers(ctx)
	if err != nil {
		return fmt.Errorf("sender: ICE servers: %w", err)
	}
	conn, offer, err := peer.Offer(ctx, peer.Config{ICEServers: ice, EnableMDNS: false})
	if err != nil {
		return fmt.Errorf("sender: offer: %w", err)
	}
	defer func() { _ = conn.Close() }()

	token, wait, err := sig.Announce(ctx, offer)
	if err != nil {
		return fmt.Errorf("sender: announce: %w", err)
	}
	select {
	case tokenCh <- token:
	case <-ctx.Done():
		return fmt.Errorf("sender: hand token to receiver: %w", ctx.Err())
	}

	answer, err := wait(ctx)
	if err != nil {
		return fmt.Errorf("sender: wait for answer: %w", err)
	}
	if err := conn.SetRemote(ctx, answer); err != nil {
		return fmt.Errorf("sender: set remote: %w", err)
	}
	openCtx, cancel := context.WithTimeout(ctx, waitOpenTimeout)
	defer cancel()
	if err := conn.WaitOpen(openCtx); err != nil {
		return fmt.Errorf("sender: wait open: %w", err)
	}
	if err := transfer.Send(ctx, conn, files, nil); err != nil {
		return fmt.Errorf("sender: send: %w", err)
	}
	return nil
}

// runReceiver performs the SP/1 receiver flow then TP/1 Receives into dstDir.
func runReceiver(ctx context.Context, sig *signal.Worker, tokenCh <-chan string, dstDir string) sideResult {
	ice, err := sig.ICEServers(ctx)
	if err != nil {
		return sideResult{Err: fmt.Errorf("receiver: ICE servers: %w", err)}
	}
	var token string
	select {
	case token = <-tokenCh:
	case <-ctx.Done():
		return sideResult{Err: fmt.Errorf("receiver: wait for token: %w", ctx.Err())}
	}

	offer, respond, err := sig.Join(ctx, token)
	if err != nil {
		return sideResult{Token: token, Err: fmt.Errorf("receiver: join: %w", err)}
	}
	conn, answer, err := peer.Answer(ctx, peer.Config{ICEServers: ice, EnableMDNS: false}, offer)
	if err != nil {
		return sideResult{Token: token, Err: fmt.Errorf("receiver: answer: %w", err)}
	}
	defer func() { _ = conn.Close() }()

	if err := respond(ctx, answer); err != nil {
		return sideResult{Token: token, Err: fmt.Errorf("receiver: respond: %w", err)}
	}
	openCtx, cancel := context.WithTimeout(ctx, waitOpenTimeout)
	defer cancel()
	if err := conn.WaitOpen(openCtx); err != nil {
		return sideResult{Token: token, Err: fmt.Errorf("receiver: wait open: %w", err)}
	}
	if err := transfer.Receive(ctx, conn, dstDir, nil); err != nil {
		return sideResult{Token: token, Err: fmt.Errorf("receiver: receive: %w", err)}
	}
	return sideResult{Token: token}
}

// writeRandomFile creates a crypto/rand file of size bytes in dir.
func writeRandomFile(t *testing.T, dir, name string, size int64) (path string, sum [32]byte) {
	t.Helper()
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	path = filepath.Join(dir, name)
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path, sha256.Sum256(payload)
}

// checkAnswerStored asserts the worker-side SP/1 contract with plain
// net/http: after the handshake, GET /v1/sessions/{token}/answer must
// return 200 with an answer body (SP/1 §Endpoints; the session lives until
// its 90 s TTL).
func checkAnswerStored(t *testing.T, ctx context.Context, base, token string) {
	t.Helper()
	url := strings.TrimRight(base, "/") + "/v1/sessions/" + token + "/answer"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build GET %s: %v", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200 (SP/1: answer must exist after the handshake)", url, resp.StatusCode)
	}
	var body struct {
		Answer struct {
			Type string `json:"type"`
			SDP  string `json:"sdp"`
		} `json:"answer"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		t.Fatalf("GET %s: decode body: %v", url, err)
	}
	if body.Answer.Type != "answer" || body.Answer.SDP == "" {
		t.Fatalf("GET %s: body %+v, want a non-empty answer SDP", url, body.Answer)
	}
}

// TestE2EWorker is the M2.8 acceptance: a full SP/1 handshake and TP/1
// transfer through a real signaling worker. It runs only when
// JSI_E2E_WORKER names the worker base URL (e.g. http://127.0.0.1:8787);
// scripts/e2e-local.sh boots wrangler dev and sets it.
func TestE2EWorker(t *testing.T) {
	base := os.Getenv("JSI_E2E_WORKER")
	if base == "" {
		t.Skip("JSI_E2E_WORKER unset — skipping worker e2e (run scripts/e2e-local.sh)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	srcDir := t.TempDir()
	dstDir := t.TempDir()
	src, wantSum := writeRandomFile(t, srcDir, "payload.bin", payloadSize)
	files, err := transfer.FilesFromPaths([]string{src})
	if err != nil {
		t.Fatalf("FilesFromPaths: %v", err)
	}

	sig := &signal.Worker{BaseURL: base}
	tokenCh := make(chan string, 1)
	senderDone := make(chan error, 1)
	receiverDone := make(chan sideResult, 1)
	go func() { senderDone <- runSender(ctx, sig, tokenCh, files) }()
	go func() { receiverDone <- runReceiver(ctx, sig, tokenCh, dstDir) }()

	if err := <-senderDone; err != nil {
		t.Fatalf("sender failed: %v", err)
	}
	res := <-receiverDone
	if res.Err != nil {
		t.Fatalf("receiver failed: %v", res.Err)
	}

	got, err := os.ReadFile(filepath.Join(dstDir, "payload.bin"))
	if err != nil {
		t.Fatalf("read received file: %v", err)
	}
	if sum := sha256.Sum256(got); sum != wantSum {
		t.Fatalf("received file SHA-256 %x differs from source %x", sha256.Sum256(got), wantSum)
	}
	t.Logf("session %s: %d bytes transferred, SHA-256 %x verified", res.Token, payloadSize, wantSum)

	checkAnswerStored(t, ctx, base, res.Token)
}
