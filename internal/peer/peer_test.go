package peer_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/treyt/jsi/internal/peer"
)

// testConfig uses host candidates only: no ICE servers, no mDNS — the two
// PeerConnections connect over loopback with no external traffic.
var testConfig = peer.Config{ICEServers: nil, EnableMDNS: false}

// TestOfferAnswerLoopback runs the full non-trickle handshake in-process
// (M2.4 acceptance): two PeerConnections over host candidates, a text
// message each direction, then 1 MiB of binary through WriteFlow.
func TestOfferAnswerLoopback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	offerer, offer, err := peer.Offer(ctx, testConfig)
	if err != nil {
		t.Fatalf("Offer: %v", err)
	}
	defer func() { _ = offerer.Close() }()

	// D2: the offer is fully gathered and carries the data-channel section.
	if !strings.Contains(offer.SDP, "m=application") {
		t.Error("offer SDP missing m=application section")
	}
	if !strings.Contains(offer.SDP, "a=candidate:") {
		t.Error("offer SDP missing gathered candidates (non-trickle)")
	}

	answerer, answer, err := peer.Answer(ctx, testConfig, offer)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	defer func() { _ = answerer.Close() }()

	if err := offerer.SetRemote(ctx, answer); err != nil {
		t.Fatalf("SetRemote: %v", err)
	}

	if err := offerer.WaitOpen(ctx); err != nil {
		t.Fatalf("offerer WaitOpen: %v", err)
	}
	if err := answerer.WaitOpen(ctx); err != nil {
		t.Fatalf("answerer WaitOpen: %v", err)
	}

	// Text message each direction (TP/1 control messages are JSON text).
	exchangeText := func(t *testing.T, from, to *peer.Conn, msg string) {
		t.Helper()
		got := make(chan string, 1)
		to.Channel().OnMessage(func(m webrtc.DataChannelMessage) {
			if !m.IsString {
				return
			}
			select {
			case got <- string(m.Data):
			default:
			}
		})
		if err := from.Channel().SendText(msg); err != nil {
			t.Fatalf("SendText: %v", err)
		}
		select {
		case s := <-got:
			if s != msg {
				t.Fatalf("text message: got %q, want %q", s, msg)
			}
		case <-ctx.Done():
			t.Fatal("timed out waiting for text message")
		}
	}
	exchangeText(t, offerer, answerer, `{"type":"hello","version":1}`)
	exchangeText(t, answerer, offerer, `{"type":"accept"}`)

	// 1 MiB of binary through WriteFlow in MaxChunkSize chunks; the
	// ordered+reliable channel must deliver it intact. The watermark path
	// is exercised implicitly (the buffer may never fill on loopback).
	const total = 1 << 20
	payload := make([]byte, total)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	var mu sync.Mutex
	var received []byte
	done := make(chan struct{}, 1)
	answerer.Channel().OnMessage(func(m webrtc.DataChannelMessage) {
		if m.IsString {
			return
		}
		mu.Lock()
		received = append(received, m.Data...)
		full := len(received) >= total
		mu.Unlock()
		if full {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})

	for off := 0; off < total; off += peer.MaxChunkSize {
		if err := offerer.WriteFlow(ctx, payload[off:off+peer.MaxChunkSize]); err != nil {
			t.Fatalf("WriteFlow chunk at offset %d: %v", off, err)
		}
	}

	select {
	case <-done:
	case <-ctx.Done():
		mu.Lock()
		n := len(received)
		mu.Unlock()
		t.Fatalf("timed out: received %d of %d bytes", n, total)
	}

	mu.Lock()
	intact := bytes.Equal(received, payload)
	mu.Unlock()
	if !intact {
		t.Fatal("received payload differs from sent payload")
	}
}

// TestWaitOpenCancel verifies that ctx cancellation unblocks WaitOpen and
// WriteFlow.
func TestWaitOpenCancel(t *testing.T) {
	setupCtx, setupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer setupCancel()

	c, _, err := peer.Offer(setupCtx, testConfig)
	if err != nil {
		t.Fatalf("Offer: %v", err)
	}
	defer func() { _ = c.Close() }()

	// The channel never opens (no remote peer); cancellation must unblock.
	ctx, cancel := context.WithCancel(context.Background())
	waitErr := make(chan error, 1)
	go func() { waitErr <- c.WaitOpen(ctx) }()
	cancel()
	select {
	case err := <-waitErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WaitOpen after cancel: got %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitOpen did not unblock on ctx cancellation")
	}

	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if err := c.WriteFlow(canceled, []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteFlow with canceled ctx: got %v, want context.Canceled", err)
	}
}

// TestConnectionPath connects two peers over loopback host candidates and
// checks the D15 transparency string (M3): both ends must report
// "direct (host)". A canceled ctx is an error; an unconnected Conn has no
// nominated pair.
func TestConnectionPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	offerer, offer, err := peer.Offer(ctx, testConfig)
	if err != nil {
		t.Fatalf("Offer: %v", err)
	}
	defer func() { _ = offerer.Close() }()

	// Before any connection there is no nominated pair to report.
	if _, err := offerer.ConnectionPath(ctx); err == nil {
		t.Error("ConnectionPath before connect: got nil error, want failure")
	}

	answerer, answer, err := peer.Answer(ctx, testConfig, offer)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	defer func() { _ = answerer.Close() }()

	if err := offerer.SetRemote(ctx, answer); err != nil {
		t.Fatalf("SetRemote: %v", err)
	}
	if err := offerer.WaitOpen(ctx); err != nil {
		t.Fatalf("offerer WaitOpen: %v", err)
	}
	if err := answerer.WaitOpen(ctx); err != nil {
		t.Fatalf("answerer WaitOpen: %v", err)
	}

	for name, c := range map[string]*peer.Conn{"offerer": offerer, "answerer": answerer} {
		path, err := c.ConnectionPath(ctx)
		if err != nil {
			t.Fatalf("%s ConnectionPath: %v", name, err)
		}
		if path != "direct (host)" {
			t.Errorf("%s ConnectionPath: got %q, want %q", name, path, "direct (host)")
		}
	}

	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err := offerer.ConnectionPath(canceled); !errors.Is(err, context.Canceled) {
		t.Errorf("ConnectionPath with canceled ctx: got %v, want context.Canceled", err)
	}
}

// TestWriteFlowOversize verifies the 16 KiB caller contract.
func TestWriteFlowOversize(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, _, err := peer.Offer(ctx, testConfig)
	if err != nil {
		t.Fatalf("Offer: %v", err)
	}
	defer func() { _ = c.Close() }()

	if err := c.WriteFlow(ctx, make([]byte, peer.MaxChunkSize+1)); err == nil {
		t.Fatal("WriteFlow accepted a chunk larger than MaxChunkSize")
	}
}

// TestOnMessageReplaysBuffered verifies that messages arriving before
// Conn.OnMessage is called are replayed, not dropped: Pion discards messages
// with no registered handler, so Conn buffers from channel setup (TP/1 hello
// race found in M3 CLI bring-up).
func TestOnMessageReplaysBuffered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	offerer, offer, err := peer.Offer(ctx, testConfig)
	if err != nil {
		t.Fatalf("Offer: %v", err)
	}
	defer func() { _ = offerer.Close() }()
	answerer, answer, err := peer.Answer(ctx, testConfig, offer)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	defer func() { _ = answerer.Close() }()
	if err := offerer.SetRemote(ctx, answer); err != nil {
		t.Fatalf("SetRemote: %v", err)
	}
	if err := offerer.WaitOpen(ctx); err != nil {
		t.Fatalf("offerer WaitOpen: %v", err)
	}
	if err := answerer.WaitOpen(ctx); err != nil {
		t.Fatalf("answerer WaitOpen: %v", err)
	}

	// Send BEFORE any message handler is installed on the receiver, and
	// sleep to make the arrival-before-handler window deterministic.
	if err := offerer.Channel().SendText("early"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	got := make(chan string, 1)
	answerer.OnMessage(func(m webrtc.DataChannelMessage) {
		if m.IsString {
			got <- string(m.Data)
		}
	})
	select {
	case s := <-got:
		if s != "early" {
			t.Fatalf("replayed message: got %q, want %q", s, "early")
		}
	case <-ctx.Done():
		t.Fatal("early message dropped instead of replayed")
	}
}

// TestGatherTimeoutPartialSDP verifies that an unreachable STUN server does
// not stall the handshake: Offer returns after ~GatherTimeout with a
// partially-gathered SDP that still carries host candidates.
func TestGatherTimeoutPartialSDP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cfg := peer.Config{
		ICEServers:    []webrtc.ICEServer{{URLs: []string{"stun:192.0.2.1:3478"}}}, // TEST-NET-1: blackholed
		GatherTimeout: 500 * time.Millisecond,
	}
	start := time.Now()
	c, offer, err := peer.Offer(ctx, cfg)
	if err != nil {
		t.Fatalf("Offer: %v", err)
	}
	defer func() { _ = c.Close() }()
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("Offer stalled on unreachable STUN: took %s, want ~GatherTimeout", elapsed)
	}
	if !strings.Contains(offer.SDP, "a=candidate:") {
		t.Error("partial SDP missing host candidates")
	}
	t.Logf("gather bounded at %s (timeout 500ms + margin)", elapsed.Round(time.Millisecond))
}
