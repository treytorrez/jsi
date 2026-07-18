package signal_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/pion/webrtc/v4"
	"github.com/treyt/jsi/internal/signal"
)

// cloneFrames deep-copies a frame set so corruption cases can't leak
// mutations into sibling subtests.
func cloneFrames(frames [][]byte) [][]byte {
	out := make([][]byte, len(frames))
	for i, f := range frames {
		out[i] = bytes.Clone(f)
	}
	return out
}

// splitRealistic splits the realistic 2–3 KB SDP fixture into frames of
// chunkSize payload bytes and fails the test on error.
func splitRealistic(t *testing.T, chunkSize int) (webrtc.SessionDescription, [][]byte) {
	t.Helper()
	desc := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: realisticSDP()}
	frames, err := signal.SplitFrames(desc, chunkSize)
	if err != nil {
		t.Fatalf("SplitFrames: %v", err)
	}
	return desc, frames
}

// TestSplitFrameLayout pins the normative QP/1 wire layout
// (proto/SIGNALING.md §QR frames): magic at 0, seq/total big-endian at
// 4/6, crc32 of the full compressed payload at 8, chunk at 12 — and the
// payload being the very bytes the paste blob carries.
func TestSplitFrameLayout(t *testing.T) {
	const chunk = 300
	desc, frames := splitRealistic(t, chunk)
	if len(frames) < 2 {
		t.Fatalf("realistic SDP split into %d frame(s), want ≥ 2 at %d B chunks", len(frames), chunk)
	}

	var crc uint32
	var payload bytes.Buffer
	for i, f := range frames {
		if len(f) < signal.FrameHeaderLen {
			t.Fatalf("frame %d length %d < header %d", i, len(f), signal.FrameHeaderLen)
		}
		if got := string(f[:4]); got != signal.FrameMagic {
			t.Errorf("frame %d magic %q, want %q", i, got, signal.FrameMagic)
		}
		if got := int(binary.BigEndian.Uint16(f[4:])); got != i+1 {
			t.Errorf("frame %d seq %d, want %d (1-based, in order)", i, got, i+1)
		}
		if got := int(binary.BigEndian.Uint16(f[6:])); got != len(frames) {
			t.Errorf("frame %d total %d, want %d", i, got, len(frames))
		}
		c := binary.BigEndian.Uint32(f[8:])
		if i == 0 {
			crc = c
		} else if c != crc {
			t.Errorf("frame %d crc32 %#08x, want %#08x (same on every frame)", i, c, crc)
		}
		chunkBytes := f[signal.FrameHeaderLen:]
		if i < len(frames)-1 && len(chunkBytes) != chunk {
			t.Errorf("frame %d chunk %d bytes, want %d (only the last frame is short)", i, len(chunkBytes), chunk)
		}
		payload.Write(chunkBytes)
	}
	if got := crc32.ChecksumIEEE(payload.Bytes()); got != crc {
		t.Errorf("concatenated chunks crc32 %#08x, want header %#08x", got, crc)
	}

	// The framed payload must be the same §Payload bytes the paste blob
	// carries: wrap the concatenated chunks as a paste blob and decode.
	blob := "jsi1:" + base64.RawURLEncoding.EncodeToString(payload.Bytes())
	got, err := signal.DecodePayload(blob)
	if err != nil {
		t.Fatalf("DecodePayload over frame payload: %v", err)
	}
	if !descEqual(got, desc) {
		t.Errorf("frame payload decodes to %+v, want %+v", got, desc)
	}
}

// TestFrameRoundTrip: realistic SDP → Split (default chunk) → Join is
// byte-equal, and a small SDP fits in a single frame.
func TestFrameRoundTrip(t *testing.T) {
	t.Run("realistic multi-frame", func(t *testing.T) {
		desc, frames := splitRealistic(t, 0)
		if len(frames) < 2 {
			t.Fatalf("got %d frame(s) at default chunk, want a multi-frame set", len(frames))
		}
		got, err := signal.JoinFrames(frames)
		if err != nil {
			t.Fatalf("JoinFrames: %v", err)
		}
		if !descEqual(got, desc) {
			t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, desc)
		}
	})

	t.Run("single frame", func(t *testing.T) {
		frames, err := signal.SplitFrames(offerDesc, 0)
		if err != nil {
			t.Fatalf("SplitFrames: %v", err)
		}
		if len(frames) != 1 {
			t.Fatalf("small SDP split into %d frames, want 1", len(frames))
		}
		if got := int(binary.BigEndian.Uint16(frames[0][6:])); got != 1 {
			t.Errorf("total = %d, want 1", got)
		}
		got, err := signal.JoinFrames(frames)
		if err != nil {
			t.Fatalf("JoinFrames: %v", err)
		}
		if !descEqual(got, offerDesc) {
			t.Errorf("round-trip mismatch: got %+v, want %+v", got, offerDesc)
		}
	})

	t.Run("chunkSize ≤ 0 means DefaultFrameChunk", func(t *testing.T) {
		desc := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: realisticSDP()}
		def, err := signal.SplitFrames(desc, signal.DefaultFrameChunk)
		if err != nil {
			t.Fatalf("SplitFrames(default): %v", err)
		}
		for _, cs := range []int{0, -1, -4000} {
			got, err := signal.SplitFrames(desc, cs)
			if err != nil {
				t.Fatalf("SplitFrames(%d): %v", cs, err)
			}
			if len(got) != len(def) {
				t.Fatalf("SplitFrames(%d) made %d frames, want %d", cs, len(got), len(def))
			}
			for i := range def {
				if !bytes.Equal(got[i], def[i]) {
					t.Fatalf("SplitFrames(%d) frame %d differs from default", cs, i)
				}
			}
		}
	})
}

// TestJoinOrderIndependent: a shuffled frame set (what a camera collects
// over several loop passes) joins to the same description.
func TestJoinOrderIndependent(t *testing.T) {
	desc, frames := splitRealistic(t, 64)
	rng := rand.New(rand.NewPCG(42, 7))
	rng.Shuffle(len(frames), func(i, j int) { frames[i], frames[j] = frames[j], frames[i] })

	got, err := signal.JoinFrames(frames)
	if err != nil {
		t.Fatalf("JoinFrames(shuffled): %v", err)
	}
	if !descEqual(got, desc) {
		t.Errorf("shuffled join mismatch:\n got %+v\nwant %+v", got, desc)
	}
}

// TestJoinFrameLoss: dropping frames fails with an error naming exactly
// the missing seqs; duplicate frames are tolerated silently.
func TestJoinFrameLoss(t *testing.T) {
	desc, frames := splitRealistic(t, 64)
	total := len(frames)
	if total <= 9 {
		t.Fatalf("fixture produced %d frames, want > 9 for the loss case", total)
	}

	dropped := make([][]byte, 0, total-2)
	for i, f := range frames {
		if i+1 == 4 || i+1 == 9 { // seqs are 1-based
			continue
		}
		dropped = append(dropped, f)
	}
	_, err := signal.JoinFrames(dropped)
	if err == nil {
		t.Fatal("JoinFrames with 2 dropped frames succeeded")
	}
	want := fmt.Sprintf("missing 2 of %d frames: 4 9", total)
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want substring %q", err, want)
	}

	dup := append(cloneFrames(frames), bytes.Clone(frames[2]), bytes.Clone(frames[2]))
	got, err := signal.JoinFrames(dup)
	if err != nil {
		t.Fatalf("JoinFrames with duplicates: %v", err)
	}
	if !descEqual(got, desc) {
		t.Errorf("duplicate-tolerant join mismatch: got %+v, want %+v", got, desc)
	}
}

// TestJoinCorruption feeds one malformed frame set per case; every
// failure mode of the decoder chain must produce its descriptive error.
func TestJoinCorruption(t *testing.T) {
	_, frames := splitRealistic(t, 64)

	tests := []struct {
		name    string
		mutate  func(f [][]byte) [][]byte
		wantErr string
	}{
		{
			name:    "no frames",
			mutate:  func(f [][]byte) [][]byte { return nil },
			wantErr: "no frames",
		},
		{
			name: "bad magic",
			mutate: func(f [][]byte) [][]byte {
				f[0][0] = 'X'
				return f
			},
			wantErr: "bad magic",
		},
		{
			name: "truncated frame",
			mutate: func(f [][]byte) [][]byte {
				f[1] = f[1][:signal.FrameHeaderLen-1]
				return f
			},
			wantErr: "truncated",
		},
		{
			name: "total zero",
			mutate: func(f [][]byte) [][]byte {
				binary.BigEndian.PutUint16(f[0][6:], 0)
				return f
			},
			wantErr: "total is 0",
		},
		{
			name: "seq zero",
			mutate: func(f [][]byte) [][]byte {
				binary.BigEndian.PutUint16(f[0][4:], 0)
				return f
			},
			wantErr: "outside 1..",
		},
		{
			name: "seq beyond total",
			mutate: func(f [][]byte) [][]byte {
				binary.BigEndian.PutUint16(f[0][4:], uint16(len(f))+1)
				return f
			},
			wantErr: "outside 1..",
		},
		{
			name: "mixed totals",
			mutate: func(f [][]byte) [][]byte {
				binary.BigEndian.PutUint16(f[1][6:], uint16(len(f))+5)
				return f
			},
			wantErr: "mixed frame sets",
		},
		{
			name: "mixed crc32",
			mutate: func(f [][]byte) [][]byte {
				f[1][8] ^= 0xFF
				return f
			},
			wantErr: "mixed frame sets",
		},
		{
			name: "bit flip in chunk",
			mutate: func(f [][]byte) [][]byte {
				f[1][signal.FrameHeaderLen] ^= 0xFF
				return f
			},
			wantErr: "crc32 mismatch",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := signal.JoinFrames(tc.mutate(cloneFrames(frames)))
			if err == nil {
				t.Fatalf("want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want substring %q", err, tc.wantErr)
			}
		})
	}
}

// TestJoinMixedFrameSets: frames from two different splits of the same
// description (different chunk sizes → different totals) are rejected.
func TestJoinMixedFrameSets(t *testing.T) {
	_, a := splitRealistic(t, 64)
	_, b := splitRealistic(t, 256)
	if len(a) == len(b) {
		t.Fatalf("both splits made %d frames; pick chunk sizes that differ", len(a))
	}
	_, err := signal.JoinFrames(append(cloneFrames(a), b[0]))
	if err == nil || !strings.Contains(err.Error(), "mixed frame sets") {
		t.Fatalf("JoinFrames(mixed sets) error = %v, want 'mixed frame sets'", err)
	}
}

// TestSplitTooManyFrames: a payload needing more than 65535 frames
// (uint16 total on the wire) is rejected at split time.
func TestSplitTooManyFrames(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	big := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdpIsh(rng, 200_000)}
	_, err := signal.SplitFrames(big, 1)
	if err == nil || !strings.Contains(err.Error(), "max") {
		t.Fatalf("SplitFrames(chunkSize 1) error = %v, want 'max'", err)
	}
}

// sdpIsh returns a random valid-UTF-8 string of n bytes shaped vaguely
// like SDP content (printable ASCII plus line breaks), so the JSON →
// zlib → JSON round-trip must reproduce it byte-exactly.
func sdpIsh(rng *rand.Rand, n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789=:\r\n. -"
	var b strings.Builder
	b.Grow(n)
	for b.Len() < n {
		b.WriteByte(alphabet[rng.IntN(len(alphabet))])
	}
	return b.String()
}

// TestFrameRoundTripSizes is the property-ish sweep: random SDP-ish
// payloads at the chunk-boundary sizes (and the degenerate empties)
// survive Split → Join byte-equal.
func TestFrameRoundTripSizes(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, n := range []int{0, 1, 399, 400, 401, 4000} {
		t.Run(fmt.Sprintf("size %d", n), func(t *testing.T) {
			desc := webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdpIsh(rng, n)}
			frames, err := signal.SplitFrames(desc, 0)
			if err != nil {
				t.Fatalf("SplitFrames: %v", err)
			}
			got, err := signal.JoinFrames(frames)
			if err != nil {
				t.Fatalf("JoinFrames: %v", err)
			}
			if !descEqual(got, desc) {
				t.Errorf("round-trip mismatch at size %d:\n got %+v\nwant %+v", n, got, desc)
			}
		})
	}
}
