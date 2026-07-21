package signal

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

const qrTestOffer = "v=0\r\no=- 123 2 IN IP4 127.0.0.1\r\na=candidate:1 1 udp 2130706431 10.0.0.1 5000 typ host\r\n"
const qrTestAnswer = "v=0\r\no=- 456 2 IN IP4 127.0.0.1\r\na=candidate:2 1 udp 2130706431 10.0.0.2 5001 typ host\r\n"

// syncBuf serializes the render goroutine's writes with the test's reads.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func hasQRArt(s string) bool {
	return strings.ContainsAny(s, "█▄▀")
}

// waitArt polls until the display buffer shows QR art (the render goroutine
// writes asynchronously from respond/wait).
func waitArt(t *testing.T, s *syncBuf) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if hasQRArt(s.String()) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("display buffer never showed QR art")
}

// TestQRHandshake runs a full QP/1 exchange: A's offer goes out as frames +
// blob, B reads the blob and answers as frames + blob, A reads the answer
// blob. Display buffers must contain rendered QR art; SDPs must round-trip.
func TestQRHandshake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	aIn, bOut := io.Pipe() // B's answer blob → A
	bIn, aOut := io.Pipe() // A's offer blob → B
	dispA, dispB := &syncBuf{}, &syncBuf{}

	qa := &QR{Out: dispA, In: aIn, BlobOut: aOut, FPS: 500}
	qb := &QR{Out: dispB, In: bIn, BlobOut: bOut, FPS: 500}

	offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: qrTestOffer}
	answer := webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: qrTestAnswer}

	type waitRes struct {
		desc webrtc.SessionDescription
		err  error
	}
	done := make(chan waitRes, 1)
	go func() {
		_, wait, err := qa.Announce(ctx, offer)
		if err != nil {
			done <- waitRes{err: err}
			return
		}
		got, err := wait(ctx)
		done <- waitRes{got, err}
	}()

	gotOffer, respond, err := qb.Join(ctx, "")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if gotOffer.SDP != offer.SDP {
		t.Errorf("offer round-trip: got %q, want %q", gotOffer.SDP, offer.SDP)
	}
	if err := respond(ctx, answer); err != nil {
		t.Fatalf("respond: %v", err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("wait: %v", r.err)
		}
		if r.desc.SDP != answer.SDP {
			t.Errorf("answer round-trip: got %q, want %q", r.desc.SDP, answer.SDP)
		}
	case <-ctx.Done():
		t.Fatal("handshake timed out")
	}

	waitArt(t, dispA)
	waitArt(t, dispB)
}

func TestQRAnnounceNilStreams(t *testing.T) {
	ctx := context.Background()
	desc := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: qrTestOffer}
	if _, _, err := (&QR{In: strings.NewReader("")}).Announce(ctx, desc); err == nil {
		t.Error("Announce with nil Out: want error")
	}
	if _, _, err := (&QR{Out: io.Discard}).Announce(ctx, desc); err == nil {
		t.Error("Announce with nil In: want error")
	}
}

func TestQRWaitRequiresAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Feed an OFFER blob to the wait path (wrong direction).
	offerBlob, err := EncodePayload(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: qrTestOffer})
	if err != nil {
		t.Fatalf("EncodePayload: %v", err)
	}
	q := &QR{Out: io.Discard, In: strings.NewReader(offerBlob + "\n")}
	_, wait, err := q.Announce(ctx, webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: qrTestOffer})
	if err != nil {
		t.Fatalf("Announce: %v", err)
	}
	if _, err := wait(ctx); err == nil || !strings.Contains(err.Error(), "expected answer") {
		t.Errorf("wait with offer blob: got %v, want 'expected answer' error", err)
	}
}
