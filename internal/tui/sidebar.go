package tui

// sidebar.go — Sidebar component that renders the dashboard's core logic
// (item list, navigation, folder expand/collapse, search, input modes,
// commands) in a fixed-width column instead of full-screen.
//
// The Sidebar is NOT a tea.Model — it's a component used by the parent
// Multiplexer model which calls its methods directly.

import (
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"jarvis/internal/config"
	"jarvis/internal/model"
	"jarvis/internal/protocol"
	"jarvis/internal/session"
	"jarvis/internal/sidecar"
	"jarvis/internal/store"
	"jarvis/internal/ui"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"gopkg.in/yaml.v3"
)

// Sidebar renders the session/folder tree in a fixed-width column.
// It replicates the Dashboard's core logic but is driven by the parent
// Multiplexer rather than by Bubble Tea directly.
type Sidebar struct {
	items   []ListItem
	cursor  int
	mode    Mode
	width   int
	height  int
	cfg     *config.Config
	mgr     *session.Manager
	focused bool

	// Folder expand state — persists across refreshes and restarts.
	expandState map[string]bool

	// Scroll offset for the viewport (how many rows to skip at the top).
	scrollOffset int

	// Search mode state.
	searchInput textinput.Model
	searchQuery string

	// Command input mode state (used for new, folder, rename).
	cmdInput    textinput.Model
	cmdPrompt   string
	cmdCallback func(string) tea.Cmd

	// Transient status message shown at the bottom.
	statusMsg string
}

// NewSidebar creates a fresh sidebar, loading persisted view state from disk
// and running session recovery.
func NewSidebar(cfg *config.Config, width, height int) *Sidebar {
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

	return &Sidebar{
		cfg:          cfg,
		mgr:          session.NewManager(cfg),
		width:        width,
		height:       height,
		expandState:  expandState,
		cursor:       cursor,
		scrollOffset: scrollOffset,
		searchInput:  si,
		cmdInput:     ci,
		mode:         ModeDashboard,
	}
}

// ── Accessors ───────────────────────────────────────────────────────────

// SetSize updates the sidebar dimensions.
func (s *Sidebar) SetSize(width, height int) {
	s.width = width
	s.height = height
}

// SetFocused marks whether this sidebar currently has keyboard focus.
func (s *Sidebar) SetFocused(focused bool) {
	s.focused = focused
}

// IsFocused returns true when the sidebar has keyboard focus.
func (s *Sidebar) IsFocused() bool {
	return s.focused
}

// Manager exposes the session manager for Multiplexer use.
func (s *Sidebar) Manager() *session.Manager {
	return s.mgr
}

// SelectedItem returns the ListItem under the cursor, or nil.
func (s *Sidebar) SelectedItem() *ListItem {
	visible := s.filteredItems()
	if s.cursor >= 0 && s.cursor < len(visible) {
		item := visible[s.cursor]
		return &item
	}
	return nil
}

// ── Persistence ─────────────────────────────────────────────────────────

// SaveState writes the current viewport state to disk so it survives restarts.
func (s *Sidebar) SaveState() {
	state := dashboardState{
		ExpandState:  s.expandState,
		Cursor:       s.cursor,
		ScrollOffset: s.scrollOffset,
	}
	data, err := yaml.Marshal(&state)
	if err != nil {
		return
	}
	store.WriteAtomic(statePath(), data)
}

// ── Refresh ─────────────────────────────────────────────────────────────

// RefreshItems returns a tea.Cmd that builds the item list from disk.
func (s *Sidebar) RefreshItems() tea.Cmd {
	return func() tea.Msg {
		return refreshMsg{items: buildItemListCached()}
	}
}

// HandleRefresh processes a refreshMsg, updating the item list and
// maintaining cursor position.
func (s *Sidebar) HandleRefresh(items []ListItem) {
	// Remember the currently selected item so the cursor follows it
	// after the list is rebuilt.
	var selectedID string
	if old := s.SelectedItem(); old != nil {
		selectedID = old.ID
	}

	s.items = items

	// Apply persisted expand state to freshly-loaded items.
	for i := range s.items {
		if s.items[i].IsFolder() {
			if expanded, exists := s.expandState[s.items[i].ID]; exists {
				s.items[i].Expanded = expanded
			}
		}
	}

	visible := s.filteredItems()

	// Try to restore the cursor to the same item by ID.
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

// ── Update (key handling) ───────────────────────────────────────────────

// Update handles a key message and returns (cmd, attachSessionID).
// attachSessionID is non-empty when the user presses Enter on a session.
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
	// NOTE: q and ctrl+c are NOT handled here — the parent Multiplexer
	// handles quit.

	// ── Navigation ──
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

	// ── Actions ──
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

	case " ":
		item := s.SelectedItem()
		if item != nil && item.IsFolder() {
			s.toggleFolder(item.ID)
			return s.RefreshItems(), ""
		}

	case "/":
		s.mode = ModeSearch
		s.searchInput.Focus()
		return textinput.Blink, ""

	case "n":
		parentID, parentName := s.resolveParentFolder()
		s.cmdPrompt = "New session: "
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
		s.cmdPrompt = "New folder: "
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

// ── View ────────────────────────────────────────────────────────────────

// View renders the sidebar as a fixed-width column string.
func (s *Sidebar) View() string {
	var b strings.Builder

	// ── Header: "SESSIONS" + counts ──
	sessionCount := 0
	blockedCount := 0
	for _, item := range s.items {
		if item.IsSession() && item.Status != model.StatusDone && item.Status != model.StatusArchived {
			sessionCount++
		}
		if item.IsSession() && item.State == model.StateWaitingForApproval {
			blockedCount++
		}
	}

	title := titleStyle.Render("SESSIONS")
	stats := statusBarStyle.Render(fmt.Sprintf(" %d", sessionCount))
	if blockedCount > 0 {
		stats += blockedStyle.Render(fmt.Sprintf(" %d!", blockedCount))
	}
	b.WriteString(title + stats + "\n\n")

	// ── Item list (scrollable viewport) ──
	visibleItems := s.filteredItems()

	if len(visibleItems) == 0 {
		b.WriteString(dimStyle.Render("  No sessions.\n  Press [n] to create one.\n"))
	}

	maxRows := s.viewportHeight()
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
		indent := strings.Repeat("  ", item.Depth)
		line := s.renderItem(item)

		if i == s.cursor {
			b.WriteString(selectedStyle.Render("▌") + " " + indent + line + "\n")
		} else {
			b.WriteString("  " + indent + line + "\n")
		}
	}

	// ── Status message ──
	if s.statusMsg != "" {
		b.WriteString("\n" + s.statusMsg + "\n")
	}

	// ── Footer: mode-dependent input area ──
	switch s.mode {
	case ModeSearch:
		b.WriteString("\n  " + inputStyle.Render("/") + " " + s.searchInput.View())
	case ModeInput:
		b.WriteString("\n  " + inputStyle.Render(s.cmdPrompt) + s.cmdInput.View())
	default:
		help := "  [n]ew [f]older [/]search"
		b.WriteString(helpStyle.Render(help))
	}

	// Constrain to fixed width.
	return lipgloss.NewStyle().Width(s.width).Render(b.String())
}

// renderItem produces a single formatted line for one list item.
// Session items render compactly (icon + truncated name) to fit the narrow width.
func (s *Sidebar) renderItem(item ListItem) string {
	// ── Separator ──
	if item.ID == "__separator__" {
		sepWidth := s.width - 4
		if sepWidth < 5 {
			sepWidth = 5
		}
		return dimStyle.Render(strings.Repeat("─", sepWidth))
	}

	// ── Folder row: arrow + name + progress ──
	if item.IsFolder() {
		arrow := "▶"
		if s.isExpanded(item.ID) {
			arrow = "▼"
		}
		// Truncate folder name to fit.
		nameWidth := s.width - 4 - item.Depth*2 - 2 // arrow(2) + cursor(2) + indent
		if nameWidth < 8 {
			nameWidth = 8
		}
		name := folderStyle.Render(ui.Truncate(item.Name, nameWidth))
		progress := ""
		if item.TotalCount > 0 {
			progress = dimStyle.Render(fmt.Sprintf(" %d/%d", item.DoneCount, item.TotalCount))
		}
		return fmt.Sprintf("%s %s%s", arrow, name, progress)
	}

	// ── Session row: icon + truncated name (compact) ──
	icon := sessionIcon(item.Status, item.State)

	// Compact layout: cursor(2) + indent(depth*2) + icon(2) + name
	nameWidth := s.width - 2 - item.Depth*2 - 3
	if nameWidth < 8 {
		nameWidth = 8
	}
	name := ui.Truncate(item.Name, nameWidth)

	return fmt.Sprintf("%s %s", icon, name)
}

// ── Internal helpers ────────────────────────────────────────────────────

// resolveParentFolder decides which folder new items should be created in
// based on the current cursor position.
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

// toggleFolder flips the expand/collapse state for a folder.
func (s *Sidebar) toggleFolder(id string) {
	current, exists := s.expandState[id]
	if !exists {
		current = false
	}
	s.expandState[id] = !current
}

// viewportHeight returns the number of item rows that fit on screen.
func (s *Sidebar) viewportHeight() int {
	maxRows := s.height - 4 // reserve header (2 lines) + footer (2 lines)
	if maxRows < 5 {
		maxRows = 5
	}
	return maxRows
}

// adjustScroll ensures the cursor stays within the visible viewport.
func (s *Sidebar) adjustScroll() {
	maxRows := s.viewportHeight()
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

// isExpanded checks the live expand state for a folder.
func (s *Sidebar) isExpanded(id string) bool {
	if expanded, exists := s.expandState[id]; exists {
		return expanded
	}
	return false
}

// filteredItems returns the items visible in the current mode.
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

// ── Command methods ─────────────────────────────────────────────────────
// These mirror the Dashboard command methods in commands.go, using s.cfg
// and s.mgr instead of d.cfg and d.mgr.

func (s *Sidebar) createSession(name string, parentID string) tea.Cmd {
	return func() tea.Msg {
		cwd := s.cfg.RepoPath()
		if cwd == "" {
			cwd = "."
		}
		sess, err := s.mgr.Spawn(name, cwd, []string{"claude"})
		if err != nil {
			return refreshMsg{items: buildItemListCached()}
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

func (s *Sidebar) createChat(parentID string) tea.Cmd {
	return func() tea.Msg {
		cwd := s.cfg.RepoPath()
		if cwd == "" {
			cwd = "."
		}
		sess, err := s.mgr.Spawn("(untitled chat)", cwd, []string{"claude"})
		if err != nil {
			return refreshMsg{items: buildItemListCached()}
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

func (s *Sidebar) createFolder(name string, parentID string) tea.Cmd {
	return func() tea.Msg {
		now := time.Now()
		f := &model.Folder{
			ID:        model.NewID(),
			Type:      "folder",
			Name:      name,
			ParentID:  parentID,
			Status:    "active",
			CreatedAt: now,
		}
		store.SaveFolder(f)

		if parentID != "" {
			parent, err := store.GetFolder(parentID)
			if err == nil {
				parent.Children = append(parent.Children, model.ChildRef{Type: "folder", ID: f.ID})
				store.SaveFolder(parent)
			}
		}

		return refreshMsg{items: buildItemListCached()}
	}
}

func (s *Sidebar) renameSession(sessionID, name string) tea.Cmd {
	return func() tea.Msg {
		sess, err := store.GetSession(sessionID)
		if err == nil {
			sess.Name = name
			sess.UpdatedAt = time.Now()
			store.SaveSession(sess)
		}
		return refreshMsg{items: buildItemListCached()}
	}
}

func (s *Sidebar) renameFolder(folderID, name string) tea.Cmd {
	return func() tea.Msg {
		f, err := store.GetFolder(folderID)
		if err == nil {
			f.Name = name
			store.SaveFolder(f)
		}
		return refreshMsg{items: buildItemListCached()}
	}
}

func (s *Sidebar) markDone(sessionID string) tea.Cmd {
	return func() tea.Msg {
		sess, err := store.GetSession(sessionID)
		if err == nil {
			sess.Status = model.StatusDone
			sess.UpdatedAt = time.Now()
			store.SaveSession(sess)
		}
		return refreshMsg{items: buildItemListCached()}
	}
}

func (s *Sidebar) markFolderDone(folderID string) tea.Cmd {
	return func() tea.Msg {
		f, err := store.GetFolder(folderID)
		if err != nil {
			return refreshMsg{items: buildItemListCached()}
		}

		now := time.Now()
		for _, child := range f.Children {
			if child.Type == "session" {
				sess, err := store.GetSession(child.ID)
				if err == nil && sess.Status != model.StatusDone && sess.Status != model.StatusArchived {
					sess.Status = model.StatusDone
					sess.UpdatedAt = now
					store.SaveSession(sess)
				}
			}
		}

		f.Status = "done"
		store.SaveFolder(f)

		return refreshMsg{items: buildItemListCached()}
	}
}

func (s *Sidebar) deleteSession(sessionID, name string) tea.Cmd {
	return func() tea.Msg {
		killSessionSidecar(sessionID)

		sess, err := store.GetSession(sessionID)
		if err == nil && sess.ParentID != "" {
			removeChildFromFolder(sess.ParentID, "session", sessionID)
		}

		store.DeleteSession(sessionID)
		return refreshMsg{items: buildItemListCached()}
	}
}

func (s *Sidebar) deleteFolder(folderID, name string) tea.Cmd {
	return func() tea.Msg {
		deleteFolderRecursive(folderID)
		return refreshMsg{items: buildItemListCached()}
	}
}

func (s *Sidebar) quickApprove(sessionID string) tea.Cmd {
	return func() tea.Msg {
		socketPath := sidecar.SocketPath(sessionID)
		conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
		if err != nil {
			log.Printf("sidebar: quick-approve connect failed for %s: %v", sessionID, err)
			return refreshMsg{items: buildItemListCached()}
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))

		codec := protocol.NewCodec(conn)
		codec.Send(protocol.Request{Action: "send_input", Text: "y\n"})

		return refreshMsg{items: buildItemListCached()}
	}
}

