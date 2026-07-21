// Package tui implements the JSI TUI (PLAN.md §7.6, milestone M5): a
// Bubble Tea application wrapping the same internal/peer, internal/signal,
// internal/transfer, and internal/policy stack as the CLI. Signaling exchange
// is driven through the Bubble Tea event loop using the QP/1 codecs directly
// (EncodePayload/DecodePayload/SplitFrames/JoinFrames) rather than the
// signal.Paste/QR channels (which block on stdin/stdout — wrong for an
// interactive TUI). The peer+transfer stack runs in goroutines; status flows
// back via tea.Msg / p.Send.
package tui

import (
	"context"

	"github.com/pion/webrtc/v4"

	"github.com/treyt/jsi/internal/peer"
	"github.com/treyt/jsi/internal/transfer"
)

// DefaultServerURL is the hosted SP/1 worker (PLAN.md §7.1).
const DefaultServerURL = "https://jsi-signal.treytorrez.workers.dev"

// State identifies one TUI screen. The ordering groups send and receive
// phases for readability; transitions are driven by Update.
type State int

const (
	stateHome State = iota
	stateSettings
	stateHelp

	stateSendPick
	stateSendGatherOffer
	stateSendShowOffer
	stateSendConnect
	stateSendTransfer
	stateSendDone
	stateSendError

	stateRecvInput
	stateRecvGatherAnswer
	stateRecvShowAnswer
	stateRecvConnect
	stateRecvTransfer
	stateRecvDone
	stateRecvError
)

func (s State) String() string {
	switch s {
	case stateHome:
		return "home"
	case stateSettings:
		return "settings"
	case stateHelp:
		return "help"
	case stateSendPick:
		return "send:pick"
	case stateSendGatherOffer:
		return "send:gather-offer"
	case stateSendShowOffer:
		return "send:show-offer"
	case stateSendConnect:
		return "send:connect"
	case stateSendTransfer:
		return "send:transfer"
	case stateSendDone:
		return "send:done"
	case stateSendError:
		return "send:error"
	case stateRecvInput:
		return "recv:input"
	case stateRecvGatherAnswer:
		return "recv:gather-answer"
	case stateRecvShowAnswer:
		return "recv:show-answer"
	case stateRecvConnect:
		return "recv:connect"
	case stateRecvTransfer:
		return "recv:transfer"
	case stateRecvDone:
		return "recv:done"
	case stateRecvError:
		return "recv:error"
	default:
		return "unknown"
	}
}

// --- Messages (tea.Msg types) ---

// offerCreatedMsg carries the result of peer.Offer (send flow).
type offerCreatedMsg struct {
	conn  *peer.Conn
	offer webrtc.SessionDescription
	err   error
}

// answerCreatedMsg carries the result of peer.Answer (receive flow).
type answerCreatedMsg struct {
	conn   *peer.Conn
	answer webrtc.SessionDescription
	err    error
}

// announceResultMsg carries the worker's Announce result (send, worker mode):
// the session token plus the wait closure to poll for the answer.
type announceResultMsg struct {
	token string
	wait  func(context.Context) (webrtc.SessionDescription, error)
	err   error
}

// answerReadyMsg carries the answer SDP from a worker long-poll or a decoded
// paste blob.
type answerReadyMsg struct {
	answer webrtc.SessionDescription
	err    error
}

// joinResultMsg carries the worker's Join result (receive, worker mode): the
// offer SDP plus the respond closure to publish the answer.
type joinResultMsg struct {
	offer   webrtc.SessionDescription
	respond func(context.Context, webrtc.SessionDescription) error
	err     error
}

// respondResultMsg carries the result of posting the answer to the worker.
type respondResultMsg struct{ err error }

// connectionOpenMsg carries the established connection path (D15).
type connectionOpenMsg struct {
	path string
	err  error
}

// connectionFailedMsg carries a SetRemote/WaitOpen failure.
type connectionFailedMsg struct{ err error }

// transferEventMsg is one progress event pumped from the transfer goroutine.
type transferEventMsg struct{ ev transfer.Event }

// transferDoneMsg ends a transfer run.
type transferDoneMsg struct{ err error }

// qrTickMsg advances the animated QR frame index.
type qrTickMsg struct{}
