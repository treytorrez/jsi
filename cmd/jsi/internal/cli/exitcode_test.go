package cli

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/treyt/jsi/internal/signal"
	"github.com/treyt/jsi/internal/transfer"
)

// TestExitCode pins the M3.4 exit-code mapping (PLAN.md §7.5).
func TestExitCode(t *testing.T) {
	generic := errors.New("boom")
	tests := []struct {
		name        string
		err         error
		interrupted bool
		want        int
	}{
		{"ok", nil, false, ExitOK},
		{"ok even if a signal raced the exit", nil, true, ExitOK},
		{"generic", generic, false, ExitGeneric},
		{"signaling timeout", signal.ErrTimeout, false, ExitSignalingTimeout},
		{"wrapped signaling timeout", fmt.Errorf("wait: %w", signal.ErrTimeout), false, ExitSignalingTimeout},
		{"peer rejected", transfer.ErrRejected, false, ExitRejected},
		{"wrapped peer rejected", fmt.Errorf("send: %w", transfer.ErrRejected), false, ExitRejected},
		{"receiver declined", transfer.ErrDeclined, false, ExitRejected},
		{"hash mismatch", transfer.ErrHashMismatch, false, ExitIntegrity},
		{"wrapped hash mismatch", fmt.Errorf("file 0: %w", transfer.ErrHashMismatch), false, ExitIntegrity},
		{"SIGINT wins over error shape", context.Canceled, true, ExitInterrupt},
		{"SIGINT during generic failure", generic, true, ExitInterrupt},
		{"bare context.Canceled without SIGINT is generic", context.Canceled, false, ExitGeneric},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExitCode(tt.err, tt.interrupted); got != tt.want {
				t.Errorf("ExitCode(%v, %v): got %d, want %d", tt.err, tt.interrupted, got, tt.want)
			}
		})
	}
}

// TestQRPayload pins the send-side QR payload choice (M3.1).
func TestQRPayload(t *testing.T) {
	tests := []struct {
		name   string
		pwaURL string
		token  string
		want   string
	}{
		{"default: bare token", "", "7KQX2A", "7KQX2A"},
		{"pwa base", "https://jsi.example", "7KQX2A", "https://jsi.example#t=7KQX2A"},
		{"trailing slash trimmed", "https://jsi.example/", "7KQX2A", "https://jsi.example#t=7KQX2A"},
		{"path base kept", "https://example.com/jsi/", "ABCDEF", "https://example.com/jsi#t=ABCDEF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := QRPayload(tt.pwaURL, tt.token); got != tt.want {
				t.Errorf("QRPayload(%q, %q): got %q, want %q", tt.pwaURL, tt.token, got, tt.want)
			}
		})
	}
}
