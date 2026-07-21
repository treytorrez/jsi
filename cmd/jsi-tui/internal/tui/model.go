package tui

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/pion/webrtc/v4"

	"github.com/treyt/jsi/internal/peer"
	"github.com/treyt/jsi/internal/policy"
	"github.com/treyt/jsi/internal/qr"
	"github.com/treyt/jsi/internal/signal"
	"github.com/treyt/jsi/internal/transfer"
)

// Model is the JSI TUI's Bubble Tea model (PLAN.md §7.6).
type Model struct {
	state    State
	theme    Theme
	settings settingsModel

	ctx     context.Context
	cancel  context.CancelFunc
	program *tea.Program

	width  int
	height int

	// Navigation
	cursor int

	// Text input (reused per state: file path, blob, token)
	input textinput.Model

	// Send flow
	files       []transfer.File
	conn        *peer.Conn
	offer       webrtc.SessionDescription
	answer      webrtc.SessionDescription
	displayText string // blob/token/QR shown to the user
	token       string
	wait        func(context.Context) (webrtc.SessionDescription, error)
	respond     func(context.Context, webrtc.SessionDescription) error

	// QR animation
	qrFrames [][]byte
	qrIndex  int

	// Transfer
	progress    map[int]int64 // file ID → bytes done
	progressMax map[int]int64 // file ID → total
	transferErr error
	connPath    string
	startTime   time.Time

	// Exit
	lastErr  error
	quitCode int
	quitting bool
}

// NewModel creates the initial TUI model.
func NewModel() Model {
	ctx, cancel := context.WithCancel(context.Background())
	ti := textinput.New()
	ti.CharLimit = 0
	ti.Width = 80
	return Model{
		state:       stateHome,
		theme:       NewTheme(),
		settings:    newSettingsModel(DefaultSettings()),
		ctx:         ctx,
		cancel:      cancel,
		input:       ti,
		progress:    make(map[int]int64),
		progressMax: make(map[int]int64),
	}
}

// SetProgram lets the Model pump transfer events via p.Send.
func (m *Model) SetProgram(p *tea.Program) { m.program = p }

// ExitCode returns the process exit code for main().
func (m Model) ExitCode() int {
	if m.quitCode != 0 {
		return m.quitCode
	}
	return ExitCode(m.lastErr, m.lastErr != nil && m.state == stateHome)
}

// Init starts the Bubble Tea loop.
func (m Model) Init() tea.Cmd { return nil }

// Update handles all messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = msg.Width - 4
		return m, nil

	case tea.KeyMsg:
		// Global: Ctrl+C always quits.
		if msg.Type == tea.KeyCtrlC {
			m.cancel()
			m.quitting = true
			return m, tea.Quit
		}
		return m.updateKey(msg)

	// Async results
	case offerCreatedMsg:
		if msg.err != nil {
			m.lastErr = msg.err
			m.state = stateSendError
			return m, nil
		}
		m.conn = msg.conn
		m.offer = msg.offer
		return m.onOfferCreated()

	case answerCreatedMsg:
		if msg.err != nil {
			m.lastErr = msg.err
			m.state = stateRecvError
			return m, nil
		}
		m.conn = msg.conn
		m.answer = msg.answer
		return m.onAnswerCreated()

	case announceResultMsg:
		if msg.err != nil {
			m.lastErr = msg.err
			m.state = stateSendError
			return m, nil
		}
		m.token = msg.token
		m.wait = msg.wait
		m.displayText = msg.token
		m.state = stateSendShowOffer
		return m, waitForAnswerCmd(m.ctx, msg.wait)

	case answerReadyMsg:
		if msg.err != nil {
			m.lastErr = msg.err
			m.state = stateSendError
			return m, nil
		}
		m.answer = msg.answer
		m.state = stateSendConnect
		return m, connectCmd(m.ctx, m.conn, m.answer)

	case joinResultMsg:
		if msg.err != nil {
			m.lastErr = msg.err
			m.state = stateRecvError
			return m, nil
		}
		m.respond = msg.respond
		return m, startAnswerCmd(m.ctx, m.settings.settings, msg.offer)

	case respondResultMsg:
		if msg.err != nil {
			m.lastErr = msg.err
			m.state = stateRecvError
			return m, nil
		}
		m.state = stateRecvConnect
		return m, waitOpenCmd(m.ctx, m.conn)

	case connectionOpenMsg:
		if msg.err != nil {
			m.lastErr = msg.err
			if m.state == stateSendConnect {
				m.state = stateSendError
			} else {
				m.state = stateRecvError
			}
			return m, nil
		}
		m.connPath = msg.path
		m.startTime = time.Now()
		if m.state == stateSendConnect {
			m.state = stateSendTransfer
			return m, startSendCmd(m.ctx, m.program, m.conn, m.files)
		}
		m.state = stateRecvTransfer
		return m, startReceiveCmd(m.ctx, m.program, m.conn, ".")

	case connectionFailedMsg:
		m.lastErr = msg.err
		if m.state == stateSendConnect {
			m.state = stateSendError
		} else {
			m.state = stateRecvError
		}
		return m, nil

	case transferEventMsg:
		return m.onTransferEvent(msg.ev), nil

	case transferDoneMsg:
		m.transferErr = msg.err
		if m.state == stateSendTransfer {
			m.state = stateSendDone
		} else {
			m.state = stateRecvDone
		}
		if msg.err != nil {
			m.lastErr = msg.err
		}
		return m, nil

	case qrTickMsg:
		if len(m.qrFrames) > 0 {
			m.qrIndex = (m.qrIndex + 1) % len(m.qrFrames)
		}
		return m, qrTickCmd()
	}

	return m, nil
}

// updateKey handles state-specific key events.
func (m Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// q to quit from home/done/error
	if msg.Type == tea.KeyRunes && string(msg.Runes) == "q" {
		switch m.state {
		case stateHome, stateSendDone, stateRecvDone, stateSendError, stateRecvError, stateHelp:
			m.cancel()
			m.quitting = true
			return m, tea.Quit
		}
	}

	// esc returns to home from sub-screens
	if msg.Type == tea.KeyEsc {
		switch m.state {
		case stateSettings, stateHelp:
			m.state = stateHome
			return m, nil
		case stateSendPick, stateSendShowOffer, stateRecvInput, stateRecvShowAnswer:
			if m.conn != nil {
				_ = m.conn.Close()
			}
			m.state = stateHome
			return m, nil
		}
	}

	switch m.state {
	case stateHome:
		return m.updateHome(msg)
	case stateSettings:
		var cmd tea.Cmd
		m.settings, cmd = m.settings.update(msg)
		return m, cmd
	case stateSendPick:
		return m.updateSendPick(msg)
	case stateSendShowOffer:
		return m.updateSendShowOffer(msg)
	case stateRecvInput:
		return m.updateRecvInput(msg)
	case stateRecvShowAnswer:
		return m.updateRecvShowAnswer(msg)
	}
	return m, nil
}

func (m Model) updateHome(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	items := 4 // Send, Receive, Settings, Help
	switch msg.Type {
	case tea.KeyUp:
		if m.cursor == 0 {
			m.cursor = items - 1
		} else {
			m.cursor--
		}
	case tea.KeyDown:
		m.cursor = (m.cursor + 1) % items
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "j":
			m.cursor = (m.cursor + 1) % items
		case "k":
			if m.cursor == 0 {
				m.cursor = items - 1
			} else {
				m.cursor--
			}
		}
	case tea.KeyEnter:
		switch m.cursor {
		case 0: // Send
			m.state = stateSendPick
			m.input.Reset()
			m.input.Placeholder = "file path to send"
			m.input.Focus()
			return m, textinput.Blink
		case 1: // Receive
			m.state = stateRecvInput
			m.input.Reset()
			m.input.Placeholder = "paste the sender's offer blob (or token for worker mode)"
			m.input.Focus()
			return m, textinput.Blink
		case 2: // Settings
			m.state = stateSettings
		case 3: // Help
			m.state = stateHelp
		}
	}
	return m, nil
}

func (m Model) updateSendPick(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		path := strings.TrimSpace(m.input.Value())
		if path == "" {
			return m, nil
		}
		files, err := transfer.FilesFromPaths([]string{path})
		if err != nil {
			m.lastErr = err
			m.state = stateSendError
			return m, nil
		}
		m.files = files
		m.state = stateSendGatherOffer
		return m, startSendOfferCmd(m.ctx, m.settings.settings)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) onOfferCreated() (tea.Model, tea.Cmd) {
	pol, _ := m.settings.settings.Resolve()
	if pol.Signal == policy.SignalWorker {
		w := &signal.Worker{BaseURL: m.settings.settings.Server}
		m.state = stateSendShowOffer
		return m, announceCmd(m.ctx, w, m.offer)
	}
	// Paste or QR: encode the offer blob.
	blob, err := signal.EncodePayload(m.offer)
	if err != nil {
		m.lastErr = err
		m.state = stateSendError
		return m, nil
	}
	m.displayText = blob
	if pol.Signal == policy.SignalQR {
		m.qrFrames, _ = signal.SplitFrames(m.offer, 0)
		m.qrIndex = 0
		m.state = stateSendShowOffer
		m.input.Reset()
		m.input.Placeholder = "paste the receiver's answer blob"
		m.input.Focus()
		return m, qrTickCmd()
	}
	m.state = stateSendShowOffer
	m.input.Reset()
	m.input.Placeholder = "paste the receiver's answer blob"
	m.input.Focus()
	return m, textinput.Blink
}

func (m Model) updateSendShowOffer(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		if m.wait != nil {
			return m, nil // worker mode: answer comes via answerReadyMsg
		}
		blob := strings.TrimSpace(m.input.Value())
		if blob == "" {
			return m, nil
		}
		answer, err := signal.DecodePayload(blob)
		if err != nil {
			m.lastErr = err
			return m, nil
		}
		m.answer = answer
		m.state = stateSendConnect
		return m, connectCmd(m.ctx, m.conn, m.answer)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) updateRecvInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		val := strings.TrimSpace(m.input.Value())
		if val == "" {
			return m, nil
		}
		pol, _ := m.settings.settings.Resolve()
		if pol.Signal == policy.SignalWorker {
			// Worker mode: val is the token.
			m.state = stateRecvGatherAnswer
			w := &signal.Worker{BaseURL: m.settings.settings.Server}
			return m, joinCmd(m.ctx, w, val)
		}
		// Paste mode: val is the offer blob.
		offer, err := signal.DecodePayload(val)
		if err != nil {
			m.lastErr = err
			m.state = stateRecvError
			return m, nil
		}
		m.offer = offer
		m.state = stateRecvGatherAnswer
		return m, startAnswerCmd(m.ctx, m.settings.settings, m.offer)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) onAnswerCreated() (tea.Model, tea.Cmd) {
	pol, _ := m.settings.settings.Resolve()
	if pol.Signal == policy.SignalWorker {
		// Worker mode: post the answer via respond.
		return m, respondCmd(m.ctx, m.respond, m.answer)
	}
	// Paste/QR: display the answer blob.
	blob, err := signal.EncodePayload(m.answer)
	if err != nil {
		m.lastErr = err
		m.state = stateRecvError
		return m, nil
	}
	m.displayText = blob
	if pol.Signal == policy.SignalQR {
		m.qrFrames, _ = signal.SplitFrames(m.answer, 0)
		m.qrIndex = 0
		m.state = stateRecvShowAnswer
		return m, qrTickCmd()
	}
	m.state = stateRecvShowAnswer
	return m, nil
}

func (m Model) updateRecvShowAnswer(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		m.state = stateRecvConnect
		return m, waitOpenCmd(m.ctx, m.conn)
	}
	return m, nil
}

func (m Model) onTransferEvent(ev transfer.Event) tea.Model {
	switch ev.Kind {
	case transfer.EventFileStart:
		m.progressMax[ev.FileID] = ev.BytesTotal
	case transfer.EventProgress:
		m.progress[ev.FileID] = ev.BytesDone
	case transfer.EventFileDone:
		m.progress[ev.FileID] = ev.BytesTotal
	}
	return m
}

// --- View ---

func (m Model) View() string {
	switch m.state {
	case stateHome:
		return m.viewHome()
	case stateSettings:
		return m.settings.view(m.width)
	case stateHelp:
		return m.viewHelp()
	case stateSendPick:
		return m.viewSendPick()
	case stateSendGatherOffer:
		return m.viewSpinner("creating offer", "gathering ICE candidates")
	case stateSendShowOffer:
		return m.viewSendShowOffer()
	case stateSendConnect:
		return m.viewSpinner("connecting", "waiting for the data channel to open")
	case stateSendTransfer:
		return m.viewTransfer("sending")
	case stateSendDone:
		return m.viewDone("sent")
	case stateSendError:
		return m.viewError()
	case stateRecvInput:
		return m.viewRecvInput()
	case stateRecvGatherAnswer:
		return m.viewSpinner("creating answer", "gathering ICE candidates")
	case stateRecvShowAnswer:
		return m.viewRecvShowAnswer()
	case stateRecvConnect:
		return m.viewSpinner("connecting", "waiting for the data channel to open")
	case stateRecvTransfer:
		return m.viewTransfer("receiving")
	case stateRecvDone:
		return m.viewDone("received")
	case stateRecvError:
		return m.viewError()
	}
	return ""
}

func (m Model) viewHome() string {
	var b strings.Builder
	b.WriteString(m.theme.Title.Render("JSI — Just Send It"))
	b.WriteString("\n\n")
	items := []string{"Send file", "Receive file", "Settings", "Help"}
	for i, item := range items {
		cursor := "  "
		style := m.theme.Normal
		if i == m.cursor {
			cursor = "▶ "
			style = m.theme.Selected
		}
		b.WriteString(style.Render(cursor + item))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(m.theme.Help.Render("j/k navigate · enter select · q quit"))
	b.WriteString("\n\n")
	b.WriteString(m.theme.Muted.Render(m.settings.settings.Summary()))
	return b.String()
}

func (m Model) viewHelp() string {
	var b strings.Builder
	b.WriteString(m.theme.Title.Render("JSI — Help"))
	b.WriteString("\n\n")
	binds := []struct{ key, action string }{
		{"j/k or ↑/↓", "navigate"},
		{"enter", "select / confirm"},
		{"esc", "back"},
		{"q", "quit (from home/done/error)"},
		{"Ctrl+C", "force quit"},
	}
	for _, bnd := range binds {
		fmt.Fprintf(&b, "  %-16s %s\n", bnd.key, bnd.action)
	}
	b.WriteString("\n")
	b.WriteString(m.theme.Help.Render("esc to go back"))
	return b.String()
}

func (m Model) viewSendPick() string {
	var b strings.Builder
	b.WriteString(m.theme.Title.Render("Send — enter a file path"))
	b.WriteString("\n\n")
	b.WriteString("  ")
	b.WriteString(m.input.View())
	b.WriteString("\n\n")
	b.WriteString(m.theme.Help.Render("enter to proceed · esc to cancel"))
	return b.String()
}

func (m Model) viewSendShowOffer() string {
	var b strings.Builder
	pol, _ := m.settings.settings.Resolve()
	if m.token != "" {
		b.WriteString(m.theme.Title.Render("Send — give the receiver this token"))
		b.WriteString("\n\n")
		b.WriteString(m.theme.Token.Render(m.token))
		b.WriteString("\n\n")
		b.WriteString(m.theme.Help.Render("waiting for the receiver to connect…"))
	} else {
		b.WriteString(m.theme.Title.Render("Send — paste this blob to the receiver"))
		b.WriteString("\n\n")
		// QR frames if available
		if len(m.qrFrames) > 0 {
			var buf bytes.Buffer
			_ = qr.RenderOnce(&buf, m.qrFrames[m.qrIndex])
			b.WriteString(m.theme.Box.Render(buf.String()))
			b.WriteString("\n")
		}
		b.WriteString(m.theme.Blob.Render(m.displayText))
		b.WriteString("\n\n")
		b.WriteString("  ")
		b.WriteString(m.input.View())
		b.WriteString("\n\n")
		b.WriteString(m.theme.Help.Render("paste the receiver's answer blob · enter to connect"))
	}
	_ = pol
	return b.String()
}

func (m Model) viewRecvInput() string {
	var b strings.Builder
	pol, _ := m.settings.settings.Resolve()
	title := "Receive — paste the sender's offer blob"
	if pol.Signal == policy.SignalWorker {
		title = "Receive — enter the sender's token"
	}
	b.WriteString(m.theme.Title.Render(title))
	b.WriteString("\n\n")
	b.WriteString("  ")
	b.WriteString(m.input.View())
	b.WriteString("\n\n")
	b.WriteString(m.theme.Help.Render("enter to proceed · esc to cancel"))
	return b.String()
}

func (m Model) viewRecvShowAnswer() string {
	var b strings.Builder
	if m.respond != nil {
		b.WriteString(m.theme.Title.Render("Receive — answering via worker…"))
		b.WriteString("\n\n")
		b.WriteString(m.theme.Help.Render("waiting for the sender to connect…"))
	} else {
		b.WriteString(m.theme.Title.Render("Receive — send this blob back to the sender"))
		b.WriteString("\n\n")
		if len(m.qrFrames) > 0 {
			var buf bytes.Buffer
			_ = qr.RenderOnce(&buf, m.qrFrames[m.qrIndex])
			b.WriteString(m.theme.Box.Render(buf.String()))
			b.WriteString("\n")
		}
		b.WriteString(m.theme.Blob.Render(m.displayText))
		b.WriteString("\n\n")
		b.WriteString(m.theme.Help.Render("enter once the sender has the answer · connecting…"))
	}
	return b.String()
}

func (m Model) viewSpinner(title, subtitle string) string {
	var b strings.Builder
	b.WriteString(m.theme.Title.Render(title + "…"))
	b.WriteString("\n\n")
	b.WriteString(m.theme.Muted.Render(subtitle + "…"))
	b.WriteString("\n\n")
	b.WriteString(m.theme.Help.Render("esc to cancel"))
	return b.String()
}

func (m Model) viewTransfer(direction string) string {
	var b strings.Builder
	b.WriteString(m.theme.Title.Render(fmt.Sprintf("%s…", direction)))
	b.WriteString("\n\n")
	if m.connPath != "" {
		b.WriteString(m.theme.Success.Render("connected: " + m.connPath))
		b.WriteString("\n\n")
	}
	for id, total := range m.progressMax {
		done := m.progress[id]
		pct := 0
		if total > 0 {
			pct = int(done * 100 / total)
		}
		bar := progressBar(pct, 30)
		fmt.Fprintf(&b, "  file %d: %s %d%%\n", id, bar, pct)
	}
	b.WriteString("\n")
	b.WriteString(m.theme.Help.Render("esc to cancel"))
	return b.String()
}

func (m Model) viewDone(direction string) string {
	var b strings.Builder
	b.WriteString(m.theme.Title.Render(fmt.Sprintf("%s — done", direction)))
	b.WriteString("\n\n")
	if m.connPath != "" {
		b.WriteString(m.theme.Success.Render("connected: " + m.connPath))
		b.WriteString("\n")
	}
	if m.transferErr != nil {
		b.WriteString(m.theme.Error.Render("error: " + m.transferErr.Error()))
	} else {
		b.WriteString(m.theme.Success.Render("all files transferred successfully"))
	}
	b.WriteString("\n\n")
	b.WriteString(m.theme.Help.Render("press q to quit"))
	return b.String()
}

func (m Model) viewError() string {
	var b strings.Builder
	b.WriteString(m.theme.Title.Render("Error"))
	b.WriteString("\n\n")
	msg := m.lastErr.Error()
	if fm := friendlyMessage(m.lastErr); fm != "" {
		msg = fm
	}
	b.WriteString(m.theme.Error.Render(msg))
	b.WriteString("\n\n")
	b.WriteString(m.theme.Help.Render("press q to quit"))
	return b.String()
}

// --- Helpers ---

func progressBar(pct, width int) string {
	filled := pct * width / 100
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

// startSendOfferCmd resolves the policy, fetches ICE if needed, creates the
// offer — one goroutine, one message.
func startSendOfferCmd(ctx context.Context, s Settings) tea.Cmd {
	return func() tea.Msg {
		pol, err := s.Resolve()
		if err != nil {
			return offerCreatedMsg{err: err}
		}
		ice, err := resolveICE(ctx, s, pol)
		if err != nil {
			return offerCreatedMsg{err: err}
		}
		conn, offer, err := peer.Offer(ctx, peer.Config{
			ICEServers: ice,
			EnableMDNS: !s.NoMDNS,
		})
		return offerCreatedMsg{conn: conn, offer: offer, err: err}
	}
}

// startAnswerCmd resolves the policy, fetches ICE if needed, creates the
// answer from the given offer.
func startAnswerCmd(ctx context.Context, s Settings, offer webrtc.SessionDescription) tea.Cmd {
	return func() tea.Msg {
		pol, err := s.Resolve()
		if err != nil {
			return answerCreatedMsg{err: err}
		}
		ice, err := resolveICE(ctx, s, pol)
		if err != nil {
			return answerCreatedMsg{err: err}
		}
		conn, answer, err := peer.Answer(ctx, peer.Config{
			ICEServers: ice,
			EnableMDNS: !s.NoMDNS,
		}, offer)
		return answerCreatedMsg{conn: conn, answer: answer, err: err}
	}
}

// resolveICE returns the ICE server list for the policy: built-in STUN for
// none, fetched from the worker for full/fallback (TURN stripped on --no-relay).
func resolveICE(ctx context.Context, s Settings, pol policy.Policy) ([]webrtc.ICEServer, error) {
	if !pol.FetchICE {
		if s.NoSTUN {
			return nil, nil
		}
		return policy.BuiltinSTUN(), nil
	}
	w := &signal.Worker{BaseURL: s.Server}
	servers, err := w.ICEServers(ctx)
	if err != nil {
		return nil, err
	}
	if !pol.Relay {
		servers = policy.StripTURN(servers)
	}
	return servers, nil
}

// waitOpenCmd waits for the data channel to open and reads the connection
// path (receiver side — SetRemote was already done in peer.Answer).
func waitOpenCmd(ctx context.Context, conn *peer.Conn) tea.Cmd {
	return func() tea.Msg {
		if err := conn.WaitOpen(ctx); err != nil {
			return connectionFailedMsg{err: err}
		}
		path, perr := conn.ConnectionPath(ctx)
		return connectionOpenMsg{path: path, err: perr}
	}
}
