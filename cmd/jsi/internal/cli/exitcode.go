package cli

import (
	"context"
	"errors"

	"github.com/treyt/jsi/internal/signal"
	"github.com/treyt/jsi/internal/transfer"
)

// Process exit codes (M3.4, PLAN.md §7.5).
const (
	ExitOK               = 0
	ExitGeneric          = 1
	ExitSignalingTimeout = 2
	ExitRejected         = 3
	ExitIntegrity        = 4
	ExitInterrupt        = 130
)

// ExitCode maps a run error to the process exit code. interrupted reports
// that the root context was canceled by SIGINT; it wins over the error
// shape (a Ctrl-C mid-handshake surfaces as context.Canceled).
func ExitCode(err error, interrupted bool) int {
	if err == nil {
		return ExitOK
	}
	if interrupted {
		return ExitInterrupt
	}
	switch {
	case errors.Is(err, signal.ErrTimeout):
		return ExitSignalingTimeout
	case errors.Is(err, transfer.ErrRejected), errors.Is(err, transfer.ErrDeclined):
		return ExitRejected
	case errors.Is(err, transfer.ErrHashMismatch):
		return ExitIntegrity
	default:
		return ExitGeneric
	}
}

// friendlyMessage maps common failures to a one-line explanation (M3.4).
// "" means the raw error is already the best message.
func friendlyMessage(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "interrupted"
	case errors.Is(err, signal.ErrTimeout):
		return "signaling timed out — no answer from the peer"
	case errors.Is(err, transfer.ErrRejected):
		return "the receiver rejected the transfer"
	case errors.Is(err, transfer.ErrDeclined):
		return "transfer declined"
	case errors.Is(err, transfer.ErrHashMismatch):
		return "integrity check failed (sha-256 mismatch)"
	case errors.Is(err, transfer.ErrPeerCancel):
		return "canceled by the peer"
	case errors.Is(err, transfer.ErrPeerClosed):
		return "the peer closed the connection"
	default:
		return ""
	}
}
