package signal

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/pion/webrtc/v4"
)

// payloadPrefix tags a QP/1 paste blob (proto/SIGNALING.md §Paste format).
const payloadPrefix = "jsi1:"

// defaultMaxLineBytes caps one pasted line. Deflated SDPs are a few
// hundred bytes; this leaves generous headroom for fat TURN SDPs.
const defaultMaxLineBytes = 1 << 20

// maxPayloadBytes caps the inflated payload (the compressed line is
// already capped by MaxLineBytes, but zlib expands — this bounds a
// hostile or garbled paste).
const maxPayloadBytes = 1 << 20

// EncodePayload serializes desc to a QP/1 paste blob (proto/SIGNALING.md
// §Payload + §Paste format): JSEP JSON {"type","sdp"} → zlib deflate →
// "jsi1:" + base64url without padding. The §Payload CRC-32 is a QR-frame
// concern (M4) and is deliberately absent from the paste format —
// integrity comes from zlib's own checksum and the DTLS handshake.
func EncodePayload(desc webrtc.SessionDescription) (string, error) {
	raw, err := json.Marshal(desc)
	if err != nil {
		return "", fmt.Errorf("signal: encode payload JSON: %w", err)
	}
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return "", fmt.Errorf("signal: deflate payload: %w", err)
	}
	if err := zw.Close(); err != nil {
		return "", fmt.Errorf("signal: deflate payload: %w", err)
	}
	return payloadPrefix + base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

// DecodePayload parses a paste blob produced by EncodePayload. Surrounding
// whitespace is tolerated; the payload Type must be offer or answer.
func DecodePayload(s string) (webrtc.SessionDescription, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, payloadPrefix) {
		return webrtc.SessionDescription{}, fmt.Errorf("signal: paste payload missing %q prefix", payloadPrefix)
	}
	deflated, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, payloadPrefix))
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("signal: paste payload base64: %w", err)
	}
	zr, err := zlib.NewReader(bytes.NewReader(deflated))
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("signal: paste payload zlib: %w", err)
	}
	defer func() { _ = zr.Close() }()
	raw, err := io.ReadAll(io.LimitReader(zr, maxPayloadBytes+1))
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("signal: paste payload inflate: %w", err)
	}
	if len(raw) > maxPayloadBytes {
		return webrtc.SessionDescription{}, fmt.Errorf("signal: paste payload exceeds %d bytes inflated", maxPayloadBytes)
	}
	var desc webrtc.SessionDescription
	if err := json.Unmarshal(raw, &desc); err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("signal: paste payload JSON: %w", err)
	}
	if desc.Type != webrtc.SDPTypeOffer && desc.Type != webrtc.SDPTypeAnswer {
		return webrtc.SessionDescription{}, fmt.Errorf("signal: paste payload type %q, want offer or answer", desc.Type)
	}
	return desc, nil
}

// Paste is a Channel speaking the QP/1 paste format (proto/SIGNALING.md
// §Paste format): SDPs cross as single-line "jsi1:" blobs that the users
// copy between terminals out of band. It is the default signaling channel
// of the D15 `none` preset — zero external contact. A Paste value
// performs one handshake role (Announce xor Join); In and Out are
// required.
type Paste struct {
	// In yields the peer's paste blob (one line).
	In io.Reader
	// Out receives this side's paste blob (one line).
	Out io.Writer
	// MaxLineBytes caps the accepted blob line length; ≤0 means 1 MiB.
	MaxLineBytes int
}

var _ Channel = (*Paste)(nil)

func (p *Paste) maxLine() int {
	if p.MaxLineBytes > 0 {
		return p.MaxLineBytes
	}
	return defaultMaxLineBytes
}

// Announce implements Channel: it encodes offer as a paste blob and writes
// it as one line to Out BEFORE returning — the CLI prints its own prompts
// around this. The token is always "" (paste carries no token; the blob
// itself is the rendezvous). The returned wait reads one line from In,
// decodes it, and requires an answer.
//
// Cancellation: reads are blocking I/O run in a goroutine, so canceling
// wait's ctx returns promptly with ctx.Err(). The underlying Read stays
// blocked until a line arrives on In (or In errors), after which the
// goroutine exits — it is parked, not leaked, but only input unblocks it.
func (p *Paste) Announce(ctx context.Context, offer webrtc.SessionDescription) (string, func(context.Context) (webrtc.SessionDescription, error), error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if p.Out == nil {
		return "", nil, errors.New("signal: paste: Out is nil")
	}
	blob, err := EncodePayload(offer)
	if err != nil {
		return "", nil, err
	}
	if _, err := io.WriteString(p.Out, blob+"\n"); err != nil {
		return "", nil, fmt.Errorf("signal: paste: write offer: %w", err)
	}
	wait := func(waitCtx context.Context) (webrtc.SessionDescription, error) {
		desc, err := p.readPayload(waitCtx)
		if err != nil {
			return webrtc.SessionDescription{}, err
		}
		if desc.Type != webrtc.SDPTypeAnswer {
			return webrtc.SessionDescription{}, fmt.Errorf("signal: paste: peer sent %q, want answer", desc.Type)
		}
		return desc, nil
	}
	return "", wait, nil
}

// Join implements Channel: token is ignored — the paste channel has no
// token; the peer's blob arrives on In instead. Join reads one line from
// In, decodes it, and requires an offer. The returned respond encodes the
// answer and writes it as one line to Out. Cancellation semantics match
// Announce's wait (documented there).
func (p *Paste) Join(ctx context.Context, _ string) (webrtc.SessionDescription, func(context.Context, webrtc.SessionDescription) error, error) {
	desc, err := p.readPayload(ctx)
	if err != nil {
		return webrtc.SessionDescription{}, nil, err
	}
	if desc.Type != webrtc.SDPTypeOffer {
		return webrtc.SessionDescription{}, nil, fmt.Errorf("signal: paste: peer sent %q, want offer", desc.Type)
	}
	respond := func(rCtx context.Context, answer webrtc.SessionDescription) error {
		if err := rCtx.Err(); err != nil {
			return err
		}
		if p.Out == nil {
			return errors.New("signal: paste: Out is nil")
		}
		blob, err := EncodePayload(answer)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(p.Out, blob+"\n"); err != nil {
			return fmt.Errorf("signal: paste: write answer: %w", err)
		}
		return nil
	}
	return desc, respond, nil
}

// readPayload reads one blob line from In and decodes it. See Announce
// for the cancellation contract of the blocking read.
func (p *Paste) readPayload(ctx context.Context) (webrtc.SessionDescription, error) {
	if err := ctx.Err(); err != nil {
		return webrtc.SessionDescription{}, err
	}
	if p.In == nil {
		return webrtc.SessionDescription{}, errors.New("signal: paste: In is nil")
	}
	type result struct {
		line []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := readLine(p.In, p.maxLine())
		ch <- result{line, err}
	}()
	select {
	case <-ctx.Done():
		return webrtc.SessionDescription{}, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return webrtc.SessionDescription{}, fmt.Errorf("signal: paste: read: %w", r.err)
		}
		return DecodePayload(string(r.line))
	}
}

// readLine reads a single '\n'-terminated line from r, capped at max
// bytes (the delimiter included).
func readLine(r io.Reader, max int) ([]byte, error) {
	line, err := bufio.NewReaderSize(r, max).ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return nil, fmt.Errorf("line exceeds %d bytes", max)
	}
	return line, err
}
