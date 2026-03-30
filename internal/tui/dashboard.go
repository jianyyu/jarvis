package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"jarvis/internal/config"
	"jarvis/internal/model"
	"jarvis/internal/session"
	"jarvis/internal/sidecar"
	"jarvis/internal/store"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"gopkg.in/yaml.v3"
)

// Mode represents the dashboard state.
type Mode int

const (
	ModeDashboard Mode = iota
	ModeSearch
	ModeInput // for /new, /folder etc.
)

// Dashboard is the main bubbletea model.
type Dashboard struct {
	items    []ListItem
	cursor   int
	mode     Mode
	width    int
	height   int
	cfg      *config.Config
	mgr      *session.Manager

	// Folder expand state — persists across refreshes
	expandState map[string]bool // folder ID → expanded

	// Scroll offset for viewport
	scrollOffset int

	// Search
	searchInput textinput.Model
	searchQuery string

	// Command input
	cmdInput    textinput.Model
	cmdPrompt   string // e.g. "New session name: "
	cmdCallback func(string) tea.Cmd

	// Status
	statusMsg string
	err       error

	// For attach — we return a command that tells the outer program to attach
	attachSessionID string
}

// Messages
type tickMsg struct{}
type refreshMsg struct{ items []ListItem }
type statusMsgClear struct{}
type attachMsg struct{ sessionID string }

func NewDashboard(cfg *config.Config) Dashboard {
	si := textinput.New()
	si.Placeholder = "search..."
	si.CharLimit = 100

	ci := textinput.New()
	ci.CharLimit = 200

	// Run recovery once at startup
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

// dashboardState is the persisted view state.
type dashboardState struct {
	ExpandState  map[string]bool `yaml:"expand_state"`
	Cursor       int             `yaml:"cursor"`
	ScrollOffset int             `yaml:"scroll_offset"`
}

func statePath() string {
	return filepath.Join(store.JarvisHome(), "dashboard_state.yaml")
}

// SaveState persists the current view state to disk.
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

// loadState reads persisted view state from disk.
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
		items := buildItemList(d.mgr)
		return refreshMsg{items: items}
	}
}

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
		// Apply persisted expand state to items
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

// resolveParentFolder returns the folder ID and name to create new items in,
// based on the current selection:
//   - cursor on a folder → create inside that folder
//   - cursor on a session inside a folder → create as sibling (in the same folder)
//   - cursor on a top-level session or __done__ → create at top level
func (d Dashboard) resolveParentFolder() (id string, name string) {
	item := d.selectedItem()
	if item == nil {
		return "", ""
	}
	if item.IsFolder() && item.ID != "__done__" {
		return item.ID, item.Name
	}
	if item.ParentID != "" && item.ParentID != "__done__" {
		// Find the parent folder name
		for _, i := range d.items {
			if i.ID == item.ParentID {
				return i.ID, i.Name
			}
		}
		return item.ParentID, ""
	}
	return "", ""
}

func (d Dashboard) selectedItem() *ListItem {
	visible := d.filteredItems()
	if d.cursor >= 0 && d.cursor < len(visible) {
		item := visible[d.cursor]
		return &item
	}
	return nil
}

// toggleFolder toggles the expand state for a folder.
func (d *Dashboard) toggleFolder(id string) {
	current, exists := d.expandState[id]
	if !exists {
		// Default: all folders collapsed
		current = false
	}
	d.expandState[id] = !current
}

// adjustScroll ensures scrollOffset keeps cursor visible.
func (d *Dashboard) adjustScroll() {
	maxRows := d.height - 4
	if maxRows < 5 {
		maxRows = 5
	}
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

func (d Dashboard) handleDashboardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	visible := d.filteredItems()

	switch msg.String() {
	case "q", "ctrl+c":
		d.SaveState()
		return d, tea.Quit

	case "up", "k":
		if d.cursor > 0 {
			d.cursor--
			// Skip separator
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
			// Skip separator
			if d.cursor < len(visible) && visible[d.cursor].ID == "__separator__" {
				if d.cursor < len(visible)-1 {
					d.cursor++
				} else {
					d.cursor--
				}
			}
			d.adjustScroll()
		}

	case "enter":
		item := d.selectedItem()
		if item == nil {
			break
		}
		if item.IsSession() && item.Status != model.StatusArchived {
			d.SaveState()
			return d, func() tea.Msg { return attachMsg{sessionID: item.ID} }
		}
		if item.IsFolder() {
			d.toggleFolder(item.ID)
			// Refresh immediately so the list reflects the new state
			return d, d.refreshItems()
		}

	case "/":
		d.mode = ModeSearch
		d.searchInput.Focus()
		return d, textinput.Blink

	case "n":
		d.cmdPrompt = "New session name: "
		d.cmdInput.SetValue("")
		d.cmdInput.Focus()
		parentID, parentName := d.resolveParentFolder()
		if parentName != "" {
			d.cmdPrompt = fmt.Sprintf("New session in %s: ", parentName)
		}
		d.cmdCallback = func(name string) tea.Cmd {
			return d.createSession(name, parentID)
		}
		d.mode = ModeInput
		return d, textinput.Blink

	case "f":
		d.cmdPrompt = "New folder name: "
		d.cmdInput.SetValue("")
		d.cmdInput.Focus()
		parentID, parentName := d.resolveParentFolder()
		if parentName != "" {
			d.cmdPrompt = fmt.Sprintf("New folder in %s: ", parentName)
		}
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
			return d, func() tea.Msg { return attachMsg{sessionID: item.ID} }
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
		if item == nil {
			break
		}
		if item.ID == "__done__" {
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

// Commands

func (d Dashboard) createSession(name string, parentID string) tea.Cmd {
	return func() tea.Msg {
		cwd := d.cfg.RepoPath()
		if cwd == "" {
			cwd = "."
		}
		sess, err := d.mgr.Spawn(name, cwd, []string{"claude"})
		if err != nil {
			return refreshMsg{items: buildItemList(d.mgr)}
		}

		// Add as child of parent folder
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

func (d Dashboard) createChat(parentID string) tea.Cmd {
	return func() tea.Msg {
		cwd := d.cfg.RepoPath()
		if cwd == "" {
			cwd = "."
		}
		sess, err := d.mgr.Spawn("(untitled chat)", cwd, []string{"claude"})
		if err != nil {
			return refreshMsg{items: buildItemList(d.mgr)}
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

func (d Dashboard) createFolder(name string, parentID string) tea.Cmd {
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

		// Add as child of parent folder
		if parentID != "" {
			parent, err := store.GetFolder(parentID)
			if err == nil {
				parent.Children = append(parent.Children, model.ChildRef{Type: "folder", ID: f.ID})
				store.SaveFolder(parent)
			}
		}

		return refreshMsg{items: buildItemList(d.mgr)}
	}
}

func (d Dashboard) renameSession(sessionID, name string) tea.Cmd {
	return func() tea.Msg {
		s, err := store.GetSession(sessionID)
		if err == nil {
			s.Name = name
			s.UpdatedAt = time.Now()
			store.SaveSession(s)
		}
		return refreshMsg{items: buildItemList(d.mgr)}
	}
}

func (d Dashboard) renameFolder(folderID, name string) tea.Cmd {
	return func() tea.Msg {
		f, err := store.GetFolder(folderID)
		if err == nil {
			f.Name = name
			store.SaveFolder(f)
		}
		return refreshMsg{items: buildItemList(d.mgr)}
	}
}

func (d Dashboard) markDone(sessionID string) tea.Cmd {
	return func() tea.Msg {
		s, err := store.GetSession(sessionID)
		if err == nil {
			s.Status = model.StatusDone
			s.UpdatedAt = time.Now()
			store.SaveSession(s)
		}
		return refreshMsg{items: buildItemList(d.mgr)}
	}
}

func (d Dashboard) markFolderDone(folderID string) tea.Cmd {
	return func() tea.Msg {
		f, err := store.GetFolder(folderID)
		if err != nil {
			return refreshMsg{items: buildItemList(d.mgr)}
		}

		// Mark all child sessions as done
		now := time.Now()
		for _, child := range f.Children {
			if child.Type == "session" {
				s, err := store.GetSession(child.ID)
				if err == nil && s.Status != model.StatusDone && s.Status != model.StatusArchived {
					s.Status = model.StatusDone
					s.UpdatedAt = now
					store.SaveSession(s)
				}
			}
		}

		// Mark folder as archived (moves it out of active view)
		f.Status = "done"
		store.SaveFolder(f)

		return refreshMsg{items: buildItemList(d.mgr)}
	}
}

func (d Dashboard) deleteSession(sessionID, name string) tea.Cmd {
	return func() tea.Msg {
		// Kill sidecar if alive
		socketPath := sidecar.SocketPath(sessionID)
		if session.PingSidecar(socketPath) {
			s, err := store.GetSession(sessionID)
			if err == nil && s.Sidecar != nil && s.Sidecar.PID > 0 {
				if p, err := os.FindProcess(s.Sidecar.PID); err == nil {
					p.Signal(os.Kill)
				}
			}
			os.Remove(socketPath)
		}

		// Remove from parent folder if any
		s, err := store.GetSession(sessionID)
		if err == nil && s.ParentID != "" {
			if parent, err := store.GetFolder(s.ParentID); err == nil {
				var newChildren []model.ChildRef
				for _, c := range parent.Children {
					if !(c.Type == "session" && c.ID == sessionID) {
						newChildren = append(newChildren, c)
					}
				}
				parent.Children = newChildren
				store.SaveFolder(parent)
			}
		}

		store.DeleteSession(sessionID)
		return refreshMsg{items: buildItemList(d.mgr)}
	}
}

func (d Dashboard) deleteFolder(folderID, name string) tea.Cmd {
	return func() tea.Msg {
		deleteFolderRecursive(folderID)
		return refreshMsg{items: buildItemList(d.mgr)}
	}
}

// deleteFolderRecursive deletes a folder and all its descendants.
func deleteFolderRecursive(folderID string) {
	f, err := store.GetFolder(folderID)
	if err != nil {
		return
	}

	for _, child := range f.Children {
		if child.Type == "session" {
			socketPath := sidecar.SocketPath(child.ID)
			if session.PingSidecar(socketPath) {
				s, _ := store.GetSession(child.ID)
				if s != nil && s.Sidecar != nil && s.Sidecar.PID > 0 {
					if p, err := os.FindProcess(s.Sidecar.PID); err == nil {
						p.Signal(os.Kill)
					}
				}
				os.Remove(socketPath)
			}
			store.DeleteSession(child.ID)
		}
		if child.Type == "folder" {
			deleteFolderRecursive(child.ID)
		}
	}

	// Remove from parent folder if nested
	if f.ParentID != "" {
		if parent, err := store.GetFolder(f.ParentID); err == nil {
			var newChildren []model.ChildRef
			for _, c := range parent.Children {
				if !(c.Type == "folder" && c.ID == folderID) {
					newChildren = append(newChildren, c)
				}
			}
			parent.Children = newChildren
			store.SaveFolder(parent)
		}
	}

	store.DeleteFolder(folderID)
}

// View

func (d Dashboard) View() string {
	var b strings.Builder

	// Title
	sessionCount := 0
	blockedCount := 0
	for _, item := range d.items {
		if item.IsSession() && item.Status != model.StatusDone && item.Status != model.StatusArchived {
			sessionCount++
		}
		if item.IsSession() && item.State == model.StateWaitingForApproval {
			blockedCount++
		}
	}

	title := titleStyle.Render("JARVIS")
	stats := statusBarStyle.Render(fmt.Sprintf(" — %d sessions", sessionCount))
	if blockedCount > 0 {
		stats += blockedStyle.Render(fmt.Sprintf(" · %d blocked", blockedCount))
	}
	b.WriteString(title + stats + "\n\n")

	// Filter items by search
	visibleItems := d.filteredItems()

	if len(visibleItems) == 0 {
		b.WriteString(dimStyle.Render("  No sessions. Press [n] to create one.\n"))
	}

	// Reserve lines for header (2) and footer (2)
	maxRows := d.height - 4
	if maxRows < 5 {
		maxRows = 5
	}

	// Render only visible rows within scroll window
	end := d.scrollOffset + maxRows
	if end > len(visibleItems) {
		end = len(visibleItems)
	}
	start := d.scrollOffset
	if start < 0 {
		start = 0
	}

	for i := start; i < end; i++ {
		item := visibleItems[i]
		indent := strings.Repeat("  ", item.Depth)
		line := d.renderItem(item)

		if i == d.cursor {
			b.WriteString(selectedStyle.Render("▌") + " " + indent + line + "\n")
		} else {
			b.WriteString("  " + indent + line + "\n")
		}
	}

	// Status message
	if d.statusMsg != "" {
		b.WriteString("\n" + d.statusMsg + "\n")
	}

	// Input area
	switch d.mode {
	case ModeSearch:
		b.WriteString("\n  " + inputStyle.Render("/") + " " + d.searchInput.View())
	case ModeInput:
		b.WriteString("\n  " + inputStyle.Render(d.cmdPrompt) + d.cmdInput.View())
	default:
		help := "  [enter] attach  [n]ew  [c]hat  [f]older  [r]ename  [d]one  [x] delete  [/]search  [q]uit"
		b.WriteString(helpStyle.Render(help))
	}

	return b.String()
}

func (d Dashboard) renderItem(item ListItem) string {
	if item.ID == "__separator__" {
		return dimStyle.Render(strings.Repeat("─", 40))
	}
	if item.IsFolder() {
		arrow := "▶"
		if d.isExpanded(item.ID) {
			arrow = "▼"
		}
		name := folderStyle.Render(item.Name)
		progress := ""
		if item.TotalCount > 0 {
			progress = dimStyle.Render(fmt.Sprintf(" %d/%d done", item.DoneCount, item.TotalCount))
		}
		return fmt.Sprintf("%s %s%s", arrow, name, progress)
	}

	// Session
	icon := sessionIcon(item.Status, item.State)

	var stateStr string
	switch {
	case item.State == model.StateWaitingForApproval:
		stateStr = blockedStyle.Render("blocked")
	case item.State == model.StateWorking:
		stateStr = activeStyle.Render("working")
	case item.State == model.StateIdle:
		stateStr = dimStyle.Render("idle")
	case item.Status == model.StatusSuspended:
		stateStr = suspendedStyle.Render("suspended")
	case item.Status == model.StatusDone:
		stateStr = doneStyle.Render("done")
	case item.Status == model.StatusQueued:
		stateStr = dimStyle.Render("queued")
	default:
		stateStr = dimStyle.Render(string(item.Status))
	}

	age := dimStyle.Render(item.Age)

	// Dynamic name width: use available terminal width
	// Layout: cursor(2) + indent(depth*2) + icon(2) + name + state(12) + age(6) + padding(4)
	nameWidth := d.width - 2 - item.Depth*2 - 2 - 12 - 6 - 4
	if nameWidth < 20 {
		nameWidth = 20
	}
	if nameWidth > 80 {
		nameWidth = 80
	}

	name := truncate(item.Name, nameWidth)
	namePad := lipgloss.NewStyle().Width(nameWidth).Render(name)
	statePad := lipgloss.NewStyle().Width(12).Render(stateStr)

	return fmt.Sprintf("%s %s %s %s", icon, namePad, statePad, age)
}

func sessionIcon(status model.SessionStatus, state model.SidecarState) string {
	if state == model.StateWaitingForApproval {
		return blockedStyle.Render("⚠")
	}
	switch status {
	case model.StatusActive:
		return activeStyle.Render("●")
	case model.StatusSuspended:
		return suspendedStyle.Render("⏸")
	case model.StatusDone:
		return doneStyle.Render("✓")
	case model.StatusQueued:
		return dimStyle.Render("◌")
	default:
		return " "
	}
}

// isExpanded checks the live expand state for a folder (not the stale item.Expanded field).
func (d Dashboard) isExpanded(id string) bool {
	if expanded, exists := d.expandState[id]; exists {
		return expanded
	}
	// Default: all folders collapsed
	return false
}

func (d Dashboard) filteredItems() []ListItem {
	// If searching, skip collapse — show all matches
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

	// No search — apply expand/collapse using live expandState
	var visible []ListItem
	skipDepth := -1
	for _, item := range d.items {
		if skipDepth >= 0 && item.Depth > skipDepth {
			continue // hidden by collapsed parent
		}
		skipDepth = -1

		if item.IsFolder() && !d.isExpanded(item.ID) {
			skipDepth = item.Depth
		}

		visible = append(visible, item)
	}
	return visible
}

// AttachSessionID returns the session ID to attach to (set when user presses enter).
func (d Dashboard) AttachSessionID() string {
	return d.attachSessionID
}

// buildItemList creates the flat list of items for display.
func buildItemList(mgr *session.Manager) []ListItem {

	sessions, _ := store.ListSessions(nil)
	folders, _ := store.ListFolders()

	// Build folder lookup
	folderMap := make(map[string]*model.Folder)
	for _, f := range folders {
		folderMap[f.ID] = f
	}

	// Build session lookup
	sessionMap := make(map[string]*model.Session)
	for _, s := range sessions {
		sessionMap[s.ID] = s
	}

	// Build "Recent" section: top N most recently interacted non-done sessions
	const recentMax = 5
	var recentItems []ListItem
	for _, s := range sessions { // already sorted by UpdatedAt desc
		if s.Status == model.StatusArchived || s.Status == model.StatusDone {
			continue
		}
		recentItems = append(recentItems, buildSessionItem(s, 0, mgr))
		if len(recentItems) >= recentMax {
			break
		}
	}

	var doneItems []ListItem

	// First add folders with their children
	usedSessions := make(map[string]bool)

	// itemGroup is a top-level entry (folder or session) with its flattened items
	type itemGroup struct {
		updatedAt time.Time
		items     []ListItem
	}
	var activeGroups []itemGroup

	var doneFolders []*model.Folder
	for _, f := range folders {
		if f.ParentID != "" || f.Status == "archived" {
			continue
		}
		if f.Status == "done" {
			doneFolders = append(doneFolders, f)
			for _, child := range f.Children {
				if child.Type == "session" {
					usedSessions[child.ID] = true
				}
			}
			continue
		}
		items := buildFolderItem(f, 0, sessionMap, folderMap, mgr, usedSessions)
		// Compute most recent UpdatedAt among children
		var maxTime time.Time
		for _, item := range items {
			if item.UpdatedAt.After(maxTime) {
				maxTime = item.UpdatedAt
			}
		}
		// Set folder header's UpdatedAt so it sorts by most recent child
		if len(items) > 0 {
			items[0].UpdatedAt = maxTime
		}
		activeGroups = append(activeGroups, itemGroup{updatedAt: maxTime, items: items})
	}

	// Then add unfiled sessions (no parent)
	for _, s := range sessions {
		if usedSessions[s.ID] || s.Status == model.StatusArchived {
			continue
		}
		if s.ParentID == "" {
			item := buildSessionItem(s, 0, mgr)
			if s.Status == model.StatusDone {
				doneItems = append(doneItems, item)
			} else {
				activeGroups = append(activeGroups, itemGroup{updatedAt: s.UpdatedAt, items: []ListItem{item}})
			}
			usedSessions[s.ID] = true
		}
	}

	// Sort active groups by most recent interaction
	sort.SliceStable(activeGroups, func(i, j int) bool {
		return activeGroups[i].updatedAt.After(activeGroups[j].updatedAt)
	})

	// Prepend recent sessions at the top
	var allItems []ListItem
	if len(recentItems) > 0 {
		allItems = append(allItems, recentItems...)
		// Separator between recent and the rest
		allItems = append(allItems, ListItem{
			Type: ItemFolder,
			ID:   "__separator__",
			Name: "─",
		})
	}

	for _, g := range activeGroups {
		allItems = append(allItems, g.items...)
	}
	activeItems := allItems

	// Add done folders and their children to doneItems
	for _, f := range doneFolders {
		folderItems := buildFolderItem(f, 1, sessionMap, folderMap, mgr, usedSessions)
		doneItems = append(doneItems, folderItems...)
	}

	// Add "Done" virtual folder at the bottom if there are done items
	if len(doneItems) > 0 {
		totalDone := len(doneItems)
		doneFolderItem := ListItem{
			Type:       ItemFolder,
			ID:         "__done__",
			Name:       "Done",
			Depth:      0,
			Expanded:   false, // collapsed by default
			DoneCount:  totalDone,
			TotalCount: totalDone,
		}
		activeItems = append(activeItems, doneFolderItem)
		// Ensure unfiled done sessions have depth 1
		for i := range doneItems {
			if doneItems[i].Depth == 0 {
				doneItems[i].Depth = 1
			}
		}
		activeItems = append(activeItems, doneItems...)
	}

	return activeItems
}

func buildFolderItem(f *model.Folder, depth int, sessionMap map[string]*model.Session, folderMap map[string]*model.Folder, mgr *session.Manager, used map[string]bool) []ListItem {
	doneCount := 0
	totalCount := 0
	for _, child := range f.Children {
		if child.Type == "session" {
			totalCount++
			if s, ok := sessionMap[child.ID]; ok && s.Status == model.StatusDone {
				doneCount++
			}
		}
	}

	items := []ListItem{{
		Type:       ItemFolder,
		ID:         f.ID,
		Name:       f.Name,
		ParentID:   f.ParentID,
		Depth:      depth,
		Expanded:   false, // default collapsed
		DoneCount:  doneCount,
		TotalCount: totalCount,
	}}

	// Add children: active sessions first, then done
	var activeChildren []ListItem
	var doneChildren []ListItem
	for _, child := range f.Children {
		if child.Type == "session" {
			if s, ok := sessionMap[child.ID]; ok && s.Status != model.StatusArchived {
				item := buildSessionItem(s, depth+1, mgr)
				if s.Status == model.StatusDone {
					doneChildren = append(doneChildren, item)
				} else {
					activeChildren = append(activeChildren, item)
				}
				used[s.ID] = true
			}
		} else if child.Type == "folder" {
			if cf, ok := folderMap[child.ID]; ok && cf.Status != "archived" {
				activeChildren = append(activeChildren, buildFolderItem(cf, depth+1, sessionMap, folderMap, mgr, used)...)
			}
		}
	}
	sortItems(activeChildren)
	sortItems(doneChildren)
	items = append(items, activeChildren...)
	items = append(items, doneChildren...)

	return items
}

func buildSessionItem(s *model.Session, depth int, mgr *session.Manager) ListItem {
	state := model.SidecarState("")
	detail := ""

	if s.Status == model.StatusActive {
		socketPath := sidecar.SocketPath(s.ID)
		if session.PingSidecar(socketPath) {
			st, det, err := session.GetLiveStatus(socketPath)
			if err == nil {
				state = st
				detail = det
			}
		} else {
			state = model.SidecarState(s.LastKnownState)
			detail = s.LastKnownDetail
		}
	} else if s.Status == model.StatusSuspended {
		state = model.SidecarState(s.LastKnownState)
		detail = s.LastKnownDetail
	}

	return ListItem{
		Type:      ItemSession,
		ID:        s.ID,
		Name:      s.Name,
		ParentID:  s.ParentID,
		Depth:     depth,
		Status:    s.Status,
		State:     state,
		Detail:    detail,
		Age:       formatAge(s.UpdatedAt),
		UpdatedAt: s.UpdatedAt,
	}
}

func sortItems(items []ListItem) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
}

func formatAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
