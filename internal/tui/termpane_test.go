package tui

import (
	"strings"
	"testing"
)

func TestNewTermPane(t *testing.T) {
	tp := NewTermPane(80, 24)
	defer tp.Close()

	if tp == nil {
		t.Fatal("NewTermPane returned nil")
	}
	if tp.cols != 80 {
		t.Errorf("cols = %d, want 80", tp.cols)
	}
	if tp.rows != 24 {
		t.Errorf("rows = %d, want 24", tp.rows)
	}
	if tp.emulator == nil {
		t.Fatal("emulator is nil")
	}
}

func TestIsAttachedDefault(t *testing.T) {
	tp := NewTermPane(80, 24)
	defer tp.Close()

	if tp.IsAttached() {
		t.Error("new TermPane should not be attached")
	}
}

func TestIsConnectedDefault(t *testing.T) {
	tp := NewTermPane(80, 24)
	defer tp.Close()

	if tp.IsConnected() {
		t.Error("new TermPane should not be connected")
	}
}

func TestSessionIDDefault(t *testing.T) {
	tp := NewTermPane(80, 24)
	defer tp.Close()

	if id := tp.SessionID(); id != "" {
		t.Errorf("SessionID() = %q, want empty", id)
	}
}

func TestWriteOutputAndView(t *testing.T) {
	tp := NewTermPane(80, 24)
	defer tp.Close()

	// Mark as connected so View() uses the emulator rather than the placeholder.
	tp.mu.Lock()
	tp.connected = true
	tp.mu.Unlock()

	tp.WriteOutput([]byte("Hello, World!"))

	view := tp.View()
	if !strings.Contains(view, "Hello, World!") {
		t.Errorf("View() should contain written output, got:\n%s", view)
	}
}

func TestViewPlaceholderWhenDisconnected(t *testing.T) {
	tp := NewTermPane(80, 24)
	defer tp.Close()

	view := tp.View()
	if !strings.Contains(view, "Select a session from the sidebar") {
		t.Errorf("disconnected View() should show placeholder, got:\n%s", view)
	}
	if !strings.Contains(view, "press 'n' to create one") {
		t.Errorf("disconnected View() should show press 'n' hint, got:\n%s", view)
	}
}

func TestResizeDoesNotPanic(t *testing.T) {
	tp := NewTermPane(80, 24)
	defer tp.Close()

	// Should not panic even when not connected.
	tp.Resize(120, 40)

	if tp.cols != 120 {
		t.Errorf("cols after resize = %d, want 120", tp.cols)
	}
	if tp.rows != 40 {
		t.Errorf("rows after resize = %d, want 40", tp.rows)
	}
}

func TestResizeZeroDoesNotPanic(t *testing.T) {
	tp := NewTermPane(80, 24)
	defer tp.Close()

	// Edge case: zero dimensions should not panic.
	tp.Resize(0, 0)
}

func TestSendInputNoopWhenNotAttached(t *testing.T) {
	tp := NewTermPane(80, 24)
	defer tp.Close()

	// Should not panic when not connected/attached.
	tp.SendInput("hello")
}

func TestDisconnectResetsState(t *testing.T) {
	tp := NewTermPane(80, 24)
	defer tp.Close()

	// Simulate connected state.
	tp.mu.Lock()
	tp.connected = true
	tp.attached = true
	tp.sessionID = "test-session"
	tp.mu.Unlock()

	tp.Disconnect()

	if tp.IsConnected() {
		t.Error("should not be connected after Disconnect")
	}
	if tp.IsAttached() {
		t.Error("should not be attached after Disconnect")
	}
	if tp.SessionID() != "" {
		t.Error("sessionID should be empty after Disconnect")
	}
}

func TestCloseDoesNotPanic(t *testing.T) {
	tp := NewTermPane(80, 24)
	tp.Close()

	// Calling Close again should not panic.
	tp.Close()
}
