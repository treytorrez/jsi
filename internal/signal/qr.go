package signal

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/pion/webrtc/v4"

	"github.com/treyt/jsi/internal/qr"
)

// QR exchanges one SDP over animated terminal QR frames and the other over a
// paste blob (QP/1; the D16 asymmetry — camera scanning lives in the PWA, so
// the CLI displays frames and reads blobs).
//
//   - Out: QR frames render here (a terminal; the CLI uses stderr).
//   - In: blob lines are read here (the CLI uses stdin).
//   - BlobOut: if non-nil, the local SDP is ALSO emitted as a paste blob line
//     (the CLI sets stdout, so terminal-to-terminal transfers work alongside
//     camera ones).
//
// Announce displays the offer frames (and optional blob), then waits for the
// answer blob. Join reads the offer blob, and its respond renders the answer
// frames (and optional blob). Verify with var _ Channel = (*QR)(nil) below.
type QR struct {
	Out     io.Writer
	In      io.Reader
	BlobOut io.Writer

	FPS          float64
	MaxLineBytes int
}

var _ Channel = (*QR)(nil)

// Announce implements Channel. The frame animation runs until the returned
// wait func resolves (answer pasted) or ctx ends.
func (q *QR) Announce(ctx context.Context, offer webrtc.SessionDescription) (string, func(context.Context) (webrtc.SessionDescription, error), error) {
	if q.Out == nil {
		return "", nil, errors.New("signal: qr: Out is nil")
	}
	if q.In == nil {
		return "", nil, errors.New("signal: qr: In is nil")
	}
	frames, err := SplitFrames(offer, 0)
	if err != nil {
		return "", nil, fmt.Errorf("signal: qr: %w", err)
	}
	rctx, stop := context.WithCancel(ctx)
	go func() { _ = qr.RenderLoop(rctx, q.Out, frames, qr.RenderOptions{FPS: q.FPS}) }()

	if q.BlobOut != nil {
		line, err := EncodePayload(offer)
		if err != nil {
			stop()
			return "", nil, fmt.Errorf("signal: qr: %w", err)
		}
		if _, err := fmt.Fprintln(q.BlobOut, line); err != nil {
			stop()
			return "", nil, fmt.Errorf("signal: qr: write blob: %w", err)
		}
	}

	wait := func(ctx context.Context) (webrtc.SessionDescription, error) {
		defer stop()
		p := &Paste{In: q.In, MaxLineBytes: q.MaxLineBytes}
		desc, err := p.readPayload(ctx)
		if err != nil {
			return webrtc.SessionDescription{}, err
		}
		if desc.Type != webrtc.SDPTypeAnswer {
			return webrtc.SessionDescription{}, fmt.Errorf("signal: qr: expected answer blob, got %s", desc.Type)
		}
		return desc, nil
	}
	return "", wait, nil
}

// Join implements Channel: reads the offer blob from In (token ignored, as
// with Paste). The returned respond renders the answer frames to Out (until
// the respond ctx ends — the CLI cancels once connected) and, if BlobOut is
// set, also writes the answer blob line there.
func (q *QR) Join(ctx context.Context, _ string) (webrtc.SessionDescription, func(context.Context, webrtc.SessionDescription) error, error) {
	blobOut := io.Writer(io.Discard)
	if q.BlobOut != nil {
		blobOut = q.BlobOut
	}
	p := &Paste{In: q.In, Out: blobOut, MaxLineBytes: q.MaxLineBytes}
	offer, respondPaste, err := p.Join(ctx, "")
	if err != nil {
		return webrtc.SessionDescription{}, nil, err
	}
	respond := func(ctx context.Context, answer webrtc.SessionDescription) error {
		if err := respondPaste(ctx, answer); err != nil {
			return err
		}
		if q.Out == nil {
			return errors.New("signal: qr: Out is nil")
		}
		frames, err := SplitFrames(answer, 0)
		if err != nil {
			return fmt.Errorf("signal: qr: %w", err)
		}
		go func() { _ = qr.RenderLoop(ctx, q.Out, frames, qr.RenderOptions{FPS: q.FPS}) }()
		return nil
	}
	return offer, respond, nil
}
