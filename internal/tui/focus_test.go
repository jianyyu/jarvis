package tui

import "testing"

func TestNewFocusManager_StartsWithSidebar(t *testing.T) {
	fm := NewFocusManager()
	if fm.Current() != FocusSidebar {
		t.Errorf("expected initial focus to be FocusSidebar, got %v", fm.Current())
	}
}

func TestNewFocusManager_NoActiveSession(t *testing.T) {
	fm := NewFocusManager()
	if fm.HasActiveSession() {
		t.Error("expected HasActiveSession() to be false on new FocusManager")
	}
}

func TestToggle_WithActiveSession(t *testing.T) {
	fm := NewFocusManager()
	fm.SetActiveSession(true)

	fm.Toggle()
	if fm.Current() != FocusTermPane {
		t.Errorf("expected FocusTermPane after first toggle, got %v", fm.Current())
	}

	fm.Toggle()
	if fm.Current() != FocusSidebar {
		t.Errorf("expected FocusSidebar after second toggle, got %v", fm.Current())
	}
}

func TestToggle_WithoutActiveSession_StaysOnSidebar(t *testing.T) {
	fm := NewFocusManager()

	fm.Toggle()
	if fm.Current() != FocusSidebar {
		t.Errorf("expected FocusSidebar when toggling without active session, got %v", fm.Current())
	}
}

func TestSetFocus_TermPane_RequiresActiveSession(t *testing.T) {
	fm := NewFocusManager()

	// Without active session, SetFocus to TermPane should be ignored.
	fm.SetFocus(FocusTermPane)
	if fm.Current() != FocusSidebar {
		t.Errorf("expected FocusSidebar when SetFocus(TermPane) without active session, got %v", fm.Current())
	}

	// With active session, SetFocus to TermPane should work.
	fm.SetActiveSession(true)
	fm.SetFocus(FocusTermPane)
	if fm.Current() != FocusTermPane {
		t.Errorf("expected FocusTermPane after SetFocus with active session, got %v", fm.Current())
	}
}

func TestSetFocus_Sidebar_AlwaysAllowed(t *testing.T) {
	fm := NewFocusManager()
	fm.SetActiveSession(true)
	fm.SetFocus(FocusTermPane)

	fm.SetFocus(FocusSidebar)
	if fm.Current() != FocusSidebar {
		t.Errorf("expected FocusSidebar after SetFocus(Sidebar), got %v", fm.Current())
	}
}

func TestSetActiveSession_FalseForcesSidebar(t *testing.T) {
	fm := NewFocusManager()
	fm.SetActiveSession(true)
	fm.SetFocus(FocusTermPane)

	if fm.Current() != FocusTermPane {
		t.Fatalf("precondition: expected FocusTermPane, got %v", fm.Current())
	}

	fm.SetActiveSession(false)

	if fm.Current() != FocusSidebar {
		t.Errorf("expected FocusSidebar after SetActiveSession(false), got %v", fm.Current())
	}
	if fm.HasActiveSession() {
		t.Error("expected HasActiveSession() to be false")
	}
}

func TestSetActiveSession_FalseFromSidebar_StaysOnSidebar(t *testing.T) {
	fm := NewFocusManager()
	fm.SetActiveSession(true)

	// Focus is still sidebar.
	fm.SetActiveSession(false)
	if fm.Current() != FocusSidebar {
		t.Errorf("expected FocusSidebar, got %v", fm.Current())
	}
}

func TestSetActiveSession_True_DoesNotChangeFocus(t *testing.T) {
	fm := NewFocusManager()

	fm.SetActiveSession(true)
	if fm.Current() != FocusSidebar {
		t.Errorf("expected focus to remain FocusSidebar after SetActiveSession(true), got %v", fm.Current())
	}
}

func TestToggle_RoundTrip(t *testing.T) {
	fm := NewFocusManager()
	fm.SetActiveSession(true)

	// Toggle several times and verify the round-trip.
	for i := 0; i < 10; i++ {
		fm.Toggle()
	}
	// Even number of toggles: should be back to Sidebar.
	if fm.Current() != FocusSidebar {
		t.Errorf("expected FocusSidebar after even toggles, got %v", fm.Current())
	}

	fm.Toggle()
	if fm.Current() != FocusTermPane {
		t.Errorf("expected FocusTermPane after odd toggles, got %v", fm.Current())
	}
}
