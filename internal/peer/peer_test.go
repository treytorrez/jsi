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
