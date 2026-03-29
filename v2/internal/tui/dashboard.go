package tui

import (
	"fmt"
	"strings"
	"time"

	"jarvis/v2/internal/config"
	"jarvis/v2/internal/model"
	"jarvis/v2/internal/session"
	"jarvis/v2/internal/sidecar"
	"jarvis/v2/internal/store"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

	return Dashboard{
		cfg:         cfg,
		mgr:         session.NewManager(cfg),
		searchInput: si,
		cmdInput:    ci,
		mode:        ModeDashboard,
	}
}

func (d Dashboard) Init() tea.Cmd {
	return tea.Batch(d.refreshItems(), tickEvery())
}

func tickEvery() tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
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
		visible := d.filteredItems()
		if d.cursor >= len(visible) {
			d.cursor = max(0, len(visible)-1)
		}
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

func (d Dashboard) selectedItem() *ListItem {
	visible := d.filteredItems()
	if d.cursor >= 0 && d.cursor < len(visible) {
		item := visible[d.cursor]
		return &item
	}
	return nil
}

// findAndToggleFolder finds the folder by ID in d.items and toggles its Expanded state.
func (d *Dashboard) toggleFolder(id string) {
	for i := range d.items {
		if d.items[i].ID == id && d.items[i].IsFolder() {
			d.items[i].Expanded = !d.items[i].Expanded
			return
		}
	}
}

func (d Dashboard) handleDashboardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	visible := d.filteredItems()

	switch msg.String() {
	case "q", "ctrl+c":
		return d, tea.Quit

	case "up", "k":
		if d.cursor > 0 {
			d.cursor--
		}

	case "down", "j":
		if d.cursor < len(visible)-1 {
			d.cursor++
		}

	case "enter":
		item := d.selectedItem()
		if item == nil {
			break
		}
		if item.IsSession() && item.Status != model.StatusArchived {
			return d, func() tea.Msg { return attachMsg{sessionID: item.ID} }
		}
		if item.IsFolder() {
			d.toggleFolder(item.ID)
			// Adjust cursor if it would go out of bounds after collapse
			newVisible := d.filteredItems()
			if d.cursor >= len(newVisible) {
				d.cursor = max(0, len(newVisible)-1)
			}
			return d, nil
		}

	case "/":
		d.mode = ModeSearch
		d.searchInput.Focus()
		return d, textinput.Blink

	case "n":
		d.cmdPrompt = "New session name: "
		d.cmdInput.SetValue("")
		d.cmdInput.Focus()
		d.cmdCallback = func(name string) tea.Cmd {
			return d.createSession(name)
		}
		d.mode = ModeInput
		return d, textinput.Blink

	case "f":
		d.cmdPrompt = "New folder name: "
		d.cmdInput.SetValue("")
		d.cmdInput.Focus()
		d.cmdCallback = func(name string) tea.Cmd {
			return d.createFolder(name)
		}
		d.mode = ModeInput
		return d, textinput.Blink

	case "c":
		return d, d.createChat()

	case "a":
		item := d.selectedItem()
		if item != nil && item.IsSession() && item.State == model.StateWaitingForApproval {
			return d, func() tea.Msg { return attachMsg{sessionID: item.ID} }
		}

	case "d":
		item := d.selectedItem()
		if item != nil && item.IsSession() {
			return d, d.markDone(item.ID)
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

func (d Dashboard) createSession(name string) tea.Cmd {
	return func() tea.Msg {
		cwd := d.cfg.RepoPath()
		if cwd == "" {
			cwd = "."
		}
		prompt := fmt.Sprintf("You are working on: %q", name)
		claudeArgs := []string{"claude", "--append-system-prompt", prompt}

		sess, err := d.mgr.Spawn(name, cwd, claudeArgs)
		if err != nil {
			return refreshMsg{items: buildItemList(d.mgr)}
		}
		return attachMsg{sessionID: sess.ID}
	}
}

func (d Dashboard) createChat() tea.Cmd {
	return func() tea.Msg {
		cwd := d.cfg.RepoPath()
		if cwd == "" {
			cwd = "."
		}
		sess, err := d.mgr.Spawn("(untitled chat)", cwd, []string{"claude"})
		if err != nil {
			return refreshMsg{items: buildItemList(d.mgr)}
		}
		return attachMsg{sessionID: sess.ID}
	}
}

func (d Dashboard) createFolder(name string) tea.Cmd {
	return func() tea.Msg {
		now := time.Now()
		f := &model.Folder{
			ID:        model.NewID(),
			Type:      "folder",
			Name:      name,
			Status:    "active",
			CreatedAt: now,
		}
		store.SaveFolder(f)
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

	for i, item := range visibleItems {
		cursor := "  "
		if i == d.cursor {
			cursor = selectedStyle.Render("❯ ")
		}

		indent := strings.Repeat("  ", item.Depth)
		line := d.renderItem(item)

		b.WriteString(cursor + indent + line + "\n")
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
		help := "  [enter] attach  [n]ew  [c]hat  [f]older  [r]ename  [d]one  [/]search  [q]uit"
		b.WriteString(helpStyle.Render(help))
	}

	return b.String()
}

func (d Dashboard) renderItem(item ListItem) string {
	if item.IsFolder() {
		arrow := "▶"
		if item.Expanded {
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
	name := item.Name

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

	detail := ""
	if item.Detail != "" {
		detail = dimStyle.Render(truncate(item.Detail, 35))
	}

	age := dimStyle.Render(item.Age)

	// Format: icon name     state    detail    age
	namePad := lipgloss.NewStyle().Width(28).Render(name)
	statePad := lipgloss.NewStyle().Width(12).Render(stateStr)

	return fmt.Sprintf("%s %s %s %s  %s", icon, namePad, statePad, detail, age)
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

func (d Dashboard) filteredItems() []ListItem {
	// First apply expand/collapse visibility
	var visible []ListItem
	skipDepth := -1 // skip children deeper than this when a folder is collapsed
	for _, item := range d.items {
		if skipDepth >= 0 && item.Depth > skipDepth {
			continue // hidden by collapsed parent
		}
		skipDepth = -1

		if item.IsFolder() && !item.Expanded {
			skipDepth = item.Depth // skip all children
		}

		visible = append(visible, item)
	}

	// Then apply search filter
	if d.searchQuery == "" {
		return visible
	}
	query := strings.ToLower(d.searchQuery)
	var result []ListItem
	for _, item := range visible {
		if strings.Contains(strings.ToLower(item.Name), query) {
			result = append(result, item)
		}
	}
	return result
}

// AttachSessionID returns the session ID to attach to (set when user presses enter).
func (d Dashboard) AttachSessionID() string {
	return d.attachSessionID
}

// buildItemList creates the flat list of items for display.
func buildItemList(mgr *session.Manager) []ListItem {
	session.RecoverAllSessions()

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

	var activeItems []ListItem
	var doneItems []ListItem

	// First add folders with their children
	usedSessions := make(map[string]bool)

	for _, f := range folders {
		if f.ParentID != "" || f.Status == "archived" {
			continue
		}
		items := buildFolderItem(f, 0, sessionMap, folderMap, mgr, usedSessions)
		activeItems = append(activeItems, items...)
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
				activeItems = append(activeItems, item)
			}
			usedSessions[s.ID] = true
		}
	}

	// Sort active: blocked first
	sortItems(activeItems)

	// Add "Done" virtual folder at the bottom if there are done items
	if len(doneItems) > 0 {
		doneFolderItem := ListItem{
			Type:       ItemFolder,
			ID:         "__done__",
			Name:       "Done",
			Depth:      0,
			Expanded:   false, // collapsed by default
			DoneCount:  len(doneItems),
			TotalCount: len(doneItems),
		}
		activeItems = append(activeItems, doneFolderItem)
		// Children are only shown when expanded — handled in filteredItems
		// Store done items so they can be shown when expanded
		for i := range doneItems {
			doneItems[i].Depth = 1
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
		Depth:      depth,
		Expanded:   true, // default expanded
		DoneCount:  doneCount,
		TotalCount: totalCount,
	}}

	// Add children
	for _, child := range f.Children {
		if child.Type == "session" {
			if s, ok := sessionMap[child.ID]; ok && s.Status != model.StatusArchived {
				items = append(items, buildSessionItem(s, depth+1, mgr))
				used[s.ID] = true
			}
		} else if child.Type == "folder" {
			if cf, ok := folderMap[child.ID]; ok && cf.Status != "archived" {
				items = append(items, buildFolderItem(cf, depth+1, sessionMap, folderMap, mgr, used)...)
			}
		}
	}

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
		Type:   ItemSession,
		ID:     s.ID,
		Name:   s.Name,
		Depth:  depth,
		Status: s.Status,
		State:  state,
		Detail: detail,
		Age:    formatAge(s.UpdatedAt),
	}
}

func sortItems(items []ListItem) {
	// Move blocked sessions to top (within same depth level)
	// Simple approach: just ensure blocked items are visible
	// A full tree-aware sort is complex; for now we rely on the build order
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
