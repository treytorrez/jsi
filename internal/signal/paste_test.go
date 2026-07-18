package signal_test

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/treyt/jsi/internal/signal"
)

// zlibBlob deflates raw and wraps it in the paste framing, for crafting
// decoder inputs that survive base64+zlib but fail later stages.
func zlibBlob(t *testing.T, raw string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write([]byte(raw)); err != nil {
		t.Fatalf("deflate fixture: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("deflate fixture: %v", err)
	}
	return "jsi1:" + base64.RawURLEncoding.EncodeToString(buf.Bytes())
}

func encodeLine(t *testing.T, desc webrtc.SessionDescription) string {
	t.Helper()
	blob, err := signal.EncodePayload(desc)
	if err != nil {
		t.Fatalf("EncodePayload: %v", err)
	}
	return blob + "\n"
}

// TestPasteHandshake runs the full QP/1 paste exchange: two Paste channels
// wired back-to-back with io.Pipe (blocking semantics like a terminal).
// Goroutine A announces and waits; goroutine B joins and responds. The
// offer and answer must round-trip byte-equal and both sides must finish.
func TestPasteHandshake(t *testing.T) {
	aInR, aInW := io.Pipe() // B writes the answer here; A reads it
	bInR, bInW := io.Pipe() // A writes the offer here; B reads it
	pa := &signal.Paste{In: aInR, Out: bInW}
	pb := &signal.Paste{In: bInR, Out: aInW}
	ctx := context.Background()

	type annResult struct {
		tok string
		sd  webrtc.SessionDescription
		err error
	}
	aCh := make(chan annResult, 1)
	go func() {
		tok, wait, err := pa.Announce(ctx, offerDesc)
		if err != nil {
			aCh <- annResult{err: fmt.Errorf("announce: %w", err)}
			return
		}
		sd, err := wait(ctx)
		aCh <- annResult{tok: tok, sd: sd, err: err}
	}()

	type joinResult struct {
		offer webrtc.SessionDescription
		err   error
	}
	bCh := make(chan joinResult, 1)
	go func() {
		offer, respond, err := pb.Join(ctx, "token-is-ignored")
		if err != nil {
			bCh <- joinResult{err: fmt.Errorf("join: %w", err)}
			return
		}
		if err := respond(ctx, answerDesc); err != nil {
			bCh <- joinResult{err: fmt.Errorf("respond: %w", err)}
			return
		}
		bCh <- joinResult{offer: offer}
	}()

	var a annResult
	var b joinResult
	for i := 0; i < 2; i++ {
		select {
		case a = <-aCh:
		case b = <-bCh:
		case <-time.After(5 * time.Second):
			t.Fatal("paste handshake did not complete in 5s")
		}
	}
	if a.err != nil {
		t.Fatalf("sender side: %v", a.err)
	}
	if b.err != nil {
		t.Fatalf("receiver side: %v", b.err)
	}
	if a.tok != "" {
		t.Errorf("Announce token = %q, want empty (paste has no token)", a.tok)
	}
	if !descEqual(b.offer, offerDesc) {
		t.Errorf("receiver decoded offer %+v, want %+v", b.offer, offerDesc)
	}
	if !descEqual(a.sd, answerDesc) {
		t.Errorf("sender decoded answer %+v, want %+v", a.sd, answerDesc)
	}
}

// TestDecodePayload is the codec table: happy paths (incl. whitespace
// tolerance) plus one case per failure mode of the decode chain.
func TestDecodePayload(t *testing.T) {
	valid := encodeLine(t, offerDesc)
	valid = strings.TrimSuffix(valid, "\n")

	tests := []struct {
		name    string
		input   string
		wantErr string // substring of the expected error; "" = success
		want    webrtc.SessionDescription
	}{
		{name: "valid offer", input: valid, want: offerDesc},
		{name: "whitespace tolerated", input: " \t" + valid + "\r\n ", want: offerDesc},
		{name: "missing prefix", input: strings.TrimPrefix(valid, "jsi1:"), wantErr: "prefix"},
		{name: "bad base64", input: "jsi1:not*valid*base64", wantErr: "base64"},
		{
			name:    "corrupt zlib",
			input:   "jsi1:" + base64.RawURLEncoding.EncodeToString([]byte("definitely not a zlib stream")),
			wantErr: "zlib",
		},
		{name: "valid zlib invalid JSON", input: zlibBlob(t, "{not json"), wantErr: "JSON"},
		{name: "wrong type pranswer", input: zlibBlob(t, `{"type":"pranswer","sdp":"v=0 x"}`), wantErr: "offer or answer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := signal.DecodePayload(tc.input)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("DecodePayload: %v", err)
				}
				if !descEqual(got, tc.want) {
					t.Errorf("decoded %+v, want %+v", got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want substring %q", err, tc.wantErr)
			}
		})
	}
}

// TestAnnounceCancel: canceling wait's context returns promptly even
// though the underlying read on In never unblocks on its own.
func TestAnnounceCancel(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() }) // release the parked read goroutine
	p := &signal.Paste{In: r, Out: io.Discard}

	_, wait, err := p.Announce(context.Background(), offerDesc)
	if err != nil {
		t.Fatalf("Announce: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() {
		_, err := wait(ctx)
		ch <- err
	}()
	time.Sleep(20 * time.Millisecond) // let wait park on the read
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

// realisticSDP is a 2–3 KB fake non-trickle SDP with \r\n lines and
// host/srflx candidates, shaped like what pion emits after a full gather.
func realisticSDP() string {
	var b strings.Builder
	b.WriteString("v=0\r\n")
	b.WriteString("o=- 4611733055283493179 2 IN IP4 127.0.0.1\r\n")
	b.WriteString("s=-\r\n")
	b.WriteString("t=0 0\r\n")
	b.WriteString("a=group:BUNDLE 0\r\n")
	b.WriteString("a=extmap-allow-mixed\r\n")
	b.WriteString("a=msid-semantic: WMS\r\n")
	b.WriteString("m=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\n")
	b.WriteString("c=IN IP4 0.0.0.0\r\n")
	b.WriteString("a=ice-ufrag:AbCdEfGhIjKlMnOp\r\n")
	b.WriteString("a=ice-pwd:0123456789abcdefghijklmnopqrstuv\r\n")
	b.WriteString("a=ice-options:trickle\r\n")
	b.WriteString("a=fingerprint:sha-256 12:34:56:78:9A:BC:DE:F0:12:34:56:78:9A:BC:DE:F0:12:34:56:78:9A:BC:DE:F0:12:34:56:78:9A:BC:DE:F0\r\n")
	b.WriteString("a=setup:actpass\r\n")
	b.WriteString("a=mid:0\r\n")
	b.WriteString("a=sctp-port:5000\r\n")
	b.WriteString("a=max-message-size:262144\r\n")
	for i := range 10 {
		fmt.Fprintf(&b, "a=candidate:%d 1 udp 2130706431 192.168.1.%d 40%02d typ host generation 0 network-id 1\r\n",
			1000+i, i, i)
		fmt.Fprintf(&b, "a=candidate:%d 1 udp 1694498815 203.0.113.%d 50%02d typ srflx raddr 192.168.1.%d rport 40%02d generation 0 network-id 1\r\n",
			2000+i, i, i, i, i)
	}
	b.WriteString("a=end-of-candidates\r\n")
	return b.String()
}

// TestPayloadRoundTripRealisticSDP: the paste blob of a realistic SDP is
// raw-url-safe, smaller than the raw SDP (compression earns its keep),
// and decodes back byte-exact.
func TestPayloadRoundTripRealisticSDP(t *testing.T) {
	sdp := realisticSDP()
	if n := len(sdp); n < 2000 || n > 4096 {
		t.Fatalf("fixture SDP length = %d bytes, want 2000–4096", n)
	}
	desc := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}

	blob, err := signal.EncodePayload(desc)
	if err != nil {
		t.Fatalf("EncodePayload: %v", err)
	}
	if !strings.HasPrefix(blob, "jsi1:") {
		t.Errorf("blob %q… missing jsi1: prefix", blob[:16])
	}
	body := strings.TrimPrefix(blob, "jsi1:")
	if strings.ContainsAny(body, "=+/") {
		t.Errorf("blob is not unpadded URL-safe base64 (found '=', '+', or '/')")
	}
	if len(blob) >= len(sdp) {
		t.Errorf("blob length %d ≥ raw SDP length %d: compression not effective", len(blob), len(sdp))
	}

	got, err := signal.DecodePayload(blob)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if !descEqual(got, desc) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, desc)
	}
}

// TestPasteTypeEnforcement: the channel rejects a blob of the wrong JSEP
// type for its role, and oversized lines are refused.
func TestPasteTypeEnforcement(t *testing.T) {
	ctx := context.Background()

	t.Run("Join requires offer", func(t *testing.T) {
		p := &signal.Paste{In: strings.NewReader(encodeLine(t, answerDesc)), Out: io.Discard}
		_, _, err := p.Join(ctx, "")
		if err == nil || !strings.Contains(err.Error(), "want offer") {
			t.Fatalf("Join error = %v, want 'want offer'", err)
		}
	})

	t.Run("wait requires answer", func(t *testing.T) {
		p := &signal.Paste{In: strings.NewReader(encodeLine(t, offerDesc)), Out: io.Discard}
		_, wait, err := p.Announce(ctx, offerDesc)
		if err != nil {
			t.Fatalf("Announce: %v", err)
		}
		if _, err := wait(ctx); err == nil || !strings.Contains(err.Error(), "want answer") {
			t.Fatalf("wait error = %v, want 'want answer'", err)
		}
	})

	t.Run("oversized line rejected", func(t *testing.T) {
		p := &signal.Paste{In: strings.NewReader(encodeLine(t, offerDesc)), Out: io.Discard, MaxLineBytes: 64}
		_, _, err := p.Join(ctx, "")
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("Join error = %v, want 'exceeds'", err)
		}
	})
}
