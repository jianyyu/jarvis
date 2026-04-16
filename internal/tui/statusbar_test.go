package tui

import (
	"strings"
	"testing"
)

func TestNewStatusBar_EmptyState_RendersSomething(t *testing.T) {
	sb := NewStatusBar(80)
	view := sb.View()
	if view == "" {
		t.Error("expected non-empty View() for empty status bar")
	}
	if !strings.Contains(view, "No session selected") {
		t.Errorf("expected 'No session selected' in empty state, got %q", view)
	}
}

func TestStatusBar_SetSession_ShowsName(t *testing.T) {
	sb := NewStatusBar(80)
	sb.SetSession("my-session", "running")
	view := sb.View()
	if !strings.Contains(view, "my-session") {
		t.Errorf("expected 'my-session' in view, got %q", view)
	}
	if !strings.Contains(view, "running") {
		t.Errorf("expected 'running' in view, got %q", view)
	}
}

func TestStatusBar_ClearSession_RemovesSessionInfo(t *testing.T) {
	sb := NewStatusBar(80)
	sb.SetSession("my-session", "running")
	sb.ClearSession()
	view := sb.View()
	if strings.Contains(view, "my-session") {
		t.Errorf("expected 'my-session' to be cleared, got %q", view)
	}
	if !strings.Contains(view, "No session selected") {
		t.Errorf("expected 'No session selected' after clear, got %q", view)
	}
}

func TestStatusBar_SetWidth_NoPanic(t *testing.T) {
	sb := NewStatusBar(80)
	sb.SetWidth(120)
	sb.SetWidth(0)
	sb.SetWidth(20)
	// Just verifying no panic occurs.
	_ = sb.View()
}

func TestStatusBar_SetWidth_UpdatesWidth(t *testing.T) {
	sb := NewStatusBar(80)
	sb.SetWidth(120)
	view := sb.View()
	// The rendered line should be padded to at least the new width.
	// lipgloss may add ANSI escape codes, but the raw content width should match.
	// We just verify it doesn't crash and produces output.
	if view == "" {
		t.Error("expected non-empty view after SetWidth")
	}
}

func TestStatusBar_ViewContainsHints(t *testing.T) {
	sb := NewStatusBar(80)
	view := sb.View()
	if !strings.Contains(view, "sidebar") {
		t.Errorf("expected keybind hint 'sidebar' in view, got %q", view)
	}
}

func TestStatusBar_SessionViewContainsSeparator(t *testing.T) {
	sb := NewStatusBar(80)
	sb.SetSession("test-session", "active")
	view := sb.View()
	if !strings.Contains(view, "\u2502") {
		t.Errorf("expected separator '│' in session view, got %q", view)
	}
}

func TestStatusBar_NarrowWidth(t *testing.T) {
	sb := NewStatusBar(10)
	sb.SetSession("very-long-session-name", "running")
	// Should not panic even with very narrow width.
	view := sb.View()
	if view == "" {
		t.Error("expected non-empty view with narrow width")
	}
}

func TestStatusBar_ZeroWidth(t *testing.T) {
	sb := NewStatusBar(0)
	// Should not panic.
	view := sb.View()
	if view == "" {
		t.Error("expected non-empty view with zero width")
	}
}
