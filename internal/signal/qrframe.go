package signal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"strconv"
	"strings"

	"github.com/pion/webrtc/v4"
)

// FrameMagic tags every QP/1 QR frame (proto/SIGNALING.md §QR frames).
const FrameMagic = "JSI1"

// FrameHeaderLen is the fixed per-frame prefix: 4 B magic + 2 B seq +
// 2 B total + 4 B crc32; the payload chunk follows at offset 12.
const FrameHeaderLen = 12

// DefaultFrameChunk is the payload bytes carried per frame when
// SplitFrames is called with chunkSize ≤ 0. The 400 B value is the one
// recorded in proto/SIGNALING.md §QR frames (QR byte mode, EC level M);
// spike M4.1 is measuring scannable density vs. animation rate and may
// retune it. It is a var, not a const, so that retune (or a test) is a
// one-line change — do not mutate it concurrently with SplitFrames.
var DefaultFrameChunk = 400

// maxFrameTotal bounds the frame count: total is a uint16 on the wire.
const maxFrameTotal = 1<<16 - 1

// SplitFrames encodes desc as QP/1 QR frames (proto/SIGNALING.md
// §QR frames): the §Payload bytes (JSEP JSON → zlib, the same pipeline
// the paste blob uses) are sliced into chunks of chunkSize bytes — or
// DefaultFrameChunk when chunkSize ≤ 0 — and each chunk is prefixed with
// a FrameHeaderLen header carrying its 1-based seq, the total, and the
// CRC-32/IEEE of the FULL payload. Frames are returned in seq order.
func SplitFrames(desc webrtc.SessionDescription, chunkSize int) ([][]byte, error) {
	if chunkSize <= 0 {
		chunkSize = DefaultFrameChunk
	}
	payload, err := deflatePayload(desc)
	if err != nil {
		return nil, fmt.Errorf("signal: qrframe: %w", err)
	}
	total := max((len(payload)+chunkSize-1)/chunkSize, 1)
	if total > maxFrameTotal {
		return nil, fmt.Errorf("signal: qrframe: payload %d bytes needs %d frames of %d bytes, max %d",
			len(payload), (len(payload)+chunkSize-1)/chunkSize, chunkSize, maxFrameTotal)
	}
	crc := crc32.ChecksumIEEE(payload)
	frames := make([][]byte, 0, total)
	for seq := 1; seq <= total; seq++ {
		start := (seq - 1) * chunkSize
		end := min(start+chunkSize, len(payload))
		f := make([]byte, FrameHeaderLen+end-start)
		copy(f, FrameMagic)
		binary.BigEndian.PutUint16(f[4:], uint16(seq))
		binary.BigEndian.PutUint16(f[6:], uint16(total))
		binary.BigEndian.PutUint32(f[8:], crc)
		copy(f[FrameHeaderLen:], payload[start:end])
		frames = append(frames, f)
	}
	return frames, nil
}

// JoinFrames reassembles a SessionDescription from QP/1 QR frames
// produced by SplitFrames. Frames may arrive in any order and duplicates
// are tolerated (first wins); all frames must agree on total and crc32,
// every seq in 1..total must be present, and the concatenated payload
// must match the crc32 before it is inflated and unmarshaled via the
// shared §Payload pipeline.
//
// Malformed input is an ERROR, never a silent skip: frames reaching this
// function come from one encoder (the peer's looping animation), so bad
// magic, truncation, total=0, seq outside 1..total, or mixed totals mean
// corruption or a confused peer, and a precise message beats a hang. The
// spec's "frames with wrong magic/length are skipped" rule
// (proto/SIGNALING.md §QR frames) applies one layer up, at the camera
// scanner, which filters the QR noise of an arbitrary scene before
// handing candidate frames here.
func JoinFrames(frames [][]byte) (webrtc.SessionDescription, error) {
	if len(frames) == 0 {
		return webrtc.SessionDescription{}, errors.New("signal: qrframe: no frames")
	}
	total := 0
	var crc uint32
	chunks := make(map[int][]byte, len(frames))
	for i, f := range frames {
		if len(f) < FrameHeaderLen {
			return webrtc.SessionDescription{}, fmt.Errorf("signal: qrframe: frame %d truncated: %d bytes < %d-byte header", i, len(f), FrameHeaderLen)
		}
		if string(f[:len(FrameMagic)]) != FrameMagic {
			return webrtc.SessionDescription{}, fmt.Errorf("signal: qrframe: frame %d bad magic %q, want %q", i, f[:len(FrameMagic)], FrameMagic)
		}
		seq := int(binary.BigEndian.Uint16(f[4:]))
		tot := int(binary.BigEndian.Uint16(f[6:]))
		c := binary.BigEndian.Uint32(f[8:])
		if tot == 0 {
			return webrtc.SessionDescription{}, fmt.Errorf("signal: qrframe: frame %d seq %d: total is 0", i, seq)
		}
		if seq < 1 || seq > tot {
			return webrtc.SessionDescription{}, fmt.Errorf("signal: qrframe: frame %d seq %d outside 1..%d", i, seq, tot)
		}
		if i == 0 {
			total, crc = tot, c
		} else if tot != total {
			return webrtc.SessionDescription{}, fmt.Errorf("signal: qrframe: frame %d total %d != %d of first frame (mixed frame sets)", i, tot, total)
		} else if c != crc {
			return webrtc.SessionDescription{}, fmt.Errorf("signal: qrframe: frame %d crc32 %#08x != %#08x of first frame (mixed frame sets)", i, c, crc)
		}
		if _, dup := chunks[seq]; !dup {
			chunks[seq] = f[FrameHeaderLen:]
		}
	}
	var missing []int
	for seq := 1; seq <= total; seq++ {
		if _, ok := chunks[seq]; !ok {
			missing = append(missing, seq)
		}
	}
	if len(missing) > 0 {
		seqs := make([]string, len(missing))
		for i, s := range missing {
			seqs[i] = strconv.Itoa(s)
		}
		return webrtc.SessionDescription{}, fmt.Errorf("signal: qrframe: missing %d of %d frames: %s",
			len(missing), total, strings.Join(seqs, " "))
	}
	var payload bytes.Buffer
	for seq := 1; seq <= total; seq++ {
		payload.Write(chunks[seq])
	}
	if got := crc32.ChecksumIEEE(payload.Bytes()); got != crc {
		return webrtc.SessionDescription{}, fmt.Errorf("signal: qrframe: crc32 mismatch: got %#08x, want %#08x (payload corrupt)", got, crc)
	}
	desc, err := inflatePayload(payload.Bytes())
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("signal: qrframe: payload %w", err)
	}
	return desc, nil
}
