// Command qrsize is the spike/qr-size measurement tool (task M4.1). It
// generates real non-trickle offer/answer SDP pairs via internal/peer
// across the D15 ICE configurations, runs each SDP through the QP/1
// payload pipeline (proto/SIGNALING.md §QP/1: JSEP JSON → zlib deflate →
// base64url), and prints markdown tables of measured sizes and frame
// counts at candidate chunk sizes. Spike-only tooling: it lives on the
// spike/qr-size branch and is not part of the shipped CLI.
package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
	"rsc.io/qr"

	"github.com/treyt/jsi/internal/peer"
)

var (
	nSamples = flag.Int("n", 5, "offer/answer pairs to generate per configuration")
	dump     = flag.Bool("dump", false, "dump one sample offer SDP per configuration to stderr")
)

// chunkSizes are the candidates for the QP/1 default chunkSize
// (proto/SIGNALING.md §QR frames).
var chunkSizes = []int{300, 400, 500, 700}

// frameHeader is the QP/1 per-frame overhead: magic(4) + seq(2) +
// total(2) + crc32(4).
const frameHeader = 12

type config struct {
	id    string
	desc  string
	peer  peer.Config
	munge bool // strip a=extmap/a=msid/a=ssrc lines before compressing (config E)
}

func configs() []config {
	stun := []webrtc.ICEServer{{URLs: []string{
		"stun:stun.cloudflare.com:3478",
		"stun:stun.l.google.com:19302",
	}}}
	// Mock of the CF Realtime ICE server block the worker returns from
	// GET /v1/ice (SP/1): the typical 6-URL list with 128-hex creds (D7).
	// The fake credentials never yield a relay allocation — the size
	// effect measured here is the TURN block's influence on gathering,
	// and any relay candidates a real allocation would add are noted in
	// the report.
	turn := webrtc.ICEServer{
		URLs: []string{
			"turn:turn.cloudflare.com:3478?transport=udp",
			"turn:turn.cloudflare.com:3478?transport=tcp",
			"turns:turn.cloudflare.com:5349?transport=tcp",
			"turn:turn.cloudflare.com:53?transport=udp",
			"turn:turn.cloudflare.com:80?transport=tcp",
			"turns:turn.cloudflare.com:443?transport=tcp",
		},
		Username:   strings.Repeat("0123456789abcdef", 8), // 128 hex chars
		Credential: strings.Repeat("fedcba9876543210", 8), // 128 hex chars
	}
	full := append(append([]webrtc.ICEServer{}, stun...), turn)
	return []config{
		{"A", "host only, mDNS off", peer.Config{EnableMDNS: false}, false},
		{"B", "host only, mDNS on (D14 production default)", peer.Config{EnableMDNS: true}, false},
		{"C", "host + 2 STUN, mDNS on (D15 `none` preset)", peer.Config{EnableMDNS: true, ICEServers: stun}, false},
		{"D", "C + mocked 6-URL TURN block (simulated `full`)", peer.Config{EnableMDNS: true, ICEServers: full}, false},
		{"E", "C with a=extmap/a=msid/a=ssrc stripped (munge reference; v1 forbids munging on the wire)", peer.Config{EnableMDNS: true, ICEServers: stun}, true},
	}
}

// sample is one SDP pushed through the QP/1 payload pipeline.
type sample struct {
	sdpLen   int // raw SDP text bytes (before JSON wrapping)
	raw      int // JSEP JSON bytes (pipeline input)
	deflated int // zlib bytes (pipeline output; what frames carry)
	b64      int // base64url(no-pad) length of the deflated payload
	candH    int // a=candidate ... typ host lines
	candS    int // a=candidate ... typ srflx lines
	candR    int // a=candidate ... typ relay lines
	mdns     int // candidate lines carrying an mDNS (.local) name
}

// measure runs one SDP text through QP/1 §Payload steps 1–2 (the exact
// codec of signal.EncodePayload) and records the stage sizes.
func measure(sdpText string, mungeIt bool) (sample, error) {
	if mungeIt {
		sdpText = mungeSDP(sdpText)
	}
	raw, err := json.Marshal(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdpText})
	if err != nil {
		return sample{}, fmt.Errorf("marshal JSEP: %w", err)
	}
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf) // default compression level, per spec
	if _, err := zw.Write(raw); err != nil {
		return sample{}, fmt.Errorf("deflate: %w", err)
	}
	if err := zw.Close(); err != nil {
		return sample{}, fmt.Errorf("deflate close: %w", err)
	}
	s := sample{
		sdpLen:   len(sdpText),
		raw:      len(raw),
		deflated: buf.Len(),
		b64:      base64.RawURLEncoding.EncodedLen(buf.Len()),
	}
	s.candH, s.candS, s.candR, s.mdns = candidateCounts(sdpText)
	return s, nil
}

// mungeSDP drops a=extmap, a=msid (incl. a=msid-semantic) and a=ssrc
// lines. QP/1 v1 forbids munging on the wire; config E exists only to
// quantify what that decision leaves on the table.
func mungeSDP(sdp string) string {
	lines := strings.Split(sdp, "\r\n")
	keep := make([]string, 0, len(lines))
	for _, ln := range lines {
		if strings.HasPrefix(ln, "a=extmap") ||
			strings.HasPrefix(ln, "a=msid") ||
			strings.HasPrefix(ln, "a=ssrc") {
			continue
		}
		keep = append(keep, ln)
	}
	return strings.Join(keep, "\r\n")
}

// candidateCounts tallies candidate lines by type and mDNS usage.
func candidateCounts(sdp string) (host, srflx, relay, mdns int) {
	for _, ln := range strings.Split(sdp, "\r\n") {
		if !strings.HasPrefix(ln, "a=candidate:") {
			continue
		}
		switch {
		case strings.Contains(ln, " typ host"):
			host++
		case strings.Contains(ln, " typ srflx"):
			srflx++
		case strings.Contains(ln, " typ relay"):
			relay++
		}
		if strings.Contains(ln, ".local") {
			mdns++
		}
	}
	return host, srflx, relay, mdns
}

// generatePair creates one offer/answer pair under cfg and measures both
// SDPs. The answerer never connects; only the descriptions matter. The
// pristine offer SDP text is returned for -dump inspection.
func generatePair(c config) (offer, answer sample, offerSDP string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	oc, odesc, err := peer.Offer(ctx, c.peer)
	if err != nil {
		return sample{}, sample{}, "", fmt.Errorf("offer: %w", err)
	}
	defer func() { _ = oc.Close() }()

	ac, adesc, err := peer.Answer(ctx, c.peer, odesc)
	if err != nil {
		return sample{}, sample{}, "", fmt.Errorf("answer: %w", err)
	}
	defer func() { _ = ac.Close() }()

	offer, err = measure(odesc.SDP, c.munge)
	if err != nil {
		return sample{}, sample{}, "", err
	}
	answer, err = measure(adesc.SDP, c.munge)
	if err != nil {
		return sample{}, sample{}, "", err
	}
	return offer, answer, odesc.SDP, nil
}

// minMedMax returns min, median, max of xs (xs must be non-empty).
func minMedMax(xs []int) (int, int, int) {
	s := append([]int(nil), xs...)
	sort.Ints(s)
	return s[0], s[len(s)/2], s[len(s)-1]
}

// cell formats "med (min–max)", collapsing to "med" when all agree.
func cell(xs []int) string {
	mn, md, mx := minMedMax(xs)
	if mn == mx {
		return fmt.Sprintf("%d", md)
	}
	return fmt.Sprintf("%d (%d–%d)", md, mn, mx)
}

func cellRatio(raw, def []int) string {
	rs := make([]float64, len(raw))
	for i := range raw {
		rs[i] = float64(raw[i]) / float64(def[i])
	}
	sort.Float64s(rs)
	mn, md, mx := rs[0], rs[len(rs)/2], rs[len(rs)-1]
	if mn == mx {
		return fmt.Sprintf("%.2fx", md)
	}
	return fmt.Sprintf("%.2fx (%.2f–%.2f)", md, mn, mx)
}

// qrVersion returns the minimal QR version carrying nBytes of binary
// payload in byte mode at EC level M, encoded with rsc.io/qr (the same
// encoder mdp/qrterminal renders). High bytes force 8-bit segments, so
// no accidental numeric/alphanumeric compaction flatters the result.
func qrVersion(nBytes int) (int, error) {
	b := make([]byte, nBytes)
	for i := range b {
		b[i] = 0x80 | byte(i*37+11)
	}
	code, err := qr.Encode(string(b), qr.M)
	if err != nil {
		return 0, err
	}
	return (code.Size-21)/4 + 1, nil
}

func frames(payload, chunk int) int {
	return (payload + chunk - 1) / chunk
}

func main() {
	flag.Parse()
	fmt.Println("# qrsize — QP/1 payload measurements (spike M4.1)")
	fmt.Println()
	fmt.Printf("Tool: cmd/qrsize on branch spike/qr-size · %s · pion/webrtc v4 · %d offer+answer pairs per config · zlib default level\n\n", runtime.Version(), *nSamples)

	cfgs := configs()
	fmt.Println("Configurations:")
	for _, c := range cfgs {
		fmt.Printf("- **%s** — %s\n", c.id, c.desc)
	}
	fmt.Println()

	type result struct {
		offers, answers []sample
	}
	results := make(map[string]result, len(cfgs))
	for _, c := range cfgs {
		var r result
		for i := 0; i < *nSamples; i++ {
			o, a, oSDP, err := generatePair(c)
			if err != nil {
				fmt.Fprintf(os.Stderr, "config %s sample %d: %v\n", c.id, i, err)
				os.Exit(1)
			}
			r.offers = append(r.offers, o)
			r.answers = append(r.answers, a)
			if *dump && i == 0 {
				fmt.Fprintf(os.Stderr, "--- config %s offer SDP (%d B) ---\n%s\n", c.id, len(oSDP), oSDP)
			}
		}
		results[c.id] = r
	}

	// Table 1: pipeline stage sizes, offer side (+ answer zlib for the
	// return direction).
	fmt.Println("## Payload pipeline sizes (offer side; medians, min–max in parens)")
	fmt.Println()
	fmt.Println("| cfg | cands h/s/r (mDNS) | raw SDP B | raw JSEP B | zlib B | ratio | b64url B | answer zlib B |")
	fmt.Println("|---|---|---|---|---|---|---|---|")
	for _, c := range cfgs {
		r := results[c.id]
		var sdpL, raw, def, b64, ansDef []int
		for i, o := range r.offers {
			sdpL = append(sdpL, o.sdpLen)
			raw = append(raw, o.raw)
			def = append(def, o.deflated)
			b64 = append(b64, o.b64)
			ansDef = append(ansDef, r.answers[i].deflated)
		}
		var hs, ss, rs, ms []int
		for _, o := range r.offers {
			hs = append(hs, o.candH)
			ss = append(ss, o.candS)
			rs = append(rs, o.candR)
			ms = append(ms, o.mdns)
		}
		_, hMed, _ := minMedMax(hs)
		_, sMed, _ := minMedMax(ss)
		_, rMed, _ := minMedMax(rs)
		_, mMed, _ := minMedMax(ms)
		fmt.Printf("| %s | %d/%d/%d (%d) | %s | %s | %s | %s | %s | %s |\n",
			c.id, hMed, sMed, rMed, mMed, cell(sdpL), cell(raw), cell(def),
			cellRatio(raw, def), cell(b64), cell(ansDef))
	}
	fmt.Println()

	// Table 2: frame counts from the worst direction of each pair.
	fmt.Println("## QP/1 frame counts (worst of offer/answer per pair; medians, min–max in parens)")
	fmt.Println()
	hdr := "| cfg | worst-dir zlib B (max) |"
	sep := "|---|---|"
	for _, cs := range chunkSizes {
		hdr += fmt.Sprintf(" frames @%d B |", cs)
		sep += "---|"
	}
	fmt.Println(hdr)
	fmt.Println(sep)
	for _, c := range cfgs {
		r := results[c.id]
		worst := make([]int, len(r.offers))
		for i := range r.offers {
			worst[i] = max(r.offers[i].deflated, r.answers[i].deflated)
		}
		_, _, wMax := minMedMax(worst)
		row := fmt.Sprintf("| %s | %d |", c.id, wMax)
		for _, cs := range chunkSizes {
			fs := make([]int, len(worst))
			for i, w := range worst {
				fs[i] = frames(w, cs)
			}
			row += fmt.Sprintf(" %s |", cell(fs))
		}
		fmt.Println(row)
	}
	fmt.Println()

	// Table 3: chunk size → minimal QR version at EC level M.
	fmt.Println("## Chunk size → minimal QR version (byte mode, EC level M, rsc.io/qr)")
	fmt.Println()
	fmt.Println("| chunk B | frame B (chunk + 12 B header) | minimal QR version @M |")
	fmt.Println("|---|---|---|")
	for _, cs := range chunkSizes {
		v, err := qrVersion(cs + frameHeader)
		if err != nil {
			fmt.Printf("| %d | %d | error: %v |\n", cs, cs+frameHeader, err)
			continue
		}
		fmt.Printf("| %d | %d | V%d-M |\n", cs, cs+frameHeader, v)
	}
	fmt.Println()

	// Environment sanity + notable findings for the report.
	fmt.Println("## Environment sanity")
	fmt.Println()
	for _, c := range cfgs {
		r := results[c.id]
		var hs, ss, rs, ms []int
		for _, o := range r.offers {
			hs = append(hs, o.candH)
			ss = append(ss, o.candS)
			rs = append(rs, o.candR)
			ms = append(ms, o.mdns)
		}
		fmt.Printf("- cfg %s: median offer candidates host=%d srflx=%d relay=%d, of which %d carry .local mDNS names\n",
			c.id, med(hs), med(ss), med(rs), med(ms))
	}
	fmt.Println()

	fmt.Println("## Worst case (config D)")
	fmt.Println()
	r := results["D"]
	worst := make([]int, len(r.offers))
	for i := range r.offers {
		worst[i] = max(r.offers[i].deflated, r.answers[i].deflated)
	}
	_, _, wMax := minMedMax(worst)
	fmt.Printf("largest deflated D payload: %d B → frames:", wMax)
	for _, cs := range chunkSizes {
		fmt.Printf(" @%d=%d", cs, frames(wMax, cs))
	}
	fmt.Println()
}

func med(xs []int) int {
	_, md, _ := minMedMax(xs)
	return md
}
