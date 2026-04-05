// Package ui provides shared formatting helpers used by both the CLI
// commands and the TUI dashboard.
package ui

import (
	"fmt"
	"time"

	"jarvis/internal/model"
)

// FormatAge returns a human-friendly relative time string like "3m", "2h", "5d".
func FormatAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// Truncate shortens a string to maxLen characters, adding "..." if truncated.
func Truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// StatusIcon returns a Unicode icon representing a session's lifecycle status.
//
//	● active    ⏸ suspended    ✓ done    ◌ queued    ▪ archived
func StatusIcon(status model.SessionStatus) string {
	switch status {
	case model.StatusActive:
		return "●"
	case model.StatusSuspended:
		return "⏸"
	case model.StatusDone:
		return "✓"
	case model.StatusQueued:
		return "◌"
	case model.StatusArchived:
		return "▪"
	default:
		return "?"
	}
}
