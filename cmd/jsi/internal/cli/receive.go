package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/treyt/jsi/internal/peer"
	"github.com/treyt/jsi/internal/protocol"
	"github.com/treyt/jsi/internal/signal"
	"github.com/treyt/jsi/internal/transfer"
)

// receive runs `jsi receive [token]` (M3.2): resolve the D15 policy →
// obtain the offer (worker token or pasted blob, with fallback escalation)
// → answer → connect → TP/1 receive into -o dir with an accept prompt and
// progress bars.
func (a *app) receive(ctx context.Context, args []string) (bool, error) {
	fs, fv := newFlagSet("receive", a.stderr)
	var outDir string
	fs.StringVar(&outDir, "o", ".", "destination directory")
	pos, err := parseFlags(fs, args)
	a.verbose = fv.verbose
	if err != nil {
		if errors.Is(err, errHelp) {
			usage(a.stdout)
			return a.verbose, nil
		}
		return a.verbose, err
	}
	if len(pos) > 1 {
		return a.verbose, fmt.Errorf("receive takes at most one [token] argument, got %d", len(pos))
	}
	pol, err := fv.resolve()
	if err != nil {
		return a.verbose, err
	}
	a.policy, a.server, a.yes = pol, fv.server, fv.yes
	a.noSTUN = fv.noSTUN

	tok := ""
	if len(pos) == 1 {
		tok = pos[0]
	}
	switch {
	case pol.Signal == SignalWorker && tok == "":
		return a.verbose, errors.New("worker signaling needs the sender's token: jsi receive <token>")
	case pol.Signal == SignalPaste && tok != "":
		return a.verbose, errors.New("a token is only used with worker signaling (--signal worker or --externals full)")
	}

	// D15 transparency: announce external contact BEFORE signaling.
	eprintln(a.stderr, pol.Summary(a.server))

	ice, err := a.iceServers(ctx)
	if err != nil {
		return a.verbose, err
	}
	offer, respond, err := a.receiverOffer(ctx, tok)
	if err != nil {
		return a.verbose, err
	}
	conn, answer, err := peer.Answer(ctx, peer.Config{ICEServers: ice, EnableMDNS: !fv.noMDNS}, offer)
	if err != nil {
		return a.verbose, err
	}
	defer func() { _ = conn.Close() }()
	respondCtx := ctx
	var stopDisplay context.CancelFunc
	if pol.Signal == SignalQR {
		// The answer frames animate until the channel opens — then the
		// display stops so it can't fight the progress renderer (stderr).
		respondCtx, stopDisplay = context.WithCancel(ctx)
	}
	if err := respond(respondCtx, answer); err != nil {
		if stopDisplay != nil {
			stopDisplay()
		}
		return a.verbose, err
	}
	if pol.Signal == SignalPaste {
		eprintln(a.stderr, "send the answer blob back to the sender; connecting…")
	}
	if pol.Signal == SignalQR {
		eprintln(a.stderr, "answer shown as QR (blob also on stdout); connecting…")
	}
	if err := conn.WaitOpen(ctx); err != nil {
		if stopDisplay != nil {
			stopDisplay()
		}
		return a.verbose, err
	}
	if stopDisplay != nil {
		stopDisplay()
	}
	a.printPath(ctx, conn)

	names := map[int]string{}
	ev := make(chan transfer.Event, 64)
	drained := make(chan struct{})
	go func() { defer close(drained); a.renderEvents(ev, names) }()
	start := time.Now()
	err = transfer.Receive(ctx, conn, outDir, ev, transfer.WithDecide(a.makeDecide(names)))
	<-drained
	if err != nil {
		return a.verbose, err
	}
	eprintf(a.stderr, "\nreceived into %s in %s\n", outDir, time.Since(start).Round(time.Millisecond))
	return a.verbose, nil
}

// receiverOffer obtains the sender's offer on the policy's signaling axis:
// worker (token arg → SP/1 Join) or paste (blob from stdin). The fallback
// preset escalates paste → worker on any failure before the SDP exchange
// completes, asking for the sender's token interactively after announcing
// the escalation (D15 transparency).
func (a *app) receiverOffer(ctx context.Context, tok string) (webrtc.SessionDescription, func(context.Context, webrtc.SessionDescription) error, error) {
	if a.policy.Signal == SignalWorker {
		return a.receiverWorker(ctx, tok)
	}
	var offer webrtc.SessionDescription
	var respond func(context.Context, webrtc.SessionDescription) error
	var err error
	joinCtx, cancel := context.WithTimeout(ctx, pasteTimeout)
	if a.policy.Signal == SignalQR {
		eprintln(a.stderr, "paste the sender's offer blob (or scan their QR with the JSI web app):")
		q := &signal.QR{Out: a.stderr, In: a.stdin, BlobOut: a.stdout}
		offer, respond, err = q.Join(joinCtx, "")
	} else {
		eprintln(a.stderr, "paste the sender's offer blob:")
		p := &signal.Paste{In: a.stdin, Out: a.stdout}
		offer, respond, err = p.Join(joinCtx, "")
	}
	cancel()
	switch {
	case err == nil:
		return offer, respond, nil
	case ctx.Err() != nil: // SIGINT, not the paste timeout
		return webrtc.SessionDescription{}, nil, ctx.Err()
	case a.policy.Escalate:
		eprintln(a.stderr, "offline signaling failed — using Cloudflare signaling server (--externals fallback)")
		eprintln(a.stderr, "enter the sender's token:")
		line, rerr := a.readLine()
		if rerr != nil {
			return webrtc.SessionDescription{}, nil, fmt.Errorf("read token: %w", rerr)
		}
		return a.receiverWorker(ctx, line)
	case errors.Is(err, context.DeadlineExceeded):
		eprintf(a.stderr, "paste timed out after %s — re-run both sides and paste promptly, or use --externals fallback|full.\n", pasteTimeout)
		return webrtc.SessionDescription{}, nil, fmt.Errorf("%w: no offer pasted within %s", signal.ErrTimeout, pasteTimeout)
	default:
		return webrtc.SessionDescription{}, nil, err
	}
}

// receiverWorker joins the SP/1 session identified by tok (token.Normalize
// runs inside Worker.Join — case-insensitive input per SP/1).
func (a *app) receiverWorker(ctx context.Context, tok string) (webrtc.SessionDescription, func(context.Context, webrtc.SessionDescription) error, error) {
	w := &signal.Worker{BaseURL: a.server}
	offer, respond, err := w.Join(ctx, tok)
	if err != nil {
		return webrtc.SessionDescription{}, nil, err
	}
	eprintln(a.stderr, "session found; answering…")
	return offer, respond, nil
}

// makeDecide builds the transfer.WithDecide callback (M3.2): it lists the
// offered files and, unless --yes, prompts "accept? [Y/n]" — accept-all or
// reject-all only. Manifest names are recorded for the progress renderer.
func (a *app) makeDecide(names map[int]string) func([]protocol.FileMeta) ([]int, error) {
	return func(fms []protocol.FileMeta) ([]int, error) {
		var total int64
		eprintf(a.stderr, "sender offers %d file(s):\n", len(fms))
		for _, fm := range fms {
			names[fm.ID] = fm.Name
			total += fm.Size
			eprintf(a.stderr, "  %s (%s)\n", fm.Name, humanBytes(fm.Size))
		}
		ids := make([]int, len(fms))
		for i, fm := range fms {
			ids[i] = fm.ID
		}
		if a.yes {
			return ids, nil
		}
		eprintf(a.stderr, "accept %s total? [Y/n] ", humanBytes(total))
		line, err := a.readLine()
		if err != nil {
			return nil, fmt.Errorf("read accept prompt: %w", err)
		}
		switch strings.ToLower(line) {
		case "", "y", "yes":
			return ids, nil
		default:
			return nil, nil // empty acceptance → Reject + ErrDeclined (exit 3)
		}
	}
}
