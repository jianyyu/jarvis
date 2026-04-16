# Multiplexer TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the dashboard + raw-PTY-attach loop with an always-on multiplexer TUI that embeds session output via a virtual terminal emulator, with a persistent sidebar for session navigation.

**Architecture:** Bubble Tea runs continuously. A VT emulator (`charmbracelet/x/vt.SafeEmulator`) renders session output in the main pane. The sidebar (refactored from the existing dashboard) shows the session tree. Focus toggles between sidebar and main pane via Option+S. All sidecar code and the socket protocol are unchanged.

**Tech Stack:** Go, Bubble Tea v1.3.10, Lipgloss v1.1.0, charmbracelet/x/vt (SafeEmulator), existing sidecar Unix socket protocol.

**Spec:** `docs/superpowers/specs/2026-04-13-multiplexer-tui-design.md`

---

### Task 1: Add charmbracelet/x/vt dependency

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

**Note:** This machine (arca) may not have outbound network access. If `go get` fails, use a machine with access or configure GOPROXY. The remaining tasks use a thin `Terminal` interface so development can proceed with a stub implementation until the real dependency is available.

- [ ] **Step 1: Add the vt package**

```bash
go get github.com/charmbracelet/x/vt@latest
```

- [ ] **Step 2: Verify it compiles**

Create a throwaway file to verify:

```go
// internal/tui/vt_check_test.go
package tui

import (
	"testing"

	"github.com/charmbracelet/x/vt"
)

func TestVTEmulatorExists(t *testing.T) {
	em := vt.NewSafeEmulator(80, 24)
	defer em.Close()
	em.WriteString("hello")
	if em.String() == "" {
		t.Fatal("expected non-empty screen")
	}
}
```

Run: `go test ./internal/tui/ -run TestVTEmulatorExists -v`
Expected: PASS

- [ ] **Step 3: Remove throwaway test**

Delete `internal/tui/vt_check_test.go` — it was just to verify the dependency.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add charmbracelet/x/vt virtual terminal emulator"
```

---

### Task 2: Create FocusManager

**Files:**
- Create: `internal/tui/focus.go`
- Create: `internal/tui/focus_test.go`

The FocusManager is a simple state machine that tracks which component has focus and routes key events accordingly.

- [ ] **Step 1: Write the failing test**

```go
// internal/tui/focus_test.go
package tui

import "testing"

func TestFocusManager_InitialState(t *testing.T) {
	fm := NewFocusManager()
	if fm.Current() != FocusSidebar {
		t.Errorf("expected initial focus on sidebar, got %v", fm.Current())
	}
}

func TestFocusManager_Toggle(t *testing.T) {
	fm := NewFocusManager()
	fm.Toggle()
	if fm.Current() != FocusTermPane {
		t.Errorf("expected termpane focus after toggle, got %v", fm.Current())
	}
	fm.Toggle()
	if fm.Current() != FocusSidebar {
		t.Errorf("expected sidebar focus after second toggle, got %v", fm.Current())
	}
}

func TestFocusManager_FocusSidebar(t *testing.T) {
	fm := NewFocusManager()
	fm.SetFocus(FocusTermPane)
	fm.SetFocus(FocusSidebar)
	if fm.Current() != FocusSidebar {
		t.Errorf("expected sidebar, got %v", fm.Current())
	}
}

func TestFocusManager_CannotFocusTermPaneWithoutSession(t *testing.T) {
	fm := NewFocusManager()
	fm.SetFocus(FocusTermPane)
	// Without a session attached, focus should stay on sidebar
	// (TermPane focus only allowed when hasActiveSession is true)
	if fm.HasActiveSession() {
		t.Error("expected no active session")
	}
}
```

Run: `go test ./internal/tui/ -run TestFocusManager -v`
Expected: FAIL (types not defined)

- [ ] **Step 2: Implement FocusManager**

```go
// internal/tui/focus.go
package tui

// FocusTarget identifies which component has keyboard focus.
type FocusTarget int

const (
	FocusSidebar  FocusTarget = iota
	FocusTermPane
)

// FocusManager tracks which component receives keystrokes.
type FocusManager struct {
	current          FocusTarget
	hasActiveSession bool
}

func NewFocusManager() *FocusManager {
	return &FocusManager{current: FocusSidebar}
}

func (fm *FocusManager) Current() FocusTarget { return fm.current }

func (fm *FocusManager) HasActiveSession() bool { return fm.hasActiveSession }

func (fm *FocusManager) SetActiveSession(active bool) {
	fm.hasActiveSession = active
	if !active && fm.current == FocusTermPane {
		fm.current = FocusSidebar
	}
}

func (fm *FocusManager) SetFocus(target FocusTarget) {
	if target == FocusTermPane && !fm.hasActiveSession {
		return
	}
	fm.current = target
}

func (fm *FocusManager) Toggle() {
	if fm.current == FocusSidebar && fm.hasActiveSession {
		fm.current = FocusTermPane
	} else {
		fm.current = FocusSidebar
	}
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/tui/ -run TestFocusManager -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/tui/focus.go internal/tui/focus_test.go
git commit -m "feat: add FocusManager for sidebar/termpane focus routing"
```

---

### Task 3: Create StatusBar component

**Files:**
- Create: `internal/tui/statusbar.go`
- Create: `internal/tui/statusbar_test.go`

A simple component that renders a single-line status bar showing active session name, state, and keybind hints.

- [ ] **Step 1: Write the failing test**

```go
// internal/tui/statusbar_test.go
package tui

import "testing"

func TestStatusBar_EmptyState(t *testing.T) {
	sb := NewStatusBar(80)
	view := sb.View()
	if view == "" {
		t.Error("expected non-empty view even with no session")
	}
}

func TestStatusBar_WithSession(t *testing.T) {
	sb := NewStatusBar(80)
	sb.SetSession("auth-fix", "working")
	view := sb.View()
	if view == "" {
		t.Error("expected non-empty view")
	}
	// Should contain session name
	if !containsStr(view, "auth-fix") {
		t.Errorf("expected view to contain 'auth-fix', got: %s", stripAnsi(view))
	}
}

func TestStatusBar_Resize(t *testing.T) {
	sb := NewStatusBar(40)
	sb.SetWidth(120)
	view := sb.View()
	if view == "" {
		t.Error("expected non-empty view after resize")
	}
}

// containsStr checks for a substring, ignoring ANSI escapes.
func containsStr(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && 
		(strings.Contains(s, substr) || strings.Contains(stripAnsi(s), substr))
}
```

Note: Add `"strings"` and a `stripAnsi` helper to the test file. `stripAnsi` can use a simple regex: `regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(s, "")`.

Run: `go test ./internal/tui/ -run TestStatusBar -v`
Expected: FAIL (types not defined)

- [ ] **Step 2: Implement StatusBar**

```go
// internal/tui/statusbar.go
package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

var statusBarBg = lipgloss.NewStyle().
	Background(lipgloss.Color("236")).
	Foreground(lipgloss.Color("252"))

var statusBarHint = lipgloss.NewStyle().
	Background(lipgloss.Color("236")).
	Foreground(lipgloss.Color("241"))

// StatusBar renders a single-line bar at the bottom of the multiplexer.
type StatusBar struct {
	width       int
	sessionName string
	sessionState string
}

func NewStatusBar(width int) *StatusBar {
	return &StatusBar{width: width}
}

func (sb *StatusBar) SetWidth(w int) { sb.width = w }

func (sb *StatusBar) SetSession(name, state string) {
	sb.sessionName = name
	sb.sessionState = state
}

func (sb *StatusBar) ClearSession() {
	sb.sessionName = ""
	sb.sessionState = ""
}

func (sb *StatusBar) View() string {
	var left string
	if sb.sessionName != "" {
		left = fmt.Sprintf(" ● %s │ %s", sb.sessionName, sb.sessionState)
	} else {
		left = " No session selected"
	}
	hint := "Option+S: sidebar "
	
	leftRendered := statusBarBg.Render(left)
	hintRendered := statusBarHint.Render(hint)

	// Pad the middle to fill width
	gap := sb.width - lipgloss.Width(leftRendered) - lipgloss.Width(hintRendered)
	if gap < 0 {
		gap = 0
	}
	padding := statusBarBg.Render(fmt.Sprintf("%*s", gap, ""))

	return leftRendered + padding + hintRendered
}
```

- [ ] **Step 3: Add stripAnsi helper and imports to test file**

Update `statusbar_test.go` to add:

```go
import (
	"regexp"
	"strings"
	"testing"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripAnsi(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/tui/ -run TestStatusBar -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tui/statusbar.go internal/tui/statusbar_test.go
git commit -m "feat: add StatusBar component for multiplexer bottom bar"
```

---

### Task 4: Create TermPane component

**Files:**
- Create: `internal/tui/termpane.go`
- Create: `internal/tui/termpane_test.go`

This is the core new component. It wraps a VT emulator and a sidecar socket connection. It has two modes: preview (read-only output streaming) and attached (full interactive).

- [ ] **Step 1: Write the failing test for TermPane creation and basic rendering**

```go
// internal/tui/termpane_test.go
package tui

import "testing"

func TestTermPane_New(t *testing.T) {
	tp := NewTermPane(80, 24)
	if tp == nil {
		t.Fatal("expected non-nil TermPane")
	}
	if tp.IsAttached() {
		t.Error("new TermPane should not be attached")
	}
	if tp.IsConnected() {
		t.Error("new TermPane should not be connected")
	}
}

func TestTermPane_EmptyView(t *testing.T) {
	tp := NewTermPane(80, 24)
	view := tp.View()
	// Empty pane should show placeholder
	if view == "" {
		t.Error("expected non-empty view for disconnected pane")
	}
}

func TestTermPane_WriteAndRender(t *testing.T) {
	tp := NewTermPane(80, 24)
	// Simulate receiving output (as if from sidecar)
	tp.WriteOutput([]byte("Hello, world!"))
	view := tp.View()
	stripped := stripAnsi(view)
	if !strings.Contains(stripped, "Hello, world!") {
		t.Errorf("expected 'Hello, world!' in view, got: %q", stripped)
	}
}

func TestTermPane_Resize(t *testing.T) {
	tp := NewTermPane(80, 24)
	tp.WriteOutput([]byte("test"))
	tp.Resize(120, 40)
	// Should not panic or error
	view := tp.View()
	if view == "" {
		t.Error("expected non-empty view after resize")
	}
}
```

Run: `go test ./internal/tui/ -run TestTermPane -v`
Expected: FAIL (types not defined)

- [ ] **Step 2: Implement TermPane core**

```go
// internal/tui/termpane.go
package tui

import (
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"jarvis/internal/protocol"

	"github.com/charmbracelet/x/vt"
)

// TermPane renders a Claude Code session's output via a VT emulator.
// It connects to a sidecar socket for I/O streaming.
type TermPane struct {
	mu       sync.Mutex
	emulator *vt.SafeEmulator
	cols     int
	rows     int

	// Sidecar connection
	conn     net.Conn
	codec    *protocol.Codec
	attached bool // true = fully attached (interactive), false = preview only

	// Session tracking
	sessionID string

	// Output streaming
	stopCh chan struct{} // closed to stop output goroutine
}

func NewTermPane(cols, rows int) *TermPane {
	em := vt.NewSafeEmulator(cols, rows)
	return &TermPane{
		emulator: em,
		cols:     cols,
		rows:     rows,
	}
}

func (tp *TermPane) IsAttached() bool {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.attached
}

func (tp *TermPane) IsConnected() bool {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.conn != nil
}

func (tp *TermPane) SessionID() string {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.sessionID
}

// WriteOutput feeds raw bytes into the VT emulator (for testing or direct use).
func (tp *TermPane) WriteOutput(data []byte) {
	tp.emulator.Write(data)
}

// View renders the VT emulator's screen buffer as a string.
func (tp *TermPane) View() string {
	tp.mu.Lock()
	connected := tp.conn != nil
	tp.mu.Unlock()

	if !connected && tp.sessionID == "" {
		return tp.emptyView()
	}

	return tp.emulator.Render()
}

func (tp *TermPane) emptyView() string {
	centerY := tp.rows / 2
	var out string
	for i := 0; i < tp.rows; i++ {
		if i == centerY-1 {
			line := "Select a session from the sidebar"
			pad := (tp.cols - len(line)) / 2
			if pad < 0 {
				pad = 0
			}
			out += fmt.Sprintf("%*s%s", pad, "", dimStyle.Render(line))
		} else if i == centerY {
			line := "or press 'n' to create one"
			pad := (tp.cols - len(line)) / 2
			if pad < 0 {
				pad = 0
			}
			out += fmt.Sprintf("%*s%s", pad, "", dimStyle.Render(line))
		}
		if i < tp.rows-1 {
			out += "\n"
		}
	}
	return out
}

// ConnectPreview connects to a session's sidecar in read-only mode.
// It streams output but does not send {Action: "attach"}.
func (tp *TermPane) ConnectPreview(socketPath, sessionID string) error {
	tp.Disconnect()

	conn, err := net.DialTimeout("unix", socketPath, 3*time.Second)
	if err != nil {
		return fmt.Errorf("connect preview: %w", err)
	}

	codec := protocol.NewCodec(conn)

	// Request ring buffer for catch-up
	if err := codec.Send(protocol.Request{Action: "get_buffer", Lines: 5000}); err != nil {
		conn.Close()
		return fmt.Errorf("get_buffer: %w", err)
	}

	var resp protocol.Response
	if err := codec.Receive(&resp); err != nil {
		conn.Close()
		return fmt.Errorf("get_buffer response: %w", err)
	}

	// Reset emulator and feed catch-up data
	tp.emulator.Close()
	tp.emulator = vt.NewSafeEmulator(tp.cols, tp.rows)

	if resp.Data != "" {
		data, err := base64.StdEncoding.DecodeString(resp.Data)
		if err == nil {
			tp.emulator.Write(data)
		}
	}

	// Send resize so sidecar knows our dimensions (even in preview mode)
	codec.Send(protocol.Request{Action: "resize", Cols: tp.cols, Rows: tp.rows})

	tp.mu.Lock()
	tp.conn = conn
	tp.codec = codec
	tp.sessionID = sessionID
	tp.attached = false
	tp.stopCh = make(chan struct{})
	tp.mu.Unlock()

	// Start output streaming goroutine
	go tp.streamOutput()

	return nil
}

// Attach transitions from preview to attached mode (interactive).
func (tp *TermPane) Attach() error {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	if tp.conn == nil {
		return fmt.Errorf("not connected")
	}
	if tp.attached {
		return nil // already attached
	}

	if err := tp.codec.Send(protocol.Request{Action: "attach"}); err != nil {
		return fmt.Errorf("attach: %w", err)
	}
	tp.attached = true
	return nil
}

// Detach transitions from attached to preview mode.
func (tp *TermPane) Detach() {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	if tp.conn != nil && tp.attached {
		tp.codec.Send(protocol.Request{Action: "detach"})
		tp.attached = false
	}
}

// Disconnect fully closes the connection to the sidecar.
func (tp *TermPane) Disconnect() {
	tp.mu.Lock()
	if tp.stopCh != nil {
		select {
		case <-tp.stopCh:
		default:
			close(tp.stopCh)
		}
	}
	if tp.conn != nil {
		if tp.attached {
			tp.codec.Send(protocol.Request{Action: "detach"})
		}
		tp.conn.Close()
		tp.conn = nil
		tp.codec = nil
	}
	tp.attached = false
	tp.sessionID = ""
	tp.mu.Unlock()

	// Reset emulator
	tp.emulator.Close()
	tp.emulator = vt.NewSafeEmulator(tp.cols, tp.rows)
}

// SendInput sends raw keystrokes to the sidecar (only works when attached).
func (tp *TermPane) SendInput(data string) {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	if tp.conn != nil && tp.attached {
		tp.codec.Send(protocol.Request{Action: "send_input", Text: data})
	}
}

// SendResize notifies the sidecar of a terminal size change.
func (tp *TermPane) SendResize(cols, rows int) {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	if tp.conn != nil {
		tp.codec.Send(protocol.Request{Action: "resize", Cols: cols, Rows: rows})
	}
}

// Resize changes the VT emulator dimensions and notifies the sidecar.
func (tp *TermPane) Resize(cols, rows int) {
	tp.cols = cols
	tp.rows = rows
	tp.emulator.Resize(cols, rows)
	tp.SendResize(cols, rows)
}

// Close releases all resources.
func (tp *TermPane) Close() {
	tp.Disconnect()
	tp.emulator.Close()
}

// streamOutput reads output events from the sidecar and writes them to the VT emulator.
func (tp *TermPane) streamOutput() {
	for {
		tp.mu.Lock()
		codec := tp.codec
		stopCh := tp.stopCh
		tp.mu.Unlock()

		if codec == nil {
			return
		}

		select {
		case <-stopCh:
			return
		default:
		}

		var resp protocol.Response
		if err := codec.Receive(&resp); err != nil {
			log.Printf("termpane: stream error: %v", err)
			return
		}

		switch resp.Event {
		case "output", "buffer":
			if resp.Data != "" {
				data, err := base64.StdEncoding.DecodeString(resp.Data)
				if err == nil {
					tp.emulator.Write(data)
				}
			}
		case "session_ended":
			log.Printf("termpane: session ended (exit code %d)", resp.ExitCode)
			return
		}
	}
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/tui/ -run TestTermPane -v`
Expected: PASS (basic creation and rendering tests; sidecar connection tests will be integration-level)

- [ ] **Step 4: Commit**

```bash
git add internal/tui/termpane.go internal/tui/termpane_test.go
git commit -m "feat: add TermPane component with VT emulator and sidecar streaming"
```

---

### Task 5: Refactor Dashboard into Sidebar

**Files:**
- Create: `internal/tui/sidebar.go`
- Create: `internal/tui/sidebar_test.go`
- Modify: `internal/tui/view.go` (extract sidebar rendering)

The Sidebar reuses the existing dashboard logic (builder.go, item list, folder expand/collapse, search, commands) but renders into a fixed-width column instead of full screen. It implements a `tea.Model`-like interface with `Update()` and `View()`.

- [ ] **Step 1: Write the failing test**

```go
// internal/tui/sidebar_test.go
package tui

import (
	"testing"

	"jarvis/internal/config"
)

func TestSidebar_New(t *testing.T) {
	cfg := &config.Config{}
	sb := NewSidebar(cfg, 24, 40)
	if sb == nil {
		t.Fatal("expected non-nil sidebar")
	}
}

func TestSidebar_View(t *testing.T) {
	cfg := &config.Config{}
	sb := NewSidebar(cfg, 24, 40)
	view := sb.View()
	if view == "" {
		t.Error("expected non-empty sidebar view")
	}
}

func TestSidebar_Resize(t *testing.T) {
	cfg := &config.Config{}
	sb := NewSidebar(cfg, 24, 40)
	sb.SetSize(30, 50)
	view := sb.View()
	if view == "" {
		t.Error("expected non-empty view after resize")
	}
}

func TestSidebar_SelectedItem(t *testing.T) {
	cfg := &config.Config{}
	sb := NewSidebar(cfg, 24, 40)
	// With no sessions, selected item should be nil
	item := sb.SelectedItem()
	// This is OK — empty state
	_ = item
}
```

Run: `go test ./internal/tui/ -run TestSidebar -v`
Expected: FAIL (Sidebar type not defined)

- [ ] **Step 2: Implement Sidebar**

The Sidebar extracts the core logic from `dashboard.go` — item list, cursor, scrolling, expand/collapse, search, input modes — but renders into a fixed width column. The key difference is `View()` renders with `lipgloss.NewStyle().Width(sb.width)` constraint.

```go
// internal/tui/sidebar.go
package tui

import (
	"fmt"
	"strings"
	"time"

	"jarvis/internal/config"
	"jarvis/internal/model"
	"jarvis/internal/session"
	"jarvis/internal/ui"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Sidebar displays the session tree in a fixed-width column.
// It reuses builder.go for constructing the item list and commands.go
// for async operations.
type Sidebar struct {
	items  []ListItem
	cursor int
	mode   Mode
	width  int
	height int
	cfg    *config.Config
	mgr    *session.Manager

	expandState  map[string]bool
	scrollOffset int

	searchInput textinput.Model
	searchQuery string

	cmdInput    textinput.Model
	cmdPrompt   string
	cmdCallback func(string) tea.Cmd

	statusMsg string
	focused   bool
}

func NewSidebar(cfg *config.Config, width, height int) *Sidebar {
	si := textinput.New()
	si.Placeholder = "search..."
	si.CharLimit = 100

	ci := textinput.New()
	ci.CharLimit = 200

	session.RecoverAllSessions()

	expandState := map[string]bool{"__done__": false}
	cursor := 0
	scrollOffset := 0

	if saved := loadState(); saved != nil {
		if saved.ExpandState != nil {
			expandState = saved.ExpandState
		}
		cursor = saved.Cursor
		scrollOffset = saved.ScrollOffset
	}

	return &Sidebar{
		cfg:          cfg,
		mgr:          session.NewManager(cfg),
		expandState:  expandState,
		cursor:       cursor,
		scrollOffset: scrollOffset,
		searchInput:  si,
		cmdInput:     ci,
		mode:         ModeDashboard,
		width:        width,
		height:       height,
		focused:      true,
	}
}

func (s *Sidebar) SetSize(width, height int) {
	s.width = width
	s.height = height
}

func (s *Sidebar) SetFocused(focused bool) { s.focused = focused }
func (s *Sidebar) IsFocused() bool          { return s.focused }
func (s *Sidebar) Manager() *session.Manager { return s.mgr }

// SelectedItem returns the item under the cursor, or nil.
func (s *Sidebar) SelectedItem() *ListItem {
	visible := s.filteredItems()
	if s.cursor >= 0 && s.cursor < len(visible) {
		item := visible[s.cursor]
		return &item
	}
	return nil
}

// SaveState persists expand state and cursor to disk.
func (s *Sidebar) SaveState() {
	state := dashboardState{
		ExpandState:  s.expandState,
		Cursor:       s.cursor,
		ScrollOffset: s.scrollOffset,
	}
	data, _ := yaml.Marshal(&state)
	store.WriteAtomic(statePath(), data)
}

// RefreshItems rebuilds the item list from disk.
func (s *Sidebar) RefreshItems() tea.Cmd {
	return func() tea.Msg {
		return refreshMsg{items: buildItemList(s.mgr)}
	}
}

// HandleRefresh processes a refreshMsg, preserving cursor position.
func (s *Sidebar) HandleRefresh(items []ListItem) {
	var selectedID string
	if old := s.SelectedItem(); old != nil {
		selectedID = old.ID
	}

	s.items = items
	for i := range s.items {
		if s.items[i].IsFolder() {
			if expanded, exists := s.expandState[s.items[i].ID]; exists {
				s.items[i].Expanded = expanded
			}
		}
	}

	visible := s.filteredItems()
	if selectedID != "" {
		for i, item := range visible {
			if item.ID == selectedID {
				s.cursor = i
				break
			}
		}
	}
	if s.cursor >= len(visible) {
		s.cursor = max(0, len(visible)-1)
	}
	s.adjustScroll()
}

// Update handles keyboard input when the sidebar has focus.
// Returns (tea.Cmd, attachSessionID). attachSessionID is non-empty
// when the user presses Enter on a session.
func (s *Sidebar) Update(msg tea.KeyMsg) (tea.Cmd, string) {
	switch s.mode {
	case ModeSearch:
		return s.handleSearchKey(msg), ""
	case ModeInput:
		return s.handleInputKey(msg), ""
	default:
		return s.handleDashboardKey(msg)
	}
}

func (s *Sidebar) handleDashboardKey(msg tea.KeyMsg) (tea.Cmd, string) {
	visible := s.filteredItems()

	switch msg.String() {
	case "q", "ctrl+c":
		s.SaveState()
		return tea.Quit, ""

	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
			if s.cursor >= 0 && s.cursor < len(visible) && visible[s.cursor].ID == "__separator__" {
				if s.cursor > 0 {
					s.cursor--
				} else {
					s.cursor++
				}
			}
			s.adjustScroll()
		}
		return nil, ""

	case "down", "j":
		if s.cursor < len(visible)-1 {
			s.cursor++
			if s.cursor < len(visible) && visible[s.cursor].ID == "__separator__" {
				if s.cursor < len(visible)-1 {
					s.cursor++
				} else {
					s.cursor--
				}
			}
			s.adjustScroll()
		}
		return nil, ""

	case "enter":
		item := s.SelectedItem()
		if item == nil {
			break
		}
		if item.IsSession() && item.Status != "archived" {
			s.SaveState()
			return nil, item.ID
		}
		if item.IsFolder() {
			s.toggleFolder(item.ID)
			return s.RefreshItems(), ""
		}

	case "/":
		s.mode = ModeSearch
		s.searchInput.Focus()
		return textinput.Blink, ""

	case "n":
		parentID, parentName := s.resolveParentFolder()
		s.cmdPrompt = "Name: "
		if parentName != "" {
			s.cmdPrompt = fmt.Sprintf("In %s: ", parentName)
		}
		s.cmdInput.SetValue("")
		s.cmdInput.Focus()
		s.cmdCallback = func(name string) tea.Cmd {
			return s.createSession(name, parentID)
		}
		s.mode = ModeInput
		return textinput.Blink, ""

	case "f":
		parentID, parentName := s.resolveParentFolder()
		s.cmdPrompt = "Folder: "
		if parentName != "" {
			s.cmdPrompt = fmt.Sprintf("In %s: ", parentName)
		}
		s.cmdInput.SetValue("")
		s.cmdInput.Focus()
		s.cmdCallback = func(name string) tea.Cmd {
			return s.createFolder(name, parentID)
		}
		s.mode = ModeInput
		return textinput.Blink, ""

	case "c":
		parentID, _ := s.resolveParentFolder()
		return s.createChat(parentID), ""

	case "a":
		item := s.SelectedItem()
		if item != nil && item.IsSession() && item.State == model.StateWaitingForApproval {
			return s.quickApprove(item.ID), ""
		}

	case "d":
		item := s.SelectedItem()
		if item == nil {
			break
		}
		if item.IsSession() {
			return s.markDone(item.ID), ""
		}
		if item.IsFolder() && item.ID != "__done__" {
			return s.markFolderDone(item.ID), ""
		}

	case "r":
		item := s.SelectedItem()
		if item != nil {
			s.cmdPrompt = "Rename: "
			s.cmdInput.SetValue(item.Name)
			s.cmdInput.CursorEnd()
			s.cmdInput.Focus()
			itemID := item.ID
			isFolder := item.IsFolder()
			s.cmdCallback = func(name string) tea.Cmd {
				if isFolder {
					return s.renameFolder(itemID, name)
				}
				return s.renameSession(itemID, name)
			}
			s.mode = ModeInput
			return textinput.Blink, ""
		}

	case "x":
		item := s.SelectedItem()
		if item == nil || item.ID == "__done__" {
			break
		}
		if item.IsSession() {
			return s.deleteSession(item.ID, item.Name), ""
		}
		if item.IsFolder() {
			return s.deleteFolder(item.ID, item.Name), ""
		}

	case " ":
		item := s.SelectedItem()
		if item != nil && item.IsFolder() {
			s.toggleFolder(item.ID)
			return s.RefreshItems(), ""
		}

	case "ctrl+r":
		return s.RefreshItems(), ""
	}

	return nil, ""
}

func (s *Sidebar) handleSearchKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		s.mode = ModeDashboard
		s.searchQuery = ""
		s.searchInput.SetValue("")
		return s.RefreshItems()
	case "enter":
		s.searchQuery = s.searchInput.Value()
		s.mode = ModeDashboard
		return s.RefreshItems()
	}

	var cmd tea.Cmd
	s.searchInput, cmd = s.searchInput.Update(msg)
	s.searchQuery = s.searchInput.Value()
	return cmd
}

func (s *Sidebar) handleInputKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		s.mode = ModeDashboard
		return nil
	case "enter":
		val := s.cmdInput.Value()
		s.mode = ModeDashboard
		if val != "" && s.cmdCallback != nil {
			return s.cmdCallback(val)
		}
		return nil
	}

	var cmd tea.Cmd
	s.cmdInput, cmd = s.cmdInput.Update(msg)
	return cmd
}

// View renders the sidebar as a fixed-width column.
func (s *Sidebar) View() string {
	style := lipgloss.NewStyle().Width(s.width)

	var b strings.Builder

	// Header
	title := titleStyle.Render("SESSIONS")
	b.WriteString(style.Render(title))
	b.WriteString("\n")

	// Item list
	visibleItems := s.filteredItems()
	maxRows := s.height - 4 // header(1) + blank(1) + footer(2)
	if maxRows < 3 {
		maxRows = 3
	}

	if len(visibleItems) == 0 {
		b.WriteString(style.Render(dimStyle.Render("  No sessions.")))
		b.WriteString("\n")
	}

	end := s.scrollOffset + maxRows
	if end > len(visibleItems) {
		end = len(visibleItems)
	}
	start := s.scrollOffset
	if start < 0 {
		start = 0
	}

	for i := start; i < end; i++ {
		item := visibleItems[i]
		indent := strings.Repeat(" ", item.Depth*2)
		line := s.renderItem(item)

		if i == s.cursor {
			row := selectedStyle.Render("▌") + indent + line
			b.WriteString(style.Render(row))
		} else {
			row := " " + indent + line
			b.WriteString(style.Render(row))
		}
		b.WriteString("\n")
	}

	// Pad remaining rows
	rendered := strings.Count(b.String(), "\n")
	for rendered < s.height-2 {
		b.WriteString(style.Render(""))
		b.WriteString("\n")
		rendered++
	}

	// Footer
	switch s.mode {
	case ModeSearch:
		b.WriteString(style.Render(inputStyle.Render("/") + s.searchInput.View()))
	case ModeInput:
		b.WriteString(style.Render(inputStyle.Render(s.cmdPrompt) + s.cmdInput.View()))
	default:
		if s.focused {
			b.WriteString(style.Render(dimStyle.Render("[n]ew [f]older [/]search")))
		}
	}

	return b.String()
}

// renderItem produces a compact line for one item in the sidebar.
func (s *Sidebar) renderItem(item ListItem) string {
	if item.ID == "__separator__" {
		return dimStyle.Render(strings.Repeat("─", s.width-2))
	}

	if item.IsFolder() {
		arrow := "▶"
		if s.isExpanded(item.ID) {
			arrow = "▼"
		}
		name := folderStyle.Render(ui.Truncate(item.Name, s.width-8))
		progress := ""
		if item.TotalCount > 0 {
			progress = dimStyle.Render(fmt.Sprintf(" %d/%d", item.DoneCount, item.TotalCount))
		}
		return fmt.Sprintf("%s %s%s", arrow, name, progress)
	}

	// Session: icon + name + state indicator
	icon := sessionIcon(item.Status, item.State)
	nameWidth := s.width - 6 // icon(2) + indent + padding
	if nameWidth < 10 {
		nameWidth = 10
	}
	name := ui.Truncate(item.Name, nameWidth)

	return fmt.Sprintf("%s %s", icon, name)
}

// --- Delegate to existing helper functions ---
// These methods mirror the Dashboard methods but operate on Sidebar fields.

func (s *Sidebar) toggleFolder(id string) {
	current := s.expandState[id]
	s.expandState[id] = !current
}

func (s *Sidebar) isExpanded(id string) bool {
	if expanded, exists := s.expandState[id]; exists {
		return expanded
	}
	return false
}

func (s *Sidebar) adjustScroll() {
	maxRows := s.height - 4
	if maxRows < 3 {
		maxRows = 3
	}
	if s.cursor < s.scrollOffset {
		s.scrollOffset = s.cursor
	}
	if s.cursor >= s.scrollOffset+maxRows {
		s.scrollOffset = s.cursor - maxRows + 1
	}
	if s.scrollOffset < 0 {
		s.scrollOffset = 0
	}
}

func (s *Sidebar) filteredItems() []ListItem {
	if s.searchQuery != "" {
		query := strings.ToLower(s.searchQuery)
		var result []ListItem
		for _, item := range s.items {
			if strings.Contains(strings.ToLower(item.Name), query) {
				result = append(result, item)
			}
		}
		return result
	}

	var visible []ListItem
	skipDepth := -1
	for _, item := range s.items {
		if skipDepth >= 0 && item.Depth > skipDepth {
			continue
		}
		skipDepth = -1
		if item.IsFolder() && !s.isExpanded(item.ID) {
			skipDepth = item.Depth
		}
		visible = append(visible, item)
	}
	return visible
}

func (s *Sidebar) resolveParentFolder() (id string, name string) {
	item := s.SelectedItem()
	if item == nil {
		return "", ""
	}
	if item.IsFolder() && item.ID != "__done__" {
		return item.ID, item.Name
	}
	if item.ParentID != "" && item.ParentID != "__done__" {
		for _, i := range s.items {
			if i.ID == item.ParentID {
				return i.ID, i.Name
			}
		}
		return item.ParentID, ""
	}
	return "", ""
}
```

Note: The sidebar reuses the command methods from `commands.go`. These need to be adapted to work with the Sidebar struct instead of Dashboard. The simplest approach is to add equivalent methods on Sidebar that delegate to the same underlying store/session functions. The method signatures in commands.go (like `createSession`, `createFolder`, etc.) need Sidebar equivalents — extract the shared logic into standalone functions.

- [ ] **Step 3: Extract shared command logic from commands.go**

Modify `internal/tui/commands.go` to export the core logic as standalone functions that both Dashboard and Sidebar can call. Add methods on Sidebar that call these functions:

```go
// Add to sidebar.go — methods that delegate to commands.go logic
func (s *Sidebar) createSession(name, parentID string) tea.Cmd {
	// Same logic as Dashboard.createSession but using s.cfg and s.mgr
	return func() tea.Msg {
		cwd := s.cfg.RepoPath()
		if cwd == "" {
			cwd = "."
		}
		sess, err := s.mgr.Spawn(name, cwd, []string{"claude"})
		if err != nil {
			return refreshMsg{items: buildItemList(s.mgr)}
		}
		if parentID != "" {
			sess.ParentID = parentID
			store.SaveSession(sess)
			parent, err := store.GetFolder(parentID)
			if err == nil {
				parent.Children = append(parent.Children, model.ChildRef{Type: "session", ID: sess.ID})
				store.SaveFolder(parent)
			}
		}
		return attachMsg{sessionID: sess.ID}
	}
}
```

Repeat for `createChat`, `createFolder`, `renameSession`, `renameFolder`, `markDone`, `markFolderDone`, `deleteSession`, `deleteFolder`, `quickApprove`. These are copy-paste from commands.go with `d.cfg`→`s.cfg`, `d.mgr`→`s.mgr`.

- [ ] **Step 4: Add missing imports to sidebar.go**

Add imports for `gopkg.in/yaml.v3`, `jarvis/internal/store`, `jarvis/internal/model`.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/tui/ -run TestSidebar -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/tui/sidebar.go internal/tui/sidebar_test.go
git commit -m "feat: add Sidebar component (refactored from Dashboard)"
```

---

### Task 6: Create MultiplexerModel

**Files:**
- Create: `internal/tui/multiplexer.go`
- Create: `internal/tui/multiplexer_test.go`

The root `tea.Model` that composes Sidebar, TermPane, StatusBar, and FocusManager. Handles `Option+S` interception, routes key events by focus, manages session preview/attach lifecycle.

- [ ] **Step 1: Write the failing test**

```go
// internal/tui/multiplexer_test.go
package tui

import (
	"testing"

	"jarvis/internal/config"
	tea "github.com/charmbracelet/bubbletea"
)

func TestMultiplexer_New(t *testing.T) {
	cfg := &config.Config{}
	m := NewMultiplexer(cfg)
	if m.focus == nil {
		t.Fatal("expected focus manager")
	}
	if m.sidebar == nil {
		t.Fatal("expected sidebar")
	}
	if m.termPane == nil {
		t.Fatal("expected term pane")
	}
	if m.statusBar == nil {
		t.Fatal("expected status bar")
	}
}

func TestMultiplexer_View(t *testing.T) {
	cfg := &config.Config{}
	m := NewMultiplexer(cfg)
	// Simulate window size
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	view := newM.(Multiplexer).View()
	if view == "" {
		t.Error("expected non-empty view")
	}
}

func TestMultiplexer_OptionS_TogglesFocus(t *testing.T) {
	cfg := &config.Config{}
	m := NewMultiplexer(cfg)

	// Initial focus should be sidebar
	if m.focus.Current() != FocusSidebar {
		t.Error("expected initial focus on sidebar")
	}
}
```

Run: `go test ./internal/tui/ -run TestMultiplexer -v`
Expected: FAIL

- [ ] **Step 2: Implement MultiplexerModel**

```go
// internal/tui/multiplexer.go
package tui

import (
	"time"

	"jarvis/internal/config"
	"jarvis/internal/sidecar"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const defaultSidebarWidth = 24

// statusPollMsg triggers a background status poll for all sessions.
type statusPollMsg struct{}

// previewConnectedMsg signals that a preview connection completed.
type previewConnectedMsg struct{ sessionID string }

// sessionAttachedMsg signals that a session attach completed.
type sessionAttachedMsg struct{ sessionID string }

// sessionAttachFailedMsg signals that a session attach failed.
type sessionAttachFailedMsg struct{ err error }

// Multiplexer is the root Bubble Tea model for the multiplexer TUI.
type Multiplexer struct {
	sidebar   *Sidebar
	termPane  *TermPane
	statusBar *StatusBar
	focus     *FocusManager
	cfg       *config.Config

	width  int
	height int

	sidebarWidth int

	// Track which session is being previewed (highlighted in sidebar)
	previewSessionID string
}

func NewMultiplexer(cfg *config.Config) Multiplexer {
	return Multiplexer{
		sidebar:      NewSidebar(cfg, defaultSidebarWidth, 40),
		termPane:     NewTermPane(80, 24),
		statusBar:    NewStatusBar(120),
		focus:        NewFocusManager(),
		cfg:          cfg,
		sidebarWidth: defaultSidebarWidth,
	}
}

func (m Multiplexer) Init() tea.Cmd {
	return tea.Batch(
		m.sidebar.RefreshItems(),
		tickEvery(),
		statusPollEvery(),
	)
}

func statusPollEvery() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return statusPollMsg{}
	})
}

func (m Multiplexer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		mainWidth := m.width - m.sidebarWidth - 1 // -1 for border
		mainHeight := m.height - 1                 // -1 for status bar
		m.sidebar.SetSize(m.sidebarWidth, mainHeight)
		m.termPane.Resize(mainWidth, mainHeight)
		m.statusBar.SetWidth(m.width)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tickMsg:
		return m, tea.Batch(m.sidebar.RefreshItems(), tickEvery())

	case refreshMsg:
		m.sidebar.HandleRefresh(msg.items)
		// If sidebar is focused, update preview to match cursor
		if m.focus.Current() == FocusSidebar {
			return m, m.maybeUpdatePreview()
		}
		return m, nil

	case statusPollMsg:
		// Status poll refreshes are handled by the sidebar's periodic refresh
		return m, statusPollEvery()

	case attachMsg:
		// Session was created via sidebar command (n/c) — attach to it
		return m, m.attachToSession(msg.sessionID)

	case sessionAttachedMsg:
		// Attach completed — update focus state
		m.focus.SetActiveSession(true)
		m.focus.SetFocus(FocusTermPane)
		m.sidebar.SetFocused(false)
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

func (m Multiplexer) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Option+S (Alt+S) always toggles focus, regardless of current focus.
	// Note: handleKey is called synchronously from Update(), so mutations
	// to m (value type) are returned and persist via the (tea.Model, tea.Cmd) return.
	// TermPane and FocusManager are pointers, so mutations are direct.
	if msg.String() == "alt+s" || (msg.Alt && msg.Type == tea.KeyRunes && string(msg.Runes) == "s") {
		if m.focus.Current() == FocusTermPane {
			// Detach and go to sidebar (preview mode)
			m.termPane.Detach()
			m.focus.SetFocus(FocusSidebar)
			m.sidebar.SetFocused(true)
			m.updateStatusBar()
		} else if m.focus.Current() == FocusSidebar && m.focus.HasActiveSession() {
			// Attach and go to term pane
			m.termPane.Attach()
			m.focus.SetFocus(FocusTermPane)
			m.sidebar.SetFocused(false)
			m.updateStatusBar()
		}
		return m, nil
	}

	if m.focus.Current() == FocusSidebar {
		return m.handleSidebarKey(msg)
	}
	return m.handleTermPaneKey(msg)
}

func (m Multiplexer) handleSidebarKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cmd, attachID := m.sidebar.Update(msg)

	if attachID != "" {
		// User pressed Enter on a session — attach to it
		return m, tea.Batch(cmd, m.attachToSession(attachID))
	}

	// After navigation, update preview
	var cmds []tea.Cmd
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	// Check if cursor moved to a different session
	prevCmd := m.maybeUpdatePreview()
	if prevCmd != nil {
		cmds = append(cmds, prevCmd)
	}

	if len(cmds) > 0 {
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

func (m Multiplexer) handleTermPaneKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// All keys (except Option+S, handled above) go to the sidecar
	m.termPane.SendInput(keyToBytes(msg))
	return m, nil
}

// attachToSession connects to a session in attached (interactive) mode.
// Returns a tea.Cmd that performs the connection and returns a message.
// IMPORTANT: This is a tea.Cmd (runs async) so it must NOT mutate the
// Multiplexer model directly. State changes happen when the returned
// message is processed in Update().
func (m Multiplexer) attachToSession(sessionID string) tea.Cmd {
	termPane := m.termPane // capture pointer (TermPane is pointer-based, safe)
	mgr := m.sidebar.mgr
	return func() tea.Msg {
		socketPath := sidecar.SocketPath(sessionID)

		// If already previewing this session, just attach
		if termPane.SessionID() == sessionID {
			if err := termPane.Attach(); err != nil {
				return sessionAttachFailedMsg{err: err}
			}
		} else {
			// Connect and attach
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

// maybeUpdatePreview checks if the sidebar cursor points to a different session
// than what's currently previewed, and connects to it if so.
// Returns a tea.Cmd that performs the preview connection asynchronously.
func (m Multiplexer) maybeUpdatePreview() tea.Cmd {
	item := m.sidebar.SelectedItem()
	if item == nil || !item.IsSession() {
		return nil
	}
	if item.ID == m.previewSessionID {
		return nil
	}

	sessionID := item.ID
	termPane := m.termPane // capture pointer

	return func() tea.Msg {
		socketPath := sidecar.SocketPath(sessionID)
		termPane.ConnectPreview(socketPath, sessionID)
		return previewConnectedMsg{sessionID: sessionID}
	}
}

func (m *Multiplexer) updateStatusBar() {
	item := m.sidebar.SelectedItem()
	if item != nil && item.IsSession() {
		state := string(item.State)
		if state == "" {
			state = string(item.Status)
		}
		m.statusBar.SetSession(item.Name, state)
	} else {
		m.statusBar.ClearSession()
	}
}

func (m Multiplexer) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading..."
	}

	sidebarView := m.sidebar.View()
	mainView := m.termPane.View()
	statusView := m.statusBar.View()

	// Border between sidebar and main pane
	borderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	if m.focus.Current() == FocusSidebar {
		borderStyle = borderStyle.Foreground(lipgloss.Color("99")) // purple when sidebar focused
	}

	var borderLines []string
	mainHeight := m.height - 1
	for i := 0; i < mainHeight; i++ {
		borderLines = append(borderLines, borderStyle.Render("│"))
	}
	border := lipgloss.JoinVertical(lipgloss.Left, borderLines...)

	body := lipgloss.JoinHorizontal(lipgloss.Top, sidebarView, border, mainView)
	return lipgloss.JoinVertical(lipgloss.Left, body, statusView)
}

// keyToBytes converts a Bubble Tea KeyMsg to raw bytes for sending to the sidecar.
func keyToBytes(msg tea.KeyMsg) string {
	switch msg.Type {
	case tea.KeyRunes:
		return string(msg.Runes)
	case tea.KeyEnter:
		return "\r"
	case tea.KeyTab:
		return "\t"
	case tea.KeyBackspace:
		return "\x7f"
	case tea.KeyEscape, tea.KeyEsc:
		return "\x1b"
	case tea.KeyUp:
		return "\x1b[A"
	case tea.KeyDown:
		return "\x1b[B"
	case tea.KeyRight:
		return "\x1b[C"
	case tea.KeyLeft:
		return "\x1b[D"
	case tea.KeySpace:
		return " "
	case tea.KeyDelete:
		return "\x1b[3~"
	case tea.KeyHome:
		return "\x1b[H"
	case tea.KeyEnd:
		return "\x1b[F"
	case tea.KeyPgUp:
		return "\x1b[5~"
	case tea.KeyPgDown:
		return "\x1b[6~"
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
	case tea.KeyCtrlJ:
		return "\x0a"
	case tea.KeyCtrlK:
		return "\x0b"
	case tea.KeyCtrlL:
		return "\x0c"
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
	}

	// Fallback: use the string representation
	return msg.String()
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/tui/ -run TestMultiplexer -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/tui/multiplexer.go internal/tui/multiplexer_test.go
git commit -m "feat: add Multiplexer root model composing sidebar, termpane, statusbar"
```

---

### Task 7: Update cmd/jarvis/main.go

**Files:**
- Modify: `cmd/jarvis/main.go:33-62` (replace `runDashboard()`)

Replace the dashboard-exit-attach-restart loop with a single `tea.NewProgram(NewMultiplexer())` call.

- [ ] **Step 1: Update runDashboard()**

Replace the `runDashboard()` function in `cmd/jarvis/main.go`:

```go
func runDashboard() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	m := tui.NewMultiplexer(cfg)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err = p.Run()
	return err
}
```

This replaces the entire `for { ... }` loop. No more exit/attach/restart cycle.

- [ ] **Step 2: Remove the unused import of `time` if no longer needed**

Check if `time` is still used in `main.go`. If only the old `time.Sleep(100 * time.Millisecond)` line used it, remove the import.

- [ ] **Step 3: Verify build**

Run: `go build ./cmd/jarvis/`
Expected: SUCCESS

- [ ] **Step 4: Manual smoke test**

Run `./jarvis` and verify:
- Sidebar appears on the left with session list
- Main pane shows empty state message
- Arrow keys navigate the sidebar
- `n` creates a new session
- If sessions exist, navigating highlights them and main pane shows preview
- `Enter` on a session focuses the main pane
- `Option+S` toggles back to sidebar
- `q` exits

- [ ] **Step 5: Commit**

```bash
git add cmd/jarvis/main.go
git commit -m "feat: wire up Multiplexer in main.go, remove dashboard-attach loop"
```

---

### Task 8: Integration test

**Files:**
- Create: `internal/tui/multiplexer_integration_test.go`

Test the full lifecycle: create TermPane → connect to a real sidecar → verify VT emulator renders output → send input → verify echo.

- [ ] **Step 1: Write integration test**

```go
// internal/tui/multiplexer_integration_test.go
//go:build integration

package tui

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"jarvis/internal/config"
	"jarvis/internal/session"
	"jarvis/internal/sidecar"
)

func TestTermPane_SidecarRoundTrip(t *testing.T) {
	// Skip if sidecar binary not available
	if _, err := exec.LookPath("jarvis-sidecar"); err != nil {
		t.Skip("jarvis-sidecar not found in PATH")
	}

	cfg, _ := config.Load()
	mgr := session.NewManager(cfg)

	// Spawn a session running 'cat' (echoes input)
	sess, err := mgr.Spawn("test-integration", "/tmp", []string{"cat"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer func() {
		// Cleanup
		session.KillSessionSidecar(sess.ID)
	}()

	socketPath := sidecar.SocketPath(sess.ID)

	// Wait for sidecar
	time.Sleep(1 * time.Second)

	// Connect TermPane in preview mode
	tp := NewTermPane(80, 24)
	defer tp.Close()

	if err := tp.ConnectPreview(socketPath, sess.ID); err != nil {
		t.Fatalf("connect preview: %v", err)
	}

	// Attach for interactive mode
	if err := tp.Attach(); err != nil {
		t.Fatalf("attach: %v", err)
	}

	// Send input
	tp.SendInput("hello world\n")

	// Wait for echo
	time.Sleep(500 * time.Millisecond)

	// Check VT emulator output
	view := tp.View()
	stripped := stripAnsi(view)
	if !strings.Contains(stripped, "hello world") {
		t.Errorf("expected 'hello world' in view, got: %q", stripped)
	}
}
```

- [ ] **Step 2: Run integration test**

Run: `go test ./internal/tui/ -tags integration -run TestTermPane_SidecarRoundTrip -v -timeout 30s`
Expected: PASS (if jarvis-sidecar is built)

- [ ] **Step 3: Commit**

```bash
git add internal/tui/multiplexer_integration_test.go
git commit -m "test: add integration test for TermPane sidecar round-trip"
```

---

### Task 9: Clean up and deprecate old Dashboard code

**Files:**
- Modify: `internal/tui/dashboard.go` (add deprecation comment, keep for `jarvis attach` fallback)

- [ ] **Step 1: Add deprecation notice**

Add a comment at the top of `dashboard.go`:

```go
// Deprecated: Dashboard is the legacy full-screen TUI model.
// New code should use Multiplexer (multiplexer.go) which embeds the
// Sidebar component. Dashboard is retained for the 'jarvis attach' CLI
// fallback path only.
```

- [ ] **Step 2: Verify all tests pass**

Run: `go test ./... -count=1`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/tui/dashboard.go
git commit -m "chore: mark Dashboard as deprecated in favor of Multiplexer"
```
