// Package tui implements the interactive terminal dashboard for Jarvis.
//
// The dashboard is built with Bubble Tea (bubbletea) and displays a
// navigable tree of sessions and folders.  Users can create, rename,
// delete, and attach to sessions without leaving the TUI.
//
// File layout:
//   - dashboard.go  — model definition, Init, Update, key handling
//   - view.go       — View() and rendering helpers
//   - commands.go   — async business-logic commands (spawn, delete, …)
//   - builder.go    — builds the flat item list from disk
//   - item.go       — ListItem data structure
//   - styles.go     — Lipgloss colour/style definitions
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"jarvis/internal/config"
	"jarvis/internal/model"
	"jarvis/internal/session"
	"jarvis/internal/store"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"
)

// ── Dashboard modes ─────────────────────────────────────────────────────

// Mode represents what the dashboard is currently doing.
type Mode int

const (
	ModeDashboard Mode = iota // Normal navigation
	ModeSearch                // Filtering by search query
	ModeInput                 // Collecting text input (new name, rename, etc.)
)

// ── Bubble Tea messages ─────────────────────────────────────────────────

type tickMsg struct{}                      // periodic refresh timer
type refreshMsg struct{ items []ListItem } // new item list from disk
type statusMsgClear struct{}               // clear transient status text
type attachMsg struct{ sessionID string }  // signal to exit TUI and attach

// ── Dashboard model ─────────────────────────────────────────────────────

// Dashboard is the main Bubble Tea model.  It holds the full UI state:
// the item list, cursor position, scroll offset, expand/collapse state,
// and input fields for search and commands.
type Dashboard struct {
	items  []ListItem // flat list of folders + sessions
	cursor int        // index into filteredItems()
	mode   Mode
	width  int // terminal width  (updated on WindowSizeMsg)
	height int // terminal height (updated on WindowSizeMsg)
	cfg    *config.Config
	mgr    *session.Manager

	// Folder expand state — persists across refreshes and restarts.
	expandState map[string]bool // folder ID → expanded?

	// Scroll offset for the viewport (how many rows to skip at the top).
	scrollOffset int

	// Search mode state.
	searchInput textinput.Model
	searchQuery string

	// Command input mode state (used for /new, /folder, rename).
	cmdInput    textinput.Model
	cmdPrompt   string           // label shown before the input, e.g. "New session name: "
	cmdCallback func(string) tea.Cmd // called when the user presses Enter

	// Transient status message shown at the bottom.
	statusMsg string
	err       error

	// Set when the user presses Enter on a session — signals the outer
	// program to exit the TUI and call Manager.Attach().
	attachSessionID string
}

// NewDashboard creates a fresh dashboard, loading persisted view state
// from disk and running session recovery.
func NewDashboard(cfg *config.Config) Dashboard {
	si := textinput.New()
	si.Placeholder = "search..."
	si.CharLimit = 100

	ci := textinput.New()
	ci.CharLimit = 200

	// Mark dead sidecars as suspended before we display anything.
	session.RecoverAllSessions()

	expandState := map[string]bool{"__done__": false}
	cursor := 0
	scrollOffset := 0

	// Restore viewport state from last session.
	if saved := loadState(); saved != nil {
		if saved.ExpandState != nil {
			expandState = saved.ExpandState
		}
		cursor = saved.Cursor
		scrollOffset = saved.ScrollOffset
	}

	return Dashboard{
		cfg:          cfg,
		mgr:          session.NewManager(cfg),
		expandState:  expandState,
		cursor:       cursor,
		scrollOffset: scrollOffset,
		searchInput:  si,
		cmdInput:     ci,
		mode:         ModeDashboard,
	}
}

// AttachSessionID returns the session ID the user chose to attach to,
// or "" if the user quit normally.
func (d Dashboard) AttachSessionID() string {
	return d.attachSessionID
}

// ── Persistence (dashboard viewport state) ──────────────────────────────

// dashboardState is the YAML-serialisable viewport state saved to disk.
type dashboardState struct {
	ExpandState  map[string]bool `yaml:"expand_state"`
	Cursor       int             `yaml:"cursor"`
	ScrollOffset int             `yaml:"scroll_offset"`
}

func statePath() string {
	return filepath.Join(store.JarvisHome(), "dashboard_state.yaml")
}

// SaveState writes the current viewport state to disk so it survives restarts.
func (d Dashboard) SaveState() {
	state := dashboardState{
		ExpandState:  d.expandState,
		Cursor:       d.cursor,
		ScrollOffset: d.scrollOffset,
	}
	data, err := yaml.Marshal(&state)
	if err != nil {
		return
	}
	store.WriteAtomic(statePath(), data)
}

func loadState() *dashboardState {
	data, err := os.ReadFile(statePath())
	if err != nil {
		return nil
	}
	var state dashboardState
	if err := yaml.Unmarshal(data, &state); err != nil {
		return nil
	}
	return &state
}

// ── Bubble Tea lifecycle ────────────────────────────────────────────────

// Init is called once when the program starts.  It kicks off the first
// data load and the periodic refresh timer.
func (d Dashboard) Init() tea.Cmd {
	return tea.Batch(d.refreshItems(), tickEvery())
}

func tickEvery() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
		return tickMsg{}
	})
}

func (d Dashboard) refreshItems() tea.Cmd {
	return func() tea.Msg {
		return refreshMsg{items: buildItemList(d.mgr)}
	}
}

// Update is the main event handler.  Bubble Tea calls it for every
// message (key press, timer tick, window resize, etc.).
func (d Dashboard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return d.handleKey(msg)

	case tea.WindowSizeMsg:
		d.width = msg.Width
		d.height = msg.Height
		return d, nil

	case tickMsg:
		return d, tea.Batch(d.refreshItems(), tickEvery())

	case refreshMsg:
		d.items = msg.items
		// Apply persisted expand state to freshly-loaded items.
		for i := range d.items {
			if d.items[i].IsFolder() {
				if expanded, exists := d.expandState[d.items[i].ID]; exists {
					d.items[i].Expanded = expanded
				}
			}
		}
		visible := d.filteredItems()
		if d.cursor >= len(visible) {
			d.cursor = max(0, len(visible)-1)
		}
		d.adjustScroll()
		return d, nil

	case statusMsgClear:
		d.statusMsg = ""
		return d, nil

	case attachMsg:
		d.attachSessionID = msg.sessionID
		return d, tea.Quit
	}

	return d, nil
}

// ── Key handling ────────────────────────────────────────────────────────

func (d Dashboard) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch d.mode {
	case ModeSearch:
		return d.handleSearchKey(msg)
	case ModeInput:
		return d.handleInputKey(msg)
	default:
		return d.handleDashboardKey(msg)
	}
}

func (d Dashboard) handleDashboardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	visible := d.filteredItems()

	switch msg.String() {
	case "q", "ctrl+c":
		d.SaveState()
		return d, tea.Quit

	// ── Navigation ──
	case "up", "k":
		if d.cursor > 0 {
			d.cursor--
			if d.cursor >= 0 && d.cursor < len(visible) && visible[d.cursor].ID == "__separator__" {
				if d.cursor > 0 {
					d.cursor--
				} else {
					d.cursor++
				}
			}
			d.adjustScroll()
		}

	case "down", "j":
		if d.cursor < len(visible)-1 {
			d.cursor++
			if d.cursor < len(visible) && visible[d.cursor].ID == "__separator__" {
				if d.cursor < len(visible)-1 {
					d.cursor++
				} else {
					d.cursor--
				}
			}
			d.adjustScroll()
		}

	// ── Actions ──
	case "enter":
		item := d.selectedItem()
		if item == nil {
			break
		}
		if item.IsSession() && item.Status != "archived" {
			d.SaveState()
			return d, func() tea.Msg { return attachMsg{sessionID: item.ID} }
		}
		if item.IsFolder() {
			d.toggleFolder(item.ID)
			return d, d.refreshItems()
		}

	case "/":
		d.mode = ModeSearch
		d.searchInput.Focus()
		return d, textinput.Blink

	case "n":
		parentID, parentName := d.resolveParentFolder()
		d.cmdPrompt = "New session name: "
		if parentName != "" {
			d.cmdPrompt = fmt.Sprintf("New session in %s: ", parentName)
		}
		d.cmdInput.SetValue("")
		d.cmdInput.Focus()
		d.cmdCallback = func(name string) tea.Cmd {
			return d.createSession(name, parentID)
		}
		d.mode = ModeInput
		return d, textinput.Blink

	case "f":
		parentID, parentName := d.resolveParentFolder()
		d.cmdPrompt = "New folder name: "
		if parentName != "" {
			d.cmdPrompt = fmt.Sprintf("New folder in %s: ", parentName)
		}
		d.cmdInput.SetValue("")
		d.cmdInput.Focus()
		d.cmdCallback = func(name string) tea.Cmd {
			return d.createFolder(name, parentID)
		}
		d.mode = ModeInput
		return d, textinput.Blink

	case "c":
		parentID, _ := d.resolveParentFolder()
		return d, d.createChat(parentID)

	case "a":
		item := d.selectedItem()
		if item != nil && item.IsSession() && item.State == model.StateWaitingForApproval {
			return d, d.quickApprove(item.ID)
		}

	case "d":
		item := d.selectedItem()
		if item == nil {
			break
		}
		if item.IsSession() {
			return d, d.markDone(item.ID)
		}
		if item.IsFolder() && item.ID != "__done__" {
			return d, d.markFolderDone(item.ID)
		}

	case "r":
		item := d.selectedItem()
		if item != nil {
			d.cmdPrompt = "Rename: "
			d.cmdInput.SetValue(item.Name)
			d.cmdInput.CursorEnd()
			d.cmdInput.Focus()
			itemID := item.ID
			isFolder := item.IsFolder()
			d.cmdCallback = func(name string) tea.Cmd {
				if isFolder {
					return d.renameFolder(itemID, name)
				}
				return d.renameSession(itemID, name)
			}
			d.mode = ModeInput
			return d, textinput.Blink
		}

	case "x":
		item := d.selectedItem()
		if item == nil || item.ID == "__done__" {
			break
		}
		if item.IsSession() {
			return d, d.deleteSession(item.ID, item.Name)
		}
		if item.IsFolder() {
			return d, d.deleteFolder(item.ID, item.Name)
		}

	case "ctrl+r":
		return d, d.refreshItems()
	}

	return d, nil
}

func (d Dashboard) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		d.mode = ModeDashboard
		d.searchQuery = ""
		d.searchInput.SetValue("")
		return d, d.refreshItems()
	case "enter":
		d.searchQuery = d.searchInput.Value()
		d.mode = ModeDashboard
		return d, d.refreshItems()
	}

	var cmd tea.Cmd
	d.searchInput, cmd = d.searchInput.Update(msg)
	d.searchQuery = d.searchInput.Value()
	return d, cmd
}

func (d Dashboard) handleInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		d.mode = ModeDashboard
		return d, nil
	case "enter":
		val := d.cmdInput.Value()
		d.mode = ModeDashboard
		if val != "" && d.cmdCallback != nil {
			return d, d.cmdCallback(val)
		}
		return d, nil
	}

	var cmd tea.Cmd
	d.cmdInput, cmd = d.cmdInput.Update(msg)
	return d, cmd
}

// ── Internal helpers ────────────────────────────────────────────────────

// selectedItem returns the ListItem under the cursor, or nil.
func (d Dashboard) selectedItem() *ListItem {
	visible := d.filteredItems()
	if d.cursor >= 0 && d.cursor < len(visible) {
		item := visible[d.cursor]
		return &item
	}
	return nil
}

// resolveParentFolder decides which folder new items should be created in
// based on the current cursor position:
//   - cursor on a folder → create inside that folder
//   - cursor on a session inside a folder → create as a sibling
//   - cursor on a top-level item or the Done folder → create at top level
func (d Dashboard) resolveParentFolder() (id string, name string) {
	item := d.selectedItem()
	if item == nil {
		return "", ""
	}
	if item.IsFolder() && item.ID != "__done__" {
		return item.ID, item.Name
	}
	if item.ParentID != "" && item.ParentID != "__done__" {
		for _, i := range d.items {
			if i.ID == item.ParentID {
				return i.ID, i.Name
			}
		}
		return item.ParentID, ""
	}
	return "", ""
}

// toggleFolder flips the expand/collapse state for a folder.
func (d *Dashboard) toggleFolder(id string) {
	current, exists := d.expandState[id]
	if !exists {
		current = false
	}
	d.expandState[id] = !current
}

// viewportHeight returns the number of item rows that fit on screen.
func (d Dashboard) viewportHeight() int {
	maxRows := d.height - 4 // reserve header (2 lines) + footer (2 lines)
	if maxRows < 5 {
		maxRows = 5
	}
	return maxRows
}

// adjustScroll ensures the cursor stays within the visible viewport.
func (d *Dashboard) adjustScroll() {
	maxRows := d.viewportHeight()
	if d.cursor < d.scrollOffset {
		d.scrollOffset = d.cursor
	}
	if d.cursor >= d.scrollOffset+maxRows {
		d.scrollOffset = d.cursor - maxRows + 1
	}
	if d.scrollOffset < 0 {
		d.scrollOffset = 0
	}
}

// isExpanded checks the live expand state for a folder.
func (d Dashboard) isExpanded(id string) bool {
	if expanded, exists := d.expandState[id]; exists {
		return expanded
	}
	return false // all folders collapsed by default
}

// filteredItems returns the items visible in the current mode:
//   - In search mode: only items whose name matches the query
//   - Otherwise: items respecting folder expand/collapse state
func (d Dashboard) filteredItems() []ListItem {
	if d.searchQuery != "" {
		query := strings.ToLower(d.searchQuery)
		var result []ListItem
		for _, item := range d.items {
			if strings.Contains(strings.ToLower(item.Name), query) {
				result = append(result, item)
			}
		}
		return result
	}

	// Apply expand/collapse: skip children of collapsed folders.
	var visible []ListItem
	skipDepth := -1
	for _, item := range d.items {
		if skipDepth >= 0 && item.Depth > skipDepth {
			continue
		}
		skipDepth = -1

		if item.IsFolder() && !d.isExpanded(item.ID) {
			skipDepth = item.Depth
		}

		visible = append(visible, item)
	}
	return visible
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
