package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pion/webrtc/v4"

	"github.com/treyt/jsi/internal/peer"
	"github.com/treyt/jsi/internal/signal"
	"github.com/treyt/jsi/internal/transfer"
)

// qrFrameInterval is the animated QR frame rate (2 fps per QP/1).
const qrFrameInterval = 500 * time.Millisecond

// announceCmd calls Worker.Announce in a goroutine.
func announceCmd(ctx context.Context, w *signal.Worker, offer webrtc.SessionDescription) tea.Cmd {
	return func() tea.Msg {
		token, wait, err := w.Announce(ctx, offer)
		return announceResultMsg{token: token, wait: wait, err: err}
	}
}

// waitForAnswerCmd calls the Announce wait closure in a goroutine.
func waitForAnswerCmd(ctx context.Context, wait func(context.Context) (webrtc.SessionDescription, error)) tea.Cmd {
	return func() tea.Msg {
		answer, err := wait(ctx)
		return answerReadyMsg{answer: answer, err: err}
	}
}

// joinCmd calls Worker.Join in a goroutine.
func joinCmd(ctx context.Context, w *signal.Worker, token string) tea.Cmd {
	return func() tea.Msg {
		offer, respond, err := w.Join(ctx, token)
		return joinResultMsg{offer: offer, respond: respond, err: err}
	}
}

// respondCmd calls the Join respond closure in a goroutine.
func respondCmd(ctx context.Context, respond func(context.Context, webrtc.SessionDescription) error, answer webrtc.SessionDescription) tea.Cmd {
	return func() tea.Msg {
		err := respond(ctx, answer)
		return respondResultMsg{err: err}
	}
}

// connectCmd applies the remote answer and waits for the channel to open,
// then reads the D15 connection path. One round-trip so the TUI sees a single
// "connecting" spinner phase.
func connectCmd(ctx context.Context, conn *peer.Conn, answer webrtc.SessionDescription) tea.Cmd {
	return func() tea.Msg {
		if err := conn.SetRemote(ctx, answer); err != nil {
			return connectionFailedMsg{err: err}
		}
		if err := conn.WaitOpen(ctx); err != nil {
			return connectionFailedMsg{err: err}
		}
		path, perr := conn.ConnectionPath(ctx)
		return connectionOpenMsg{path: path, err: perr}
	}
}

// startSendCmd runs transfer.Send in a goroutine, pumping events back via
// program.Send. The returned msg is transferDoneMsg (terminal).
func startSendCmd(ctx context.Context, p *tea.Program, conn *peer.Conn, files []transfer.File) tea.Cmd {
	return func() tea.Msg {
		ev := make(chan transfer.Event, 64)
		go func() {
			for e := range ev {
				p.Send(transferEventMsg{ev: e})
			}
		}()
		err := transfer.Send(ctx, conn, files, ev)
		return transferDoneMsg{err: err}
	}
}

// startReceiveCmd runs transfer.Receive in a goroutine, pumping events back
// via program.Send.
func startReceiveCmd(ctx context.Context, p *tea.Program, conn *peer.Conn, destDir string) tea.Cmd {
	return func() tea.Msg {
		ev := make(chan transfer.Event, 64)
		go func() {
			for e := range ev {
				p.Send(transferEventMsg{ev: e})
			}
		}()
		err := transfer.Receive(ctx, conn, destDir, ev)
		return transferDoneMsg{err: err}
	}
}

// qrTickCmd schedules the next QR frame advance at 2 fps.
func qrTickCmd() tea.Cmd {
	return tea.Tick(qrFrameInterval, func(time.Time) tea.Msg {
		return qrTickMsg{}
	})
}
