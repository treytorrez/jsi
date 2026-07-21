package tui

import (
	"errors"
	"testing"

	"github.com/treyt/jsi/internal/policy"
	"github.com/treyt/jsi/internal/signal"
	"github.com/treyt/jsi/internal/transfer"
)

func TestNewModelDefaults(t *testing.T) {
	m := NewModel()
	if m.state != stateHome {
		t.Errorf("state: got %s, want home", m.state)
	}
	if m.settings.settings.Externals != policy.ExternalsNone {
		t.Errorf("externals: got %q, want none", m.settings.settings.Externals)
	}
}

func TestExitCodeMapping(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		interrupted bool
		want        int
	}{
		{"ok", nil, false, ExitOK},
		{"interrupted", nil, true, ExitInterrupt},
		{"signaling timeout", signal.ErrTimeout, false, ExitSignalingTimeout},
		{"rejected", transfer.ErrRejected, false, ExitRejected},
		{"declined", transfer.ErrDeclined, false, ExitRejected},
		{"hash mismatch", transfer.ErrHashMismatch, false, ExitIntegrity},
		{"generic", errors.New("something broke"), false, ExitGeneric},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExitCode(tt.err, tt.interrupted)
			if got != tt.want {
				t.Errorf("ExitCode(%v, %v): got %d, want %d", tt.err, tt.interrupted, got, tt.want)
			}
		})
	}
}

func TestProgressBar(t *testing.T) {
	tests := []struct {
		pct   int
		width int
		want  string
	}{
		{0, 5, "[░░░░░]"},
		{50, 10, "[█████░░░░░]"},
		{100, 4, "[████]"},
	}
	for _, tt := range tests {
		got := progressBar(tt.pct, tt.width)
		if got != tt.want {
			t.Errorf("progressBar(%d, %d): got %q, want %q", tt.pct, tt.width, got, tt.want)
		}
	}
}
