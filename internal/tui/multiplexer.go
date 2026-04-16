package tui

// multiplexer.go — Root tea.Model that composes Sidebar, TermPane, StatusBar,
// and FocusManager into the multiplexer TUI. This is the main entry point
// that replaces the old full-screen Dashboard for interactive use.

import (
	"log"
	"strings"
	"time"

	"jarvis/internal/config"
	"jarvis/internal/session"
	"jarvis/internal/sidecar"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Multiplexer-specific messages ──────────────────────────────────────

type previewConnectedMsg struct{ sessionID string }

type sessionAttachedMsg struct{ sessionID string }

type sessionAttachFailedMsg struct{ err error }

// ── Multiplexer model ──────────────────────────────────────────────────

// Multiplexer is the root Bubble Tea model that composes all TUI components.
// It is a VALUE type (consistent with Bubble Tea conventions), but all
// sub-components are pointers so mutations persist through the model.
type Multiplexer struct {
	sidebar   *Sidebar
	termPane  *TermPane
	statusBar *StatusBar
	focus     *FocusManager
	cfg       *config.Config

	width        int
	height       int
	sidebarWidth int // default 24

	previewSessionID string // tracks which session is being previewed
}

// NewMultiplexer creates a fresh multiplexer with all sub-components initialised.
func NewMultiplexer(cfg *config.Config) Multiplexer {
	const defaultSidebarWidth = 24
	const defaultTermCols = 80
	const defaultTermRows = 24

	return Multiplexer{
		sidebar:      NewSidebar(cfg, defaultSidebarWidth, defaultTermRows),
		termPane:     NewTermPane(defaultTermCols, defaultTermRows),
		statusBar:    NewStatusBar(defaultSidebarWidth + defaultTermCols),
		focus:        NewFocusManager(),
		cfg:          cfg,
		sidebarWidth: defaultSidebarWidth,
	}
}

// SetProgram passes the Bubble Tea program reference to the TermPane so
// background goroutines can send messages to the main Update loop.
func (m *Multiplexer) SetProgram(p *tea.Program) {
	m.termPane.SetProgram(p)
}

// ── Bubble Tea lifecycle ───────────────────────────────────────────────

// Init is called once when the program starts. It kicks off the first
// data load, the periodic refresh timer, and the status poll timer.
func (m Multiplexer) Init() tea.Cmd {
	return tea.Batch(m.sidebar.RefreshItems(), tickEvery(), redrawTick())
}

// redrawTick triggers a re-render so the term pane shows new output (10fps).
func redrawTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return termPaneRedrawMsg{}
	})
}

// Update is the main event handler.
func (m Multiplexer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.handleWindowSize(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tickMsg:
		// Single refresh timer — don't duplicate with statusPollMsg.
		return m, tea.Batch(m.sidebar.RefreshItems(), tickEvery())

	case refreshMsg:
		m.sidebar.HandleRefresh(msg.items)
		m.updateStatusBar()
		return m, nil

	case attachMsg:
		// Session created via sidebar command — attach to it.
		return m, m.attachToSession(msg.sessionID)

	case sessionAttachedMsg:
		m.focus.SetActiveSession(true)
		m.focus.SetFocus(FocusTermPane)
		m.sidebar.SetFocused(false)
		m.previewSessionID = msg.sessionID
		m.updateStatusBar()
		// Hide sidebar, give full width to term pane for clean copy-paste.
		bodyHeight := m.height - 1
		if bodyHeight < 1 {
			bodyHeight = 1
		}
		m.termPane.Resize(m.width, bodyHeight)
		return m, redrawTick()

	case termPaneRedrawMsg:
		// Only re-render if there's pending data. Otherwise just reschedule
		// the tick without triggering View() (return same model).
		if m.termPane.HasPendingData() {
			return m, redrawTick()
		}
		// No pending data — reschedule but skip the render by returning
		// a cmd directly (Bubble Tea won't call View if model unchanged).
		return m, redrawTick()

	case sidecarEndedMsg:
		log.Printf("mux: session %s ended (exit %d)", msg.sessionID, msg.exitCode)
		m.termPane.Disconnect() // clean up dead connection so re-attach works
		m.focus.SetActiveSession(false)
		m.focus.SetFocus(FocusSidebar)
		m.sidebar.SetFocused(true)
		return m, m.sidebar.RefreshItems()

	case sessionAttachFailedMsg:
		if msg.err != nil {
			m.statusBar.SetSession("ATTACH FAILED", msg.err.Error())
		}
		return m, m.sidebar.RefreshItems()

	case previewConnectedMsg:
		m.previewSessionID = msg.sessionID
		m.updateStatusBar()
		return m, nil

	case emulatorResponseMsg:
		// Emulator generates responses to terminal queries (DA2, etc.)
		// but forwarding them to Claude Code can cause text to leak into
		// the input box. Claude Code works fine without them — just drop.
		return m, nil

	case statusMsgClear:
		return m, nil
	}

	return m, nil
}

// ── Window size ────────────────────────────────────────────────────────

func (m Multiplexer) handleWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.width = msg.Width
	m.height = msg.Height

	// Status bar takes 1 row, border column takes 1 char.
	bodyHeight := m.height - 1
	if bodyHeight < 1 {
		bodyHeight = 1
	}

	m.sidebar.SetSize(m.sidebarWidth, bodyHeight)
	m.statusBar.SetWidth(m.width)

	// When term pane is focused, it gets the full width (sidebar hidden).
	if m.focus.Current() == FocusTermPane {
		m.termPane.Resize(m.width, bodyHeight)
	} else {
		termWidth := m.width - m.sidebarWidth - 1 // 1 for border column
		if termWidth < 1 {
			termWidth = 1
		}
		m.termPane.Resize(termWidth, bodyHeight)
	}

	return m, nil
}

// ── Key handling ───────────────────────────────────────────────────────

func (m Multiplexer) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global: Toggle focus between sidebar and term pane.
	// Ctrl+\ (same as old jarvis detach key) or Alt+S (if terminal has Meta enabled).
	// NOT Esc — Claude Code uses Esc to cancel operations.
	if msg.Type == tea.KeyCtrlBackslash || msg.String() == "alt+s" {
		return m.handleToggleFocus()
	}

	// Global: q and ctrl+c quit (only when sidebar is focused and in normal mode).
	if m.focus.Current() == FocusSidebar && m.sidebar.mode == ModeDashboard {
		switch msg.String() {
		case "q", "ctrl+c":
			m.sidebar.SaveState()
			return m, tea.Quit
		}
	}

	switch m.focus.Current() {
	case FocusSidebar:
		return m.handleSidebarKey(msg)
	case FocusTermPane:
		return m.handleTermPaneKey(msg)
	}

	return m, nil
}

func (m Multiplexer) handleToggleFocus() (tea.Model, tea.Cmd) {
	bodyHeight := m.height - 1
	if bodyHeight < 1 {
		bodyHeight = 1
	}

	switch m.focus.Current() {
	case FocusTermPane:
		// Detach from session but keep connection alive for quick re-attach.
		m.termPane.Detach()
		// Show sidebar again, resize term pane to share width.
		m.focus.SetFocus(FocusSidebar)
		m.sidebar.SetFocused(true)
		termWidth := m.width - m.sidebarWidth - 1
		if termWidth < 1 {
			termWidth = 1
		}
		m.termPane.Resize(termWidth, bodyHeight)
		return m, nil

	case FocusSidebar:
		if m.termPane.IsConnected() && m.termPane.SessionID() != "" {
			// Hide sidebar, give full width to term pane.
			m.termPane.Resize(m.width, bodyHeight)
			return m, m.reattachToSession()
		}
		return m, nil
	}
	return m, nil
}

func (m Multiplexer) reattachToSession() tea.Cmd {
	termPane := m.termPane
	return func() tea.Msg {
		if err := termPane.Attach(); err != nil {
			return sessionAttachFailedMsg{err: err}
		}
		return sessionAttachedMsg{sessionID: termPane.SessionID()}
	}
}

func (m Multiplexer) handleSidebarKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cmd, attachSessionID := m.sidebar.Update(msg)
	if attachSessionID != "" {
		attachCmd := m.attachToSession(attachSessionID)
		if cmd != nil {
			return m, tea.Batch(cmd, attachCmd)
		}
		return m, attachCmd
	}

	// Update status bar on navigation (no preview connection — too slow
	// with many sessions). Preview connects only on Enter (attach).
	switch msg.String() {
	case "up", "down", "j", "k":
		m.updateStatusBar()
	}

	return m, cmd
}

func (m Multiplexer) handleTermPaneKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Viewport scroll: Shift+Up/Down scrolls 3 lines, Ctrl+Up/Down scrolls half page.
	// PageUp/PageDown also work if available.
	switch msg.String() {
	case "shift+up":
		m.termPane.ScrollUp(3)
		return m, nil
	case "shift+down":
		m.termPane.ScrollDown(3)
		return m, nil
	case "ctrl+up":
		m.termPane.ScrollUp(m.height / 2)
		return m, nil
	case "ctrl+down":
		m.termPane.ScrollDown(m.height / 2)
		return m, nil
	}
	switch msg.Type {
	case tea.KeyPgUp:
		m.termPane.ScrollUp(m.height / 2)
		return m, nil
	case tea.KeyPgDown:
		m.termPane.ScrollDown(m.height / 2)
		return m, nil
	}

	// Escape returns to live view when scrolled up.
	if msg.Type == tea.KeyEscape && m.termPane.IsScrolledUp() {
		m.termPane.ScrollToBottom()
		return m, nil
	}

	// Any other typing auto-scrolls to bottom (live view).
	if m.termPane.IsScrolledUp() {
		m.termPane.ScrollToBottom()
	}

	raw := keyToBytes(msg)
	if raw != "" {
		m.termPane.SendInput(raw)
	}
	return m, tea.Tick(50*time.Millisecond, func(t time.Time) tea.Msg {
		return termPaneRedrawMsg{}
	})
}

// ── Session attachment ─────────────────────────────────────────────────

// attachToSession returns a tea.Cmd that connects and attaches to a session.
// If the sidecar is dead, it resumes the session first (restarts the sidecar).
// IMPORTANT: This is async, so it must NOT mutate the Multiplexer value type.
func (m Multiplexer) attachToSession(sessionID string) tea.Cmd {
	termPane := m.termPane
	mgr := m.sidebar.Manager()
	return func() tea.Msg {
		if termPane.SessionID() == sessionID && termPane.IsConnected() {
			if err := termPane.Attach(); err != nil {
				return sessionAttachFailedMsg{err: err}
			}
			return sessionAttachedMsg{sessionID: sessionID}
		}

		socketPath := sidecar.SocketPath(sessionID)

		// Check if sidecar is alive. If not, resume (restart) it.
		if !session.PingSidecar(socketPath) {
			if err := mgr.ResumeByID(sessionID); err != nil {
				log.Printf("mux: resume failed for %s: %v", sessionID, err)
				return sessionAttachFailedMsg{err: err}
			}
			socketPath = sidecar.SocketPath(sessionID)
		}

		if err := termPane.ConnectPreview(socketPath, sessionID); err != nil {
			return sessionAttachFailedMsg{err: err}
		}
		if err := termPane.Attach(); err != nil {
			return sessionAttachFailedMsg{err: err}
		}
		return sessionAttachedMsg{sessionID: sessionID}
	}
}

// ── Preview management ─────────────────────────────────────────────────

// maybeUpdatePreview checks if the sidebar selection changed and, if so,
// returns a cmd that connects a preview for the newly selected session.
func (m Multiplexer) maybeUpdatePreview() tea.Cmd {
	// Don't update preview while attached — that would disrupt the connection.
	if m.termPane.IsAttached() {
		return nil
	}

	item := m.sidebar.SelectedItem()
	if item == nil || !item.IsSession() {
		return nil
	}

	if item.ID == m.previewSessionID {
		return nil
	}

	sessionID := item.ID
	termPane := m.termPane
	return func() tea.Msg {
		socketPath := sidecar.SocketPath(sessionID)
		if err := termPane.ConnectPreview(socketPath, sessionID); err != nil {
			// Preview connection failed — not fatal, just skip.
			return previewConnectedMsg{sessionID: ""}
		}
		return previewConnectedMsg{sessionID: sessionID}
	}
}

// ── Status bar updates ─────────────────────────────────────────────────

// updateStatusBar reads the sidebar selection and updates the status bar.
func (m Multiplexer) updateStatusBar() {
	item := m.sidebar.SelectedItem()
	if item == nil || !item.IsSession() {
		m.statusBar.ClearSession()
		return
	}

	state := string(item.State)
	if state == "" {
		state = string(item.Status)
	}
	m.statusBar.SetSession(item.Name, state)
}

// ── View ───────────────────────────────────────────────────────────────

// View composes the layout:
//
//	+----------+------------------------+
//	| Sidebar  | TermPane               |
//	|          |                        |
//	+----------+------------------------+
//	| StatusBar                         |
//	+-----------------------------------+
func (m Multiplexer) View() string {
	sidebarFocused := m.focus.Current() == FocusSidebar
	m.sidebar.SetFocused(sidebarFocused)

	// Calculate body height (total height minus status bar).
	bodyHeight := m.height - 1
	if bodyHeight < 1 {
		bodyHeight = 1
	}

	termView := m.termPane.View()

	// When term pane is focused, hide sidebar for full-width display
	// (allows clean text selection / copy-paste).
	if !sidebarFocused {
		return lipgloss.JoinVertical(lipgloss.Left, termView, m.statusBar.View())
	}

	sidebarView := m.sidebar.View()

	// Border column between sidebar and term pane.
	borderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("99"))
	var borderLines []string
	for i := 0; i < bodyHeight; i++ {
		borderLines = append(borderLines, borderStyle.Render("\u2502"))
	}
	border := strings.Join(borderLines, "\n")

	body := lipgloss.JoinHorizontal(lipgloss.Top, sidebarView, border, termView)
	return lipgloss.JoinVertical(lipgloss.Left, body, m.statusBar.View())
}

// ── keyToBytes ─────────────────────────────────────────────────────────

// keyToBytes converts a Bubble Tea KeyMsg to raw bytes suitable for sending
// to a sidecar's PTY. It handles runes, control characters, arrow keys,
// function keys, and other special keys.
func keyToBytes(msg tea.KeyMsg) string {
	switch msg.Type {
	// ── Control keys (Ctrl+A through Ctrl+Z) ──
	case tea.KeyCtrlA:
		return "\x01"
	case tea.KeyCtrlB:
		return "\x02"
	case tea.KeyCtrlC:
		return "\x03"
	case tea.KeyCtrlD:
		return "\x04"
	case tea.KeyCtrlE:
		return "\x05"
	case tea.KeyCtrlF:
		return "\x06"
	case tea.KeyCtrlG:
		return "\x07"
	case tea.KeyCtrlH:
		return "\x08"
	// KeyCtrlI == KeyTab, handled below.
	case tea.KeyCtrlJ:
		return "\x0a"
	case tea.KeyCtrlK:
		return "\x0b"
	case tea.KeyCtrlL:
		return "\x0c"
	// KeyCtrlM == KeyEnter, handled below.
	case tea.KeyCtrlN:
		return "\x0e"
	case tea.KeyCtrlO:
		return "\x0f"
	case tea.KeyCtrlP:
		return "\x10"
	case tea.KeyCtrlQ:
		return "\x11"
	case tea.KeyCtrlR:
		return "\x12"
	case tea.KeyCtrlS:
		return "\x13"
	case tea.KeyCtrlT:
		return "\x14"
	case tea.KeyCtrlU:
		return "\x15"
	case tea.KeyCtrlV:
		return "\x16"
	case tea.KeyCtrlW:
		return "\x17"
	case tea.KeyCtrlX:
		return "\x18"
	case tea.KeyCtrlY:
		return "\x19"
	case tea.KeyCtrlZ:
		return "\x1a"

	// ── Common keys ──
	case tea.KeyEnter:
		return "\r"
	case tea.KeyTab:
		return "\t"
	case tea.KeyBackspace:
		return "\x7f"
	case tea.KeyEscape:
		return "\x1b"
	case tea.KeySpace:
		return " "

	// ── Arrow keys ──
	case tea.KeyUp:
		return "\x1b[A"
	case tea.KeyDown:
		return "\x1b[B"
	case tea.KeyRight:
		return "\x1b[C"
	case tea.KeyLeft:
		return "\x1b[D"

	// ── Navigation keys ──
	case tea.KeyHome:
		return "\x1b[H"
	case tea.KeyEnd:
		return "\x1b[F"
	case tea.KeyPgUp:
		return "\x1b[5~"
	case tea.KeyPgDown:
		return "\x1b[6~"
	case tea.KeyDelete:
		return "\x1b[3~"
	case tea.KeyInsert:
		return "\x1b[2~"

	// ── Function keys ──
	case tea.KeyF1:
		return "\x1bOP"
	case tea.KeyF2:
		return "\x1bOQ"
	case tea.KeyF3:
		return "\x1bOR"
	case tea.KeyF4:
		return "\x1bOS"
	case tea.KeyF5:
		return "\x1b[15~"
	case tea.KeyF6:
		return "\x1b[17~"
	case tea.KeyF7:
		return "\x1b[18~"
	case tea.KeyF8:
		return "\x1b[19~"
	case tea.KeyF9:
		return "\x1b[20~"
	case tea.KeyF10:
		return "\x1b[21~"
	case tea.KeyF11:
		return "\x1b[23~"
	case tea.KeyF12:
		return "\x1b[24~"

	// ── Shift+Tab ──
	case tea.KeyShiftTab:
		return "\x1b[Z"

	// ── Runes (normal characters) ──
	case tea.KeyRunes:
		return string(msg.Runes)
	}

	return ""
}


