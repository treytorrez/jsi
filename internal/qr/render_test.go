package qr

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestRenderOnce(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderOnce(&buf, []byte("JSI1-test-frame")); err != nil {
		t.Fatalf("RenderOnce: %v", err)
	}
	if !strings.Contains(buf.String(), "█") && !strings.Contains(buf.String(), "▄") && !strings.Contains(buf.String(), "▀") {
		t.Error("output contains no QR block characters")
	}
}

func TestRenderOnceBinarySafe(t *testing.T) {
	frame := make([]byte, 256)
	for i := range frame {
		frame[i] = byte(i) // includes NUL and invalid-UTF-8 bytes
	}
	var buf bytes.Buffer
	if err := RenderOnce(&buf, frame); err != nil {
		t.Fatalf("RenderOnce with binary frame: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("no output for binary frame")
	}
}

func TestRenderOnceEmpty(t *testing.T) {
	if err := RenderOnce(&bytes.Buffer{}, nil); err == nil {
		t.Fatal("RenderOnce(nil): want error")
	}
}

func TestRenderLoopCyclesAndStops(t *testing.T) {
	frames := [][]byte{[]byte("frame-a"), []byte("frame-b"), []byte("frame-c")}
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()

	var buf bytes.Buffer
	start := time.Now()
	if err := RenderLoop(ctx, &buf, frames, RenderOptions{FPS: 60}); err != nil {
		t.Fatalf("RenderLoop: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("RenderLoop did not stop promptly on ctx: %s", elapsed)
	}
	out := buf.String()
	for _, want := range []string{"frame 1/3", "frame 2/3", "frame 3/3"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing caption %q", want)
		}
	}
	// 350ms at 60fps cycles the 3-frame loop ~7 times: expect repeated cycles.
	if got := strings.Count(out, "frame 1/3"); got < 2 {
		t.Errorf("expected animation cycles, saw frame 1/3 %d time(s)", got)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Error("output missing ANSI clear sequences between frames")
	}
}

func TestRenderLoopEmpty(t *testing.T) {
	if err := RenderLoop(context.Background(), &bytes.Buffer{}, nil, RenderOptions{}); err == nil {
		t.Fatal("RenderLoop(nil): want error")
	}
}
