package tui

// focus.go — Tracks which component has keyboard focus in the multiplexer TUI.
//
// Two possible targets: the sidebar (session list) or the terminal pane
// (active Claude session).  The terminal pane can only receive focus when
// there is an active session attached.

// FocusTarget identifies which component currently owns keyboard input.
type FocusTarget int

const (
	// FocusSidebar directs input to the session list / sidebar.
	FocusSidebar FocusTarget = iota
	// FocusTermPane directs input to the active terminal pane.
	FocusTermPane
)

// FocusManager is a simple state machine that tracks the current focus target
// and enforces the constraint that the terminal pane requires an active session.
type FocusManager struct {
	current       FocusTarget
	activeSession bool
}

// NewFocusManager returns a FocusManager with focus on the sidebar and no
// active session.
func NewFocusManager() *FocusManager {
	return &FocusManager{
		current:       FocusSidebar,
		activeSession: false,
	}
}

// Current returns the focus target that currently owns keyboard input.
func (fm *FocusManager) Current() FocusTarget {
	return fm.current
}

// Toggle switches focus between sidebar and terminal pane.  If there is no
// active session, focus stays on the sidebar.
func (fm *FocusManager) Toggle() {
	if !fm.activeSession {
		return
	}
	if fm.current == FocusSidebar {
		fm.current = FocusTermPane
	} else {
		fm.current = FocusSidebar
	}
}

// SetFocus directly sets the focus target.  Setting FocusTermPane is only
// allowed when there is an active session; otherwise the call is ignored.
func (fm *FocusManager) SetFocus(target FocusTarget) {
	if target == FocusTermPane && !fm.activeSession {
		return
	}
	fm.current = target
}

// SetActiveSession updates whether there is an active session attached.  When
// set to false while focus is on the terminal pane, focus is forced back to
// the sidebar.
func (fm *FocusManager) SetActiveSession(active bool) {
	fm.activeSession = active
	if !active && fm.current == FocusTermPane {
		fm.current = FocusSidebar
	}
}

// HasActiveSession returns whether the focus manager believes there is an
// active session.
func (fm *FocusManager) HasActiveSession() bool {
	return fm.activeSession
}
