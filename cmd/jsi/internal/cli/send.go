package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/treyt/jsi/internal/peer"
	"github.com/treyt/jsi/internal/signal"
	"github.com/treyt/jsi/internal/transfer"
)

// send runs `jsi send <file...>` (M3.1): resolve the D15 policy → create
// the offer → announce it (paste blob or worker token+QR, with fallback
// escalation) → connect → TP/1 send with progress bars → per-file sha256
// summary.
func (a *app) send(ctx context.Context, args []string) (bool, error) {
	fs, fv := newFlagSet("send", a.stderr)
	paths, err := parseFlags(fs, args)
	a.verbose = fv.verbose
	if err != nil {
		if errors.Is(err, errHelp) {
			usage(a.stdout)
			return a.verbose, nil
		}
		return a.verbose, err
	}
	if len(paths) == 0 {
		usage(a.stderr)
		return a.verbose, errors.New("send needs at least one file")
	}
	files, err := transfer.FilesFromPaths(paths)
	if err != nil {
		return a.verbose, err
	}
	pol, err := fv.resolve()
	if err != nil {
		return a.verbose, err
	}
	a.policy, a.server, a.pwaURL = pol, fv.server, fv.pwaURL

	// D15 transparency: announce external contact BEFORE signaling.
	eprintln(a.stderr, pol.Summary(a.server))

	ice, err := a.iceServers(ctx)
	if err != nil {
		return a.verbose, err
	}
	conn, offer, err := peer.Offer(ctx, peer.Config{ICEServers: ice, EnableMDNS: true})
	if err != nil {
		return a.verbose, err
	}
	defer func() { _ = conn.Close() }()

	answer, err := a.senderHandshake(ctx, offer)
	if err != nil {
		return a.verbose, err
	}
	if err := conn.SetRemote(ctx, answer); err != nil {
		return a.verbose, err
	}
	if err := conn.WaitOpen(ctx); err != nil {
		return a.verbose, err
	}
	a.printPath(ctx, conn)

	ev := make(chan transfer.Event, 64)
	drained := make(chan struct{})
	go func() { defer close(drained); a.renderEvents(ev, senderNames(files)) }()
	start := time.Now()
	err = transfer.Send(ctx, conn, files, ev)
	<-drained
	if err != nil {
		return a.verbose, err
	}
	return a.verbose, a.sendSummary(files, time.Since(start))
}

// iceServers builds the ICE server list for the resolved policy: the
// built-in STUN pair, or GET /v1/ice (D6) when the policy fetches — with
// TURN stripped on the --no-relay axis.
func (a *app) iceServers(ctx context.Context) ([]webrtc.ICEServer, error) {
	if !a.policy.FetchICE {
		return BuiltinSTUN(), nil
	}
	w := &signal.Worker{BaseURL: a.server}
	servers, err := w.ICEServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch ICE servers from %s: %w", a.server, err)
	}
	if !a.policy.Relay {
		servers = StripTURN(servers)
	}
	return servers, nil
}

// senderHandshake announces the offer and waits for the answer on the
// policy's signaling axis: worker (token + QR) or paste (blob on stdout).
// The fallback preset escalates paste → worker on any failure before the
// SDP exchange completes (error or the QP/1 120 s timeout), announcing the
// escalation first (D15 transparency).
func (a *app) senderHandshake(ctx context.Context, offer webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	if a.policy.Signal == SignalWorker {
		return a.senderWorker(ctx, offer)
	}
	eprintln(a.stderr, "paste this offer blob to the receiver (chat, email — any channel):")
	p := &signal.Paste{In: a.stdin, Out: a.stdout}
	_, wait, err := p.Announce(ctx, offer) // writes the offer blob line to stdout
	if err != nil {
		return webrtc.SessionDescription{}, err
	}
	eprintln(a.stderr, "paste the receiver's answer blob:")
	waitCtx, cancel := context.WithTimeout(ctx, pasteTimeout)
	answer, err := wait(waitCtx)
	cancel()
	switch {
	case err == nil:
		return answer, nil
	case ctx.Err() != nil: // SIGINT, not the paste timeout
		return webrtc.SessionDescription{}, ctx.Err()
	case a.policy.Escalate:
		eprintln(a.stderr, "offline signaling failed — using Cloudflare signaling server (--externals fallback)")
		return a.senderWorker(ctx, offer)
	case errors.Is(err, context.DeadlineExceeded):
		eprintf(a.stderr, "paste timed out after %s — re-run both sides and paste promptly, or use --externals fallback|full.\n", pasteTimeout)
		return webrtc.SessionDescription{}, fmt.Errorf("%w: no answer pasted within %s", signal.ErrTimeout, pasteTimeout)
	default:
		return webrtc.SessionDescription{}, err
	}
}

// senderWorker announces the offer via SP/1: print the token prominently
// with its terminal QR (M3.1), then poll for the answer (signal.ErrTimeout
// at session expiry → exit 2).
func (a *app) senderWorker(ctx context.Context, offer webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	w := &signal.Worker{BaseURL: a.server}
	tok, wait, err := w.Announce(ctx, offer)
	if err != nil {
		return webrtc.SessionDescription{}, err
	}
	eprintf(a.stderr, "\ntoken: %s\n\n", tok)
	printQR(a.stderr, QRPayload(a.pwaURL, tok))
	eprintf(a.stderr, "\nreceiver runs: jsi receive %s\n", tok)
	eprintln(a.stderr, "waiting for receiver…")
	return wait(ctx)
}

// printPath prints the D15 transparency line for the established path
// (PLAN.md §4: clients MUST show direct / STUN-assisted / TURN-relayed).
func (a *app) printPath(ctx context.Context, conn *peer.Conn) {
	path, err := conn.ConnectionPath(ctx)
	if err != nil {
		eprintf(a.stderr, "connected (path unknown: %v)\n", err)
		return
	}
	eprintf(a.stderr, "connected: %s\n", path)
}

// sendSummary prints the per-file sha256 + duration summary (M3.1).
func (a *app) sendSummary(files []transfer.File, dur time.Duration) error {
	eprintf(a.stderr, "\nsent %d file(s) in %s:\n", len(files), dur.Round(time.Millisecond))
	for _, f := range files {
		sum, err := sha256File(f.Path)
		if err != nil {
			return err
		}
		eprintf(a.stderr, "  %s  %s  sha256:%s\n", f.Name, humanBytes(f.Size), sum)
	}
	return nil
}

// sha256File streams one file through SHA-256 for the send summary (D10 —
// the wire hash is computed inside transfer; this is the sender's local
// cross-check).
func sha256File(path string) (string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	defer func() { _ = fh.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// senderNames maps send-list index (the TP/1 file ID) to display name for
// the progress renderer.
func senderNames(files []transfer.File) map[int]string {
	names := make(map[int]string, len(files))
	for i, f := range files {
		names[i] = f.Name
	}
	return names
}
