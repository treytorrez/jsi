package tui

import "github.com/charmbracelet/lipgloss"

// Theme holds the lipgloss styles for the TUI (M5.1). Colors are ANSI 256
// for broad terminal compatibility.
type Theme struct {
	Title       lipgloss.Style
	Subtitle    lipgloss.Style
	Selected    lipgloss.Style
	Normal      lipgloss.Style
	Highlight   lipgloss.Style
	Muted       lipgloss.Style
	Success     lipgloss.Style
	Error       lipgloss.Style
	Box         lipgloss.Style
	Token       lipgloss.Style
	Blob        lipgloss.Style
	Help        lipgloss.Style
	ProgressBar lipgloss.Style
}

// NewTheme returns the default theme.
func NewTheme() Theme {
	return Theme{
		Title:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")).Padding(0, 1),
		Subtitle:  lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Padding(0, 1),
		Selected:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")),
		Normal:    lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
		Highlight: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("46")),
		Muted:     lipgloss.NewStyle().Foreground(lipgloss.Color("243")),
		Success:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("46")),
		Error:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196")),
		Box:       lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2).BorderForeground(lipgloss.Color("39")),
		Token:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("226")).Padding(1, 2),
		Blob:      lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Padding(0, 1).MaxWidth(120),
		Help:      lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Padding(0, 1),
	}
}

// DefaultTheme is the shared theme instance.
var DefaultTheme = NewTheme()
