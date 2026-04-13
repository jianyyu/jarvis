package tui

// multiplexer.go — Root tea.Model that composes Sidebar, TermPane, StatusBar,
// and FocusManager into the multiplexer TUI. This is the main entry point
// that replaces the old full-screen Dashboard for interactive use.

import (
	"strings"
	"time"

	"jarvis/internal/config"
	"jarvis/internal/sidecar"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Multiplexer-specific messages ──────────────────────────────────────

type statusPollMsg struct{}

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

// ── Bubble Tea lifecycle ───────────────────────────────────────────────

// Init is called once when the program starts. It kicks off the first
// data load, the periodic refresh timer, and the status poll timer.
func (m Multiplexer) Init() tea.Cmd {
	return tea.Batch(m.sidebar.RefreshItems(), tickEvery(), statusPollEvery())
}

func statusPollEvery() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return statusPollMsg{}
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

	case statusPollMsg:
		// Status updates piggyback on the tick refresh. Just reschedule.
		return m, statusPollEvery()

	case attachMsg:
		// Session created via sidebar command — attach to it.
		return m, m.attachToSession(msg.sessionID)

	case sessionAttachedMsg:
		m.focus.SetActiveSession(true)
		m.focus.SetFocus(FocusTermPane)
		m.sidebar.SetFocused(false)
		m.previewSessionID = msg.sessionID
		m.updateStatusBar()
		return m, nil

	case sessionAttachFailedMsg:
		return m, m.sidebar.RefreshItems()

	case previewConnectedMsg:
		m.previewSessionID = msg.sessionID
		m.updateStatusBar()
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

	termWidth := m.width - m.sidebarWidth - 1 // 1 for border column
	if termWidth < 1 {
		termWidth = 1
	}

	m.sidebar.SetSize(m.sidebarWidth, bodyHeight)
	m.termPane.Resize(termWidth, bodyHeight)
	m.statusBar.SetWidth(m.width)

	return m, nil
}

// ── Key handling ───────────────────────────────────────────────────────

func (m Multiplexer) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global: Option+S toggles focus between sidebar and term pane.
	if msg.String() == "alt+s" {
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
	switch m.focus.Current() {
	case FocusTermPane:
		// Detach from session and return to sidebar.
		m.termPane.Detach()
		m.focus.SetActiveSession(false)
		m.focus.SetFocus(FocusSidebar)
		m.sidebar.SetFocused(true)
		return m, nil

	case FocusSidebar:
		if m.focus.HasActiveSession() {
			// Re-attach to existing session.
			return m, m.reattachToSession()
		}
		// If no active session but we have a preview, attach to it.
		if m.termPane.IsConnected() && m.termPane.SessionID() != "" {
			return m, m.attachToSession(m.termPane.SessionID())
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
		// User pressed Enter on a session — attach to it.
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
	raw := keyToBytes(msg)
	if raw != "" {
		m.termPane.SendInput(raw)
	}
	return m, nil
}

// ── Session attachment ─────────────────────────────────────────────────

// attachToSession returns a tea.Cmd that connects and attaches to a session.
// IMPORTANT: This is async, so it must NOT mutate the Multiplexer value type.
// It captures the termPane pointer (safe because it's pointer-based).
func (m Multiplexer) attachToSession(sessionID string) tea.Cmd {
	termPane := m.termPane
	return func() tea.Msg {
		socketPath := sidecar.SocketPath(sessionID)

		if termPane.SessionID() == sessionID {
			// Already connected to this session — just attach.
			if err := termPane.Attach(); err != nil {
				return sessionAttachFailedMsg{err: err}
			}
		} else {
			// Connect to a new session.
			if err := termPane.ConnectPreview(socketPath, sessionID); err != nil {
				return sessionAttachFailedMsg{err: err}
			}
			if err := termPane.Attach(); err != nil {
				return sessionAttachFailedMsg{err: err}
			}
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

	// Render sidebar.
	sidebarView := lipgloss.NewStyle().
		Width(m.sidebarWidth).
		Height(bodyHeight).
		Render(m.sidebar.View())

	// Render border column.
	borderColor := "240" // dim when term pane is focused
	if sidebarFocused {
		borderColor = "99" // purple when sidebar is focused
	}
	borderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(borderColor))

	var borderLines []string
	for i := 0; i < bodyHeight; i++ {
		borderLines = append(borderLines, borderStyle.Render("\u2502"))
	}
	border := strings.Join(borderLines, "\n")

	// Render term pane.
	termWidth := m.width - m.sidebarWidth - 1
	if termWidth < 1 {
		termWidth = 1
	}
	termView := lipgloss.NewStyle().
		Width(termWidth).
		Height(bodyHeight).
		Render(m.termPane.View())

	// Compose horizontally: sidebar | border | termpane
	body := lipgloss.JoinHorizontal(lipgloss.Top, sidebarView, border, termView)

	// Compose vertically: body over status bar
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
