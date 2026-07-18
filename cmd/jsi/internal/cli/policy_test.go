package cli

import (
	"strings"
	"testing"

	"github.com/pion/webrtc/v4"
)

// TestResolvePolicyMatrix covers the D15 connection policy table (PLAN.md
// §4): all three presets plus the C1 per-axis overrides.
func TestResolvePolicyMatrix(t *testing.T) {
	tests := []struct {
		name    string
		preset  string
		signal  string
		relay   RelaySetting
		want    Policy
		wantErr bool
	}{
		{"none defaults", ExternalsNone, "", RelayUnset,
			Policy{Preset: "none", Signal: SignalPaste}, false},
		{"fallback defaults", ExternalsFallback, "", RelayUnset,
			Policy{Preset: "fallback", Signal: SignalPaste, Escalate: true, Relay: true, FetchICE: true}, false},
		{"full defaults", ExternalsFull, "", RelayUnset,
			Policy{Preset: "full", Signal: SignalWorker, Relay: true, FetchICE: true}, false},

		// C1 signaling overrides.
		{"none + worker signal", ExternalsNone, SignalWorker, RelayUnset,
			Policy{Preset: "none", Signal: SignalWorker}, false},
		{"full + paste signal", ExternalsFull, SignalPaste, RelayUnset,
			Policy{Preset: "full", Signal: SignalPaste, Relay: true, FetchICE: true}, false},
		{"fallback + worker signal drops escalation", ExternalsFallback, SignalWorker, RelayUnset,
			Policy{Preset: "fallback", Signal: SignalWorker, Relay: true, FetchICE: true}, false},

		// C1 relay overrides.
		{"none + relay opts into TURN and the upfront ICE fetch", ExternalsNone, "", RelayOn,
			Policy{Preset: "none", Signal: SignalPaste, Relay: true, FetchICE: true}, false},
		{"full + no-relay keeps the ICE fetch (STUN from the same response)", ExternalsFull, "", RelayOff,
			Policy{Preset: "full", Signal: SignalWorker, FetchICE: true}, false},
		{"fallback + no-relay still fetches upfront", ExternalsFallback, "", RelayOff,
			Policy{Preset: "fallback", Signal: SignalPaste, Escalate: true, FetchICE: true}, false},

		// Both axes at once.
		{"none + worker + relay", ExternalsNone, SignalWorker, RelayOn,
			Policy{Preset: "none", Signal: SignalWorker, Relay: true, FetchICE: true}, false},

		// Errors.
		{"invalid preset", "sometimes", "", RelayUnset, Policy{}, true},
		{"invalid signal", ExternalsNone, "pigeon", RelayUnset, Policy{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolvePolicy(tt.preset, tt.signal, tt.relay)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ResolvePolicy(%q, %q, %d): got %+v, want error", tt.preset, tt.signal, tt.relay, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolvePolicy(%q, %q, %d): %v", tt.preset, tt.signal, tt.relay, err)
			}
			if got != tt.want {
				t.Errorf("ResolvePolicy(%q, %q, %d): got %+v, want %+v", tt.preset, tt.signal, tt.relay, got, tt.want)
			}
		})
	}
}

// TestPolicySummary pins the D15 transparency lines printed before
// signaling (PLAN.md §4).
func TestPolicySummary(t *testing.T) {
	const server = "https://jsi-signal.example.workers.dev"
	tests := []struct {
		name string
		pol  Policy
		want string
	}{
		{"none", Policy{Preset: "none", Signal: SignalPaste},
			"mode: none — no servers will be contacted (STUN only)"},
		{"full", Policy{Preset: "full", Signal: SignalWorker, Relay: true, FetchICE: true},
			"mode: full — signaling via https://jsi-signal.example.workers.dev (TURN relay allowed)"},
		{"fallback", Policy{Preset: "fallback", Signal: SignalPaste, Escalate: true, Relay: true, FetchICE: true},
			"mode: fallback — offline signaling first, escalates to https://jsi-signal.example.workers.dev on failure (TURN relay allowed)"},
		{"custom: worker without relay", Policy{Preset: "none", Signal: SignalWorker},
			"mode: none (custom) — signaling via https://jsi-signal.example.workers.dev; STUN only"},
		{"custom: paste with relay", Policy{Preset: "none", Signal: SignalPaste, Relay: true, FetchICE: true},
			"mode: none (custom) — offline paste signaling; STUN + TURN relay"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.pol.Summary(server); got != tt.want {
				t.Errorf("Summary: got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestStripTURN covers the --no-relay axis against the SP/1 §/v1/ice
// response shape (proto/SIGNALING.md).
func TestStripTURN(t *testing.T) {
	// The worker's documented response: two STUN entries plus one TURN
	// entry carrying several turn:/turns: URLs.
	fetched := []webrtc.ICEServer{
		{URLs: []string{"stun:stun.cloudflare.com:3478"}},
		{URLs: []string{"stun:stun.l.google.com:19302"}},
		{URLs: []string{
			"turn:turn.cloudflare.com:3478?transport=udp",
			"turns:turn.cloudflare.com:5349?transport=tcp",
			"turn:turn.cloudflare.com:53?transport=udp",
		}, Username: "u", Credential: "c"},
	}
	got := StripTURN(fetched)
	if len(got) != 2 {
		t.Fatalf("StripTURN kept %d servers, want 2: %+v", len(got), got)
	}
	for _, s := range got {
		for _, u := range s.URLs {
			if strings.HasPrefix(u, "turn:") || strings.HasPrefix(u, "turns:") {
				t.Errorf("StripTURN left TURN URL %q", u)
			}
		}
	}
	// The credential-bearing TURN entry was dropped wholesale, so no
	// credentials survive anywhere.
	for _, s := range got {
		if s.Username != "" || s.Credential != nil {
			t.Errorf("StripTURN kept credentials on %+v", s)
		}
	}

	// A mixed-URL server keeps only its non-TURN URLs.
	mixed := StripTURN([]webrtc.ICEServer{
		{URLs: []string{"stun:example.com:3478", "turn:example.com:3478"}, Username: "u"},
	})
	if len(mixed) != 1 || len(mixed[0].URLs) != 1 || mixed[0].URLs[0] != "stun:example.com:3478" {
		t.Errorf("StripTURN mixed: got %+v", mixed)
	}

	if got := StripTURN(nil); len(got) != 0 {
		t.Errorf("StripTURN(nil): got %+v", got)
	}
}
