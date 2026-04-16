package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// StatusBar renders a single-line bar at the bottom of the TUI showing the
// active session name, its state, and keybind hints.
type StatusBar struct {
	width       int
	sessionName string
	sessionState string
}

// Styles scoped to the status bar.
var (
	statusBarBg = lipgloss.NewStyle().
			Background(lipgloss.Color("236")).
			Foreground(lipgloss.Color("252"))

	statusBarHint = lipgloss.NewStyle().
			Background(lipgloss.Color("236")).
			Foreground(lipgloss.Color("241"))
)

// NewStatusBar creates a StatusBar sized to the given width.
func NewStatusBar(width int) *StatusBar {
	return &StatusBar{width: width}
}

// SetWidth updates the bar width, typically called on terminal resize.
func (s *StatusBar) SetWidth(w int) {
	s.width = w
}

// SetSession stores the active session name and state for rendering.
func (s *StatusBar) SetSession(name, state string) {
	s.sessionName = name
	s.sessionState = state
}

// ClearSession removes session info so the bar shows the empty-state message.
func (s *StatusBar) ClearSession() {
	s.sessionName = ""
	s.sessionState = ""
}

// View renders the status bar as a single styled line.
func (s *StatusBar) View() string {
	width := s.width
	if width < 1 {
		width = 1
	}

	var left string
	if s.sessionName == "" {
		left = " No session selected"
	} else {
		left = " \u25cf " + s.sessionName + " \u2502 " + s.sessionState
	}

	right := "Ctrl+\\: sidebar "

	// Calculate padding between left and right sections.
	padding := width - lipgloss.Width(left) - lipgloss.Width(right)
	if padding < 0 {
		padding = 0
	}

	line := statusBarBg.Render(left) +
		statusBarBg.Render(strings.Repeat(" ", padding)) +
		statusBarHint.Render(right)

	return line
}
