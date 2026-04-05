package tui

// view.go — All rendering logic for the TUI dashboard.
//
// The View() method is called by Bubble Tea every time the model changes.
// It builds a string that represents the entire terminal screen, using
// Lipgloss styles from styles.go.

import (
	"fmt"
	"strings"

	"jarvis/internal/model"
	"jarvis/internal/ui"

	"github.com/charmbracelet/lipgloss"
)

// View renders the entire dashboard as a string for Bubble Tea.
func (d Dashboard) View() string {
	var b strings.Builder

	// ── Header: title + session/blocked counts ──
	sessionCount := 0
	blockedCount := 0
	for _, item := range d.items {
		if item.IsSession() && item.Status != model.StatusDone && item.Status != model.StatusArchived {
			sessionCount++
		}
		if item.IsSession() && item.State == model.StateWaitingForApproval {
			blockedCount++
		}
	}

	title := titleStyle.Render("JARVIS")
	stats := statusBarStyle.Render(fmt.Sprintf(" — %d sessions", sessionCount))
	if blockedCount > 0 {
		stats += blockedStyle.Render(fmt.Sprintf(" · %d blocked", blockedCount))
	}
	b.WriteString(title + stats + "\n\n")

	// ── Item list (scrollable viewport) ──
	visibleItems := d.filteredItems()

	if len(visibleItems) == 0 {
		b.WriteString(dimStyle.Render("  No sessions. Press [n] to create one.\n"))
	}

	maxRows := d.viewportHeight()
	end := d.scrollOffset + maxRows
	if end > len(visibleItems) {
		end = len(visibleItems)
	}
	start := d.scrollOffset
	if start < 0 {
		start = 0
	}

	for i := start; i < end; i++ {
		item := visibleItems[i]
		indent := strings.Repeat("  ", item.Depth)
		line := d.renderItem(item)

		if i == d.cursor {
			b.WriteString(selectedStyle.Render("▌") + " " + indent + line + "\n")
		} else {
			b.WriteString("  " + indent + line + "\n")
		}
	}

	// ── Status message (transient feedback) ──
	if d.statusMsg != "" {
		b.WriteString("\n" + d.statusMsg + "\n")
	}

	// ── Footer: mode-dependent input area ──
	switch d.mode {
	case ModeSearch:
		b.WriteString("\n  " + inputStyle.Render("/") + " " + d.searchInput.View())
	case ModeInput:
		b.WriteString("\n  " + inputStyle.Render(d.cmdPrompt) + d.cmdInput.View())
	default:
		help := "  [enter] attach  [a]pprove  [n]ew  [c]hat  [f]older  [r]ename  [d]one  [x] delete  [/]search  [q]uit"
		b.WriteString(helpStyle.Render(help))
	}

	return b.String()
}

// renderItem produces a single formatted line for one list item.
func (d Dashboard) renderItem(item ListItem) string {
	// ── Separator ──
	if item.ID == "__separator__" {
		return dimStyle.Render(strings.Repeat("─", 40))
	}

	// ── Folder row: arrow + name + progress ──
	if item.IsFolder() {
		arrow := "▶"
		if d.isExpanded(item.ID) {
			arrow = "▼"
		}
		name := folderStyle.Render(item.Name)
		progress := ""
		if item.TotalCount > 0 {
			progress = dimStyle.Render(fmt.Sprintf(" %d/%d done", item.DoneCount, item.TotalCount))
		}
		return fmt.Sprintf("%s %s%s", arrow, name, progress)
	}

	// ── Session row: icon + name + state + age ──
	icon := sessionIcon(item.Status, item.State)

	var stateStr string
	switch {
	case item.State == model.StateWaitingForApproval:
		stateStr = blockedStyle.Render("blocked")
	case item.State == model.StateWorking:
		stateStr = activeStyle.Render("working")
	case item.State == model.StateIdle:
		stateStr = dimStyle.Render("idle")
	case item.Status == model.StatusSuspended:
		stateStr = suspendedStyle.Render("suspended")
	case item.Status == model.StatusDone:
		stateStr = doneStyle.Render("done")
	case item.Status == model.StatusQueued:
		stateStr = dimStyle.Render("queued")
	default:
		stateStr = dimStyle.Render(string(item.Status))
	}

	age := dimStyle.Render(item.Age)

	// Dynamic name width based on terminal width.
	// Layout budget: cursor(2) + indent(depth*2) + icon(2) + name + state(12) + age(6) + padding(4)
	nameWidth := d.width - 2 - item.Depth*2 - 2 - 12 - 6 - 4
	if nameWidth < 20 {
		nameWidth = 20
	}
	if nameWidth > 80 {
		nameWidth = 80
	}

	name := ui.Truncate(item.Name, nameWidth)
	namePad := lipgloss.NewStyle().Width(nameWidth).Render(name)
	statePad := lipgloss.NewStyle().Width(12).Render(stateStr)

	return fmt.Sprintf("%s %s %s %s", icon, namePad, statePad, age)
}

// sessionIcon returns a coloured Unicode icon for a session's current state.
func sessionIcon(status model.SessionStatus, state model.SidecarState) string {
	if state == model.StateWaitingForApproval {
		return blockedStyle.Render("⚠")
	}
	switch status {
	case model.StatusActive:
		return activeStyle.Render("●")
	case model.StatusSuspended:
		return suspendedStyle.Render("⏸")
	case model.StatusDone:
		return doneStyle.Render("✓")
	case model.StatusQueued:
		return dimStyle.Render("◌")
	default:
		return " "
	}
}
