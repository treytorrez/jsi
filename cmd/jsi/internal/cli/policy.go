package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
)

// D15 externals presets (PLAN.md §3 D15, §4 connection policy).
const (
	ExternalsNone     = "none"
	ExternalsFallback = "fallback"
	ExternalsFull     = "full"
)

// Signaling channels (--signal; PLAN.md §7.5 — "qr" arrives in M4).
const (
	SignalPaste  = "paste"
	SignalWorker = "worker"
	SignalQR     = "qr"
)

// pasteTimeout is the QP/1 offline-signaling timeout per direction
// (proto/SIGNALING.md §Sequence): 120 s.
const pasteTimeout = 120 * time.Second

// BuiltinSTUN is the ICE server list of the D15 `none` preset: STUN only,
// zero worker contact. STUN is the one always-permitted external —
// stateless, sees IPs, never content (PLAN.md §4). Returns a fresh slice
// per call.
func BuiltinSTUN() []webrtc.ICEServer {
	return []webrtc.ICEServer{
		{URLs: []string{"stun:stun.cloudflare.com:3478"}},
		{URLs: []string{"stun:stun.l.google.com:19302"}},
	}
}

// RelaySetting is the --relay/--no-relay axis; RelayUnset defers to the
// preset (C1 mixing, PLAN.md §4).
type RelaySetting int

const (
	RelayUnset RelaySetting = iota
	RelayOn
	RelayOff
)

// Policy is the resolved D15 connection policy for one run.
type Policy struct {
	Preset   string // as requested: none | fallback | full
	Signal   string // channel tried first: paste | worker
	Escalate bool   // a paste failure escalates to worker signaling (fallback)
	Relay    bool   // TURN allowed
	FetchICE bool   // fetch GET /v1/ice before peer setup (D6)
}

// ResolvePolicy maps an --externals preset plus per-axis overrides to a
// Policy (PLAN.md §4 connection policy table). Pure; table-tested.
func ResolvePolicy(preset, signalOverride string, relay RelaySetting) (Policy, error) {
	var p Policy
	switch preset {
	case ExternalsNone:
		p = Policy{Preset: preset, Signal: SignalPaste}
	case ExternalsFallback:
		// D15: paste first, escalate to the worker on failure; /v1/ice is
		// fetched UPFRONT so TURN exists if the direct path fails (a fresh
		// fetch after escalation would need a new paste cycle — ICE restart
		// → new SDP exchange).
		p = Policy{Preset: preset, Signal: SignalPaste, Relay: true, FetchICE: true}
	case ExternalsFull:
		p = Policy{Preset: preset, Signal: SignalWorker, Relay: true, FetchICE: true}
	default:
		return Policy{}, fmt.Errorf("invalid --externals %q (want none|fallback|full)", preset)
	}

	switch signalOverride {
	case "":
	case SignalPaste, SignalWorker, SignalQR:
		p.Signal = signalOverride
	default:
		return Policy{}, fmt.Errorf("invalid --signal %q (want paste|qr|worker)", signalOverride)
	}

	switch relay {
	case RelayOn:
		p.Relay = true
	case RelayOff:
		p.Relay = false
	case RelayUnset:
	}

	// Escalation is the fallback preset's defining behavior and applies only
	// while an offline channel (paste or QR) is tried first.
	p.Escalate = preset == ExternalsFallback && p.Signal != SignalWorker

	// TURN credentials must exist before offer creation (D6), so any
	// relay-allowed run fetches /v1/ice. Presets that fetch keep fetching
	// under --no-relay too: their STUN comes from the same response and
	// StripTURN drops the TURN entries.
	p.FetchICE = p.FetchICE || p.Relay

	return p, nil
}

// Summary is the D15 transparency line printed BEFORE any signaling
// (PLAN.md §4: clients MUST announce external contact up front).
func (p Policy) Summary(server string) string {
	switch {
	case p.Preset == ExternalsNone && p.Signal == SignalPaste && !p.Relay:
		return "mode: none — no servers will be contacted (STUN only)"
	case p.Preset == ExternalsFull && p.Signal == SignalWorker && p.Relay:
		return "mode: full — signaling via " + server + " (TURN relay allowed)"
	case p.Escalate:
		return "mode: fallback — offline signaling first, escalates to " + server + " on failure (TURN relay allowed)"
	default:
		// C1 mixed mode: compose from the two axes.
		sig := "offline paste signaling"
		if p.Signal == SignalWorker {
			sig = "signaling via " + server
		}
		transport := "STUN only"
		if p.Relay {
			transport = "STUN + TURN relay"
		}
		return fmt.Sprintf("mode: %s (custom) — %s; %s", p.Preset, sig, transport)
	}
}

// StripTURN implements the --no-relay axis: turn:/turns: URLs are removed
// from a fetched ICE server list, stun: entries are kept, and servers left
// with no URLs are dropped.
func StripTURN(servers []webrtc.ICEServer) []webrtc.ICEServer {
	out := make([]webrtc.ICEServer, 0, len(servers))
	for _, s := range servers {
		urls := make([]string, 0, len(s.URLs))
		for _, u := range s.URLs {
			if strings.HasPrefix(u, "turn:") || strings.HasPrefix(u, "turns:") {
				continue
			}
			urls = append(urls, u)
		}
		if len(urls) == 0 {
			continue
		}
		s.URLs = urls
		out = append(out, s)
	}
	return out
}
