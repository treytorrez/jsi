package signal

import (
	"context"

	"github.com/pion/webrtc/v4"
)

// Channel is the signaling seam (PLAN.md §7.3): one offer out, one answer
// back. Implementations: Worker (SP/1 over HTTP, M2), Paste (QP/1 paste
// blob, M3.6), QR (QP/1 animated frames, M4).
type Channel interface {
	// Announce publishes offer and returns the session token plus a wait
	// function. wait resolves with the peer's answer, polling until the
	// answer arrives, its context is canceled, or the session expires
	// (ErrTimeout).
	Announce(ctx context.Context, offer webrtc.SessionDescription) (token string, wait func(context.Context) (webrtc.SessionDescription, error), err error)
	// Join fetches the offer for token and returns it plus a respond
	// function that publishes the peer's answer.
	Join(ctx context.Context, token string) (offer webrtc.SessionDescription, respond func(context.Context, webrtc.SessionDescription) error, err error)
}
