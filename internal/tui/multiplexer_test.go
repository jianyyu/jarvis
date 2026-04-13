package tui

import (
	"testing"

	"jarvis/internal/config"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMultiplexer_New(t *testing.T) {
	cfg := &config.Config{}
	m := NewMultiplexer(cfg)

	if m.sidebar == nil {
		t.Error("expected non-nil sidebar")
	}
	if m.termPane == nil {
		t.Error("expected non-nil termPane")
	}
	if m.statusBar == nil {
		t.Error("expected non-nil statusBar")
	}
	if m.focus == nil {
		t.Error("expected non-nil focus")
	}
	if m.cfg != cfg {
		t.Error("expected cfg to match")
	}
	if m.sidebarWidth != 24 {
		t.Errorf("expected default sidebarWidth 24, got %d", m.sidebarWidth)
	}
}

func TestMultiplexer_View(t *testing.T) {
	cfg := &config.Config{}
	m := NewMultiplexer(cfg)

	// Before WindowSizeMsg, View should still produce output (possibly minimal).
	view := m.View()
	if view == "" {
		t.Error("expected non-empty View() even before WindowSizeMsg")
	}

	// After WindowSizeMsg, should produce a non-empty view.
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m2 := model.(Multiplexer)
	view2 := m2.View()
	if view2 == "" {
		t.Error("expected non-empty View() after WindowSizeMsg")
	}
}

func TestMultiplexer_InitialFocus(t *testing.T) {
	cfg := &config.Config{}
	m := NewMultiplexer(cfg)

	if m.focus.Current() != FocusSidebar {
		t.Errorf("expected initial focus to be FocusSidebar, got %v", m.focus.Current())
	}
}

func TestMultiplexer_WindowSizeMsg(t *testing.T) {
	cfg := &config.Config{}
	m := NewMultiplexer(cfg)

	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	m2 := model.(Multiplexer)

	if m2.width != 100 {
		t.Errorf("expected width 100, got %d", m2.width)
	}
	if m2.height != 50 {
		t.Errorf("expected height 50, got %d", m2.height)
	}
}

func TestMultiplexer_QuitKey(t *testing.T) {
	cfg := &config.Config{}
	m := NewMultiplexer(cfg)

	// Set a size so the model has dimensions.
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m2 := model.(Multiplexer)

	// q should produce a quit command when sidebar is focused.
	_, cmd := m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Error("expected non-nil cmd for 'q' key")
	}
}

func TestMultiplexer_CtrlCKey(t *testing.T) {
	cfg := &config.Config{}
	m := NewMultiplexer(cfg)

	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m2 := model.(Multiplexer)

	// ctrl+c should produce a quit command.
	_, cmd := m2.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Error("expected non-nil cmd for ctrl+c key")
	}
}

func TestKeyToBytes_BasicKeys(t *testing.T) {
	tests := []struct {
		name     string
		msg      tea.KeyMsg
		expected string
	}{
		{
			name:     "enter",
			msg:      tea.KeyMsg{Type: tea.KeyEnter},
			expected: "\r",
		},
		{
			name:     "tab",
			msg:      tea.KeyMsg{Type: tea.KeyTab},
			expected: "\t",
		},
		{
			name:     "escape",
			msg:      tea.KeyMsg{Type: tea.KeyEscape},
			expected: "\x1b",
		},
		{
			name:     "backspace",
			msg:      tea.KeyMsg{Type: tea.KeyBackspace},
			expected: "\x7f",
		},
		{
			name:     "up arrow",
			msg:      tea.KeyMsg{Type: tea.KeyUp},
			expected: "\x1b[A",
		},
		{
			name:     "down arrow",
			msg:      tea.KeyMsg{Type: tea.KeyDown},
			expected: "\x1b[B",
		},
		{
			name:     "right arrow",
			msg:      tea.KeyMsg{Type: tea.KeyRight},
			expected: "\x1b[C",
		},
		{
			name:     "left arrow",
			msg:      tea.KeyMsg{Type: tea.KeyLeft},
			expected: "\x1b[D",
		},
		{
			name:     "space",
			msg:      tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}},
			expected: " ",
		},
		{
			name:     "delete",
			msg:      tea.KeyMsg{Type: tea.KeyDelete},
			expected: "\x1b[3~",
		},
		{
			name:     "home",
			msg:      tea.KeyMsg{Type: tea.KeyHome},
			expected: "\x1b[H",
		},
		{
			name:     "end",
			msg:      tea.KeyMsg{Type: tea.KeyEnd},
			expected: "\x1b[F",
		},
		{
			name:     "page up",
			msg:      tea.KeyMsg{Type: tea.KeyPgUp},
			expected: "\x1b[5~",
		},
		{
			name:     "page down",
			msg:      tea.KeyMsg{Type: tea.KeyPgDown},
			expected: "\x1b[6~",
		},
		{
			name:     "rune a",
			msg:      tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}},
			expected: "a",
		},
		{
			name:     "ctrl+a",
			msg:      tea.KeyMsg{Type: tea.KeyCtrlA},
			expected: "\x01",
		},
		{
			name:     "ctrl+c",
			msg:      tea.KeyMsg{Type: tea.KeyCtrlC},
			expected: "\x03",
		},
		{
			name:     "ctrl+z",
			msg:      tea.KeyMsg{Type: tea.KeyCtrlZ},
			expected: "\x1a",
		},
		{
			name:     "F1",
			msg:      tea.KeyMsg{Type: tea.KeyF1},
			expected: "\x1bOP",
		},
		{
			name:     "F2",
			msg:      tea.KeyMsg{Type: tea.KeyF2},
			expected: "\x1bOQ",
		},
		{
			name:     "F3",
			msg:      tea.KeyMsg{Type: tea.KeyF3},
			expected: "\x1bOR",
		},
		{
			name:     "F4",
			msg:      tea.KeyMsg{Type: tea.KeyF4},
			expected: "\x1bOS",
		},
		{
			name:     "F5",
			msg:      tea.KeyMsg{Type: tea.KeyF5},
			expected: "\x1b[15~",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := keyToBytes(tc.msg)
			if got != tc.expected {
				t.Errorf("keyToBytes(%s) = %q, want %q", tc.name, got, tc.expected)
			}
		})
	}
}

func TestKeyToBytes_MultipleRunes(t *testing.T) {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h', 'i'}}
	got := keyToBytes(msg)
	if got != "hi" {
		t.Errorf("keyToBytes(multi-rune) = %q, want %q", got, "hi")
	}
}

func TestMultiplexer_StatusPollMsg(t *testing.T) {
	cfg := &config.Config{}
	m := NewMultiplexer(cfg)

	// statusPollMsg should return a non-nil cmd (reschedule).
	model, cmd := m.Update(statusPollMsg{})
	if cmd == nil {
		t.Error("expected non-nil cmd for statusPollMsg")
	}
	_ = model
}

func TestMultiplexer_RefreshMsg(t *testing.T) {
	cfg := &config.Config{}
	m := NewMultiplexer(cfg)

	// Send a refreshMsg with some items.
	items := []ListItem{
		{Type: ItemSession, ID: "s1", Name: "Session One"},
	}
	model, _ := m.Update(refreshMsg{items: items})
	m2 := model.(Multiplexer)

	// The sidebar should have received the items.
	sel := m2.sidebar.SelectedItem()
	if sel == nil {
		t.Fatal("expected SelectedItem after refreshMsg")
	}
	if sel.ID != "s1" {
		t.Errorf("expected selected item 's1', got %q", sel.ID)
	}
}

func TestMultiplexer_SessionAttachFailedMsg(t *testing.T) {
	cfg := &config.Config{}
	m := NewMultiplexer(cfg)

	// sessionAttachFailedMsg should refresh sidebar.
	model, cmd := m.Update(sessionAttachFailedMsg{err: nil})
	_ = model
	if cmd == nil {
		t.Error("expected non-nil cmd for sessionAttachFailedMsg")
	}
}
