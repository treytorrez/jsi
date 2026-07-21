// Command jsi-tui is the interactive terminal UI for JSI (PLAN.md §7.6, M5).
// It wraps the same internal/peer + internal/signal + internal/transfer stack
// as the CLI, presenting send/receive flows through a Bubble Tea interface.
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/treyt/jsi/cmd/jsi-tui/internal/tui"
)

func main() {
	m := tui.NewModel()
	p := tea.NewProgram(m, tea.WithAltScreen())
	m.SetProgram(p)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "jsi-tui: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.ExitCode())
}
