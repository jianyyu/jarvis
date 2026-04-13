package tui

import (
	"strings"
	"testing"

	"jarvis/internal/config"
)

func TestSidebar_New(t *testing.T) {
	cfg := &config.Config{}
	sb := NewSidebar(cfg, 30, 40)

	if sb == nil {
		t.Fatal("NewSidebar returned nil")
	}
	if sb.width != 30 {
		t.Errorf("expected width 30, got %d", sb.width)
	}
	if sb.height != 40 {
		t.Errorf("expected height 40, got %d", sb.height)
	}
	if sb.mode != ModeDashboard {
		t.Errorf("expected ModeDashboard, got %v", sb.mode)
	}
	if sb.expandState == nil {
		t.Error("expected expandState to be initialized")
	}
	if sb.mgr == nil {
		t.Error("expected Manager to be initialized")
	}
	if sb.cfg != cfg {
		t.Error("expected cfg to match")
	}
}

func TestSidebar_View(t *testing.T) {
	cfg := &config.Config{}
	sb := NewSidebar(cfg, 30, 40)

	view := sb.View()
	if view == "" {
		t.Error("expected non-empty View()")
	}
	if !strings.Contains(view, "SESSIONS") {
		t.Errorf("expected 'SESSIONS' header in view, got %q", view)
	}
}

func TestSidebar_Resize(t *testing.T) {
	cfg := &config.Config{}
	sb := NewSidebar(cfg, 30, 40)

	sb.SetSize(50, 60)
	if sb.width != 50 {
		t.Errorf("expected width 50 after SetSize, got %d", sb.width)
	}
	if sb.height != 60 {
		t.Errorf("expected height 60 after SetSize, got %d", sb.height)
	}

	// Should not panic with zero or negative.
	sb.SetSize(0, 0)
	_ = sb.View()
}

func TestSidebar_SelectedItem(t *testing.T) {
	cfg := &config.Config{}
	sb := NewSidebar(cfg, 30, 40)

	item := sb.SelectedItem()
	if item != nil {
		t.Errorf("expected nil SelectedItem on empty sidebar, got %+v", item)
	}
}

func TestSidebar_Focus(t *testing.T) {
	cfg := &config.Config{}
	sb := NewSidebar(cfg, 30, 40)

	if sb.IsFocused() {
		t.Error("expected sidebar to not be focused initially")
	}

	sb.SetFocused(true)
	if !sb.IsFocused() {
		t.Error("expected sidebar to be focused after SetFocused(true)")
	}

	sb.SetFocused(false)
	if sb.IsFocused() {
		t.Error("expected sidebar to not be focused after SetFocused(false)")
	}
}

func TestSidebar_Manager(t *testing.T) {
	cfg := &config.Config{}
	sb := NewSidebar(cfg, 30, 40)

	mgr := sb.Manager()
	if mgr == nil {
		t.Error("expected non-nil Manager()")
	}
}

func TestSidebar_HandleRefresh(t *testing.T) {
	cfg := &config.Config{}
	sb := NewSidebar(cfg, 30, 40)
	// Reset cursor to a known position independent of disk state.
	sb.cursor = 0
	sb.scrollOffset = 0

	items := []ListItem{
		{Type: ItemSession, ID: "s1", Name: "Session One"},
		{Type: ItemSession, ID: "s2", Name: "Session Two"},
	}

	sb.HandleRefresh(items)

	if len(sb.items) != 2 {
		t.Errorf("expected 2 items after HandleRefresh, got %d", len(sb.items))
	}

	sel := sb.SelectedItem()
	if sel == nil {
		t.Fatal("expected non-nil SelectedItem after HandleRefresh with items")
	}
	if sel.ID != "s1" {
		t.Errorf("expected first item selected, got %s", sel.ID)
	}
}

func TestSidebar_ViewWithItems(t *testing.T) {
	cfg := &config.Config{}
	sb := NewSidebar(cfg, 40, 40)
	// Reset cursor/scroll to known position independent of disk state.
	sb.cursor = 0
	sb.scrollOffset = 0

	items := []ListItem{
		{Type: ItemSession, ID: "s1", Name: "Session One"},
		{Type: ItemFolder, ID: "f1", Name: "My Folder"},
	}
	sb.HandleRefresh(items)

	view := sb.View()
	if !strings.Contains(view, "Session One") {
		t.Errorf("expected 'Session One' in view, got %q", view)
	}
	if !strings.Contains(view, "My Folder") {
		t.Errorf("expected 'My Folder' in view, got %q", view)
	}
}

func TestSidebar_ViewFooter(t *testing.T) {
	cfg := &config.Config{}
	sb := NewSidebar(cfg, 40, 40)

	view := sb.View()
	// The abbreviated footer should contain these hints.
	if !strings.Contains(view, "[n]") {
		t.Errorf("expected '[n]' in footer, got %q", view)
	}
	if !strings.Contains(view, "[/]") {
		t.Errorf("expected '[/]' in footer, got %q", view)
	}
}
