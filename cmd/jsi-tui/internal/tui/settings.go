package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/treyt/jsi/internal/policy"
)

// Settings holds the user-configurable D15 policy options (PLAN.md §7.5).
// Persisted in the Model across screens.
type Settings struct {
	Externals string // none | fallback | full
	Signal    string // "" (default) | paste | worker | qr
	Relay     policy.RelaySetting
	Server    string
	NoSTUN    bool
	NoMDNS    bool
}

// DefaultSettings returns the D15 default: externals none (paste signaling +
// STUN, zero server contact), the hosted worker URL for fallback/full.
func DefaultSettings() Settings {
	return Settings{
		Externals: policy.ExternalsNone,
		Signal:    "",
		Relay:     policy.RelayUnset,
		Server:    DefaultServerURL,
		NoSTUN:    false,
		NoMDNS:    false,
	}
}

// Resolve maps Settings to a policy.Policy via the tested ResolvePolicy.
func (s Settings) Resolve() (policy.Policy, error) {
	return policy.ResolvePolicy(s.Externals, s.Signal, s.Relay)
}

// Summary is the D15 transparency line (delegates to policy.Policy.Summary).
func (s Settings) Summary() string {
	p, err := s.Resolve()
	if err != nil {
		return fmt.Sprintf("mode: invalid — %v", err)
	}
	return p.Summary(s.Server)
}

// settingsCursor indexes the editable fields (M5.1 settings screen).
type settingsCursor int

const (
	setExternals settingsCursor = iota
	setSignal
	setRelay
	setServer
	setNoSTUN
	setNoMDNS
	setCount
)

func (c settingsCursor) String() string {
	switch c {
	case setExternals:
		return "externals"
	case setSignal:
		return "signal"
	case setRelay:
		return "relay"
	case setServer:
		return "server"
	case setNoSTUN:
		return "no-stun"
	case setNoMDNS:
		return "no-mdns"
	default:
		return "?"
	}
}

// settingsModel is the editable settings screen state.
type settingsModel struct {
	settings Settings
	cursor   settingsCursor
}

func newSettingsModel(s Settings) settingsModel {
	return settingsModel{settings: s}
}

// cycleExternals rotates none → fallback → full → none.
func (m *settingsModel) cycleExternals() {
	switch m.settings.Externals {
	case policy.ExternalsNone:
		m.settings.Externals = policy.ExternalsFallback
	case policy.ExternalsFallback:
		m.settings.Externals = policy.ExternalsFull
	default:
		m.settings.Externals = policy.ExternalsNone
	}
}

// cycleSignal rotates "" → paste → worker → qr → "".
func (m *settingsModel) cycleSignal() {
	switch m.settings.Signal {
	case "":
		m.settings.Signal = policy.SignalPaste
	case policy.SignalPaste:
		m.settings.Signal = policy.SignalWorker
	case policy.SignalWorker:
		m.settings.Signal = policy.SignalQR
	default:
		m.settings.Signal = ""
	}
}

// cycleRelay rotates unset → on → off → unset.
func (m *settingsModel) cycleRelay() {
	switch m.settings.Relay {
	case policy.RelayUnset:
		m.settings.Relay = policy.RelayOn
	case policy.RelayOn:
		m.settings.Relay = policy.RelayOff
	default:
		m.settings.Relay = policy.RelayUnset
	}
}

func (m *settingsModel) toggleNoSTUN() { m.settings.NoSTUN = !m.settings.NoSTUN }
func (m *settingsModel) toggleNoMDNS() { m.settings.NoMDNS = !m.settings.NoMDNS }

// updateSettings handles key events on the settings screen.
func (m *settingsModel) update(msg tea.Msg) (settingsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyUp, tea.KeyDown:
			if msg.Type == tea.KeyUp {
				if m.cursor == 0 {
					m.cursor = setCount - 1
				} else {
					m.cursor--
				}
			} else {
				m.cursor = (m.cursor + 1) % setCount
			}
		case tea.KeyLeft, tea.KeyRight:
			m.activate(msg.Type == tea.KeyRight)
		case tea.KeyEnter:
			m.activate(true)
		case tea.KeyRunes:
			switch string(msg.Runes) {
			case "j":
				m.cursor = (m.cursor + 1) % setCount
			case "k":
				if m.cursor == 0 {
					m.cursor = setCount - 1
				} else {
					m.cursor--
				}
			case " ":
				m.activate(true)
			}
		}
	}
	return *m, nil
}

func (m *settingsModel) activate(right bool) {
	switch m.cursor {
	case setExternals:
		m.cycleExternals()
	case setSignal:
		if right {
			m.cycleSignal()
		}
	case setRelay:
		m.cycleRelay()
	case setServer:
		// Server URL is edited externally (not inline-editable in v1);
		// left/right cycles signal so the user can change presets without
		// typing.
		_ = right
	case setNoSTUN:
		m.toggleNoSTUN()
	case setNoMDNS:
		m.toggleNoMDNS()
	}
}

// viewSettings renders the settings screen.
func (m settingsModel) view(width int) string {
	var b strings.Builder
	b.WriteString(DefaultTheme.Title.Render("JSI — Settings"))
	b.WriteString("\n\n")

	rows := []struct {
		label string
		value string
	}{
		{"externals", m.settings.Externals},
		{"signal", signalLabel(m.settings.Signal)},
		{"relay", relayLabel(m.settings.Relay)},
		{"server", m.settings.Server},
		{"no-stun", boolLabel(m.settings.NoSTUN)},
		{"no-mdns", boolLabel(m.settings.NoMDNS)},
	}

	for i, row := range rows {
		cursor := "  "
		style := DefaultTheme.Normal
		if settingsCursor(i) == m.cursor {
			cursor = "▶ "
			style = DefaultTheme.Selected
		}
		b.WriteString(style.Render(fmt.Sprintf("%s%-12s %s", cursor, row.label+":", row.value)))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(DefaultTheme.Help.Render("j/k or ↑/↓ navigate · ←/→ or enter change · esc back"))
	b.WriteString("\n\n")
	b.WriteString(DefaultTheme.Muted.Render(m.settings.Summary()))
	return b.String()
}

func signalLabel(s string) string {
	if s == "" {
		return "(preset default)"
	}
	return s
}

func relayLabel(r policy.RelaySetting) string {
	switch r {
	case policy.RelayOn:
		return "on"
	case policy.RelayOff:
		return "off"
	default:
		return "(preset default)"
	}
}

func boolLabel(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
