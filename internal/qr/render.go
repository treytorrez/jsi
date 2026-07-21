// Package qr renders QP/1 frames as QR codes in a terminal (M4.3): RenderOnce
// for a single frame, RenderLoop for the animated multi-frame cycle a camera
// scans. Per D16 the CLI only displays — scanning lives in the PWA.
package qr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/mdp/qrterminal/v3"
	"rsc.io/qr"
)

// DefaultFPS is the default animation rate (frames per second).
const DefaultFPS = 2.0

// RenderOptions tunes RenderLoop.
type RenderOptions struct {
	// FPS is the animation rate; <= 0 means DefaultFPS.
	FPS float64
	// QuietZone is the QR quiet zone in modules; <= 0 means 2 (QP/1).
	QuietZone int
}

func (o RenderOptions) fps() float64 {
	if o.FPS > 0 {
		return o.FPS
	}
	return DefaultFPS
}

func (o RenderOptions) quietZone() int {
	if o.QuietZone > 0 {
		return o.QuietZone
	}
	return 2
}

// renderBlock renders one frame (arbitrary binary content, QR byte mode) with
// its caption into buf, returning the number of terminal lines written.
func renderBlock(buf *bytes.Buffer, i, n int, frame []byte, quietZone int) int {
	qrterminal.GenerateWithConfig(string(frame), qrterminal.Config{
		Level:      qr.M, // EC level M per spike M4.1 (glare/motion robustness)
		Writer:     buf,
		HalfBlocks: true,
		QuietZone:  quietZone,
	})
	fmt.Fprintf(buf, "frame %d/%d — point the camera at the screen\n", i+1, n)
	return bytes.Count(buf.Bytes(), []byte("\n"))
}

// RenderOnce renders a single frame (no caption) to w.
func RenderOnce(w io.Writer, frame []byte) error {
	if len(frame) == 0 {
		return errors.New("qr: empty frame")
	}
	qrterminal.GenerateWithConfig(string(frame), qrterminal.Config{
		Level:      qr.M,
		Writer:     w,
		HalfBlocks: true,
		QuietZone:  2,
	})
	return nil
}

// RenderLoop cycles frames as an animated QR at opts.FPS until ctx is done.
// Stopping via ctx is the normal end of display (the peer answered or the
// connection opened), so it returns nil, never ctx.Err(). Between frames it
// erases the previous block with ANSI cursor-up + erase-below; the caller is
// responsible for not interleaving other output on w.
func RenderLoop(ctx context.Context, w io.Writer, frames [][]byte, opts RenderOptions) error {
	if len(frames) == 0 {
		return errors.New("qr: no frames to render")
	}
	interval := time.Duration(float64(time.Second) / opts.fps())
	prevLines := 0
	for i := 0; ; i = (i + 1) % len(frames) {
		var buf bytes.Buffer
		renderBlock(&buf, i, len(frames), frames[i], opts.quietZone())
		if prevLines > 0 {
			if _, err := fmt.Fprintf(w, "\x1b[%dA\x1b[J", prevLines); err != nil { // up N lines, erase below
				return fmt.Errorf("qr: render: %w", err)
			}
		}
		prevLines = buf.Len()
		if _, err := w.Write(buf.Bytes()); err != nil {
			return fmt.Errorf("qr: render: %w", err)
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
}
