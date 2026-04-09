package tui

// builder.go — Constructs the flat list of ListItems for the dashboard.
//
// The dashboard displays a tree of folders and sessions, but Bubble Tea
// works with a flat list.  buildItemList() reads all sessions and folders
// from disk, sorts them, and flattens the tree into a single []ListItem
// with Depth fields indicating nesting.
//
// Layout of the final list:
//
//   ┌─ Recent (top 5 most-recently-touched active sessions)
//   ├─ ────────── separator
//   ├─ Active folders and unfiled sessions (sorted by most recent activity)
//   │   └─ children at Depth+1
//   └─ "Done" virtual folder (collapsed by default)
//       └─ done sessions and done folders at Depth+1

import (
	"sort"
	"time"

	"jarvis/internal/model"
	"jarvis/internal/session"
	"jarvis/internal/sidecar"
	"jarvis/internal/store"
	"jarvis/internal/ui"
)

// buildItemList creates the flat list of items for the dashboard to display.
func buildItemList(mgr *session.Manager) []ListItem {

	sessions, _ := store.ListSessions(nil)
	folders, _ := store.ListFolders()

	// Lookup maps for quick access by ID.
	folderMap := make(map[string]*model.Folder)
	for _, f := range folders {
		folderMap[f.ID] = f
	}
	sessionMap := make(map[string]*model.Session)
	for _, s := range sessions {
		sessionMap[s.ID] = s
	}

	// ── "Recent" section: top N most recently touched active sessions ──
	const recentMax = 5
	var recentItems []ListItem
	for _, s := range sessions { // already sorted by UpdatedAt desc
		if s.Status == model.StatusArchived || s.Status == model.StatusDone {
			continue
		}
		// Skip sessions inside folders — they appear under their folder already.
		if s.ParentID != "" {
			continue
		}
		recentItems = append(recentItems, buildSessionItem(s, 0, mgr))
		if len(recentItems) >= recentMax {
			break
		}
	}

	// ── Walk folders: separate active from done ──
	var doneItems []ListItem
	usedSessions := make(map[string]bool) // track sessions claimed by a folder

	type itemGroup struct {
		updatedAt time.Time
		items     []ListItem
	}
	var activeGroups []itemGroup
	var doneFolders []*model.Folder

	for _, f := range folders {
		if f.ParentID != "" || f.Status == "archived" {
			continue // skip nested or archived folders (handled recursively)
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

		items := buildFolderItems(f, 0, sessionMap, folderMap, mgr, usedSessions)

		// Sort the group by its most-recently-updated child.
		var maxTime time.Time
		for _, item := range items {
			if item.UpdatedAt.After(maxTime) {
				maxTime = item.UpdatedAt
			}
		}
		if len(items) > 0 {
			items[0].UpdatedAt = maxTime
		}
		activeGroups = append(activeGroups, itemGroup{updatedAt: maxTime, items: items})
	}

	// ── Unfiled sessions (not inside any folder) ──
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

	// ── Sort active groups by most recent interaction ──
	sort.SliceStable(activeGroups, func(i, j int) bool {
		return activeGroups[i].updatedAt.After(activeGroups[j].updatedAt)
	})

	// ── Assemble final flat list ──
	var allItems []ListItem

	// Recent section at the top.
	if len(recentItems) > 0 {
		allItems = append(allItems, recentItems...)
		allItems = append(allItems, ListItem{
			Type: ItemFolder,
			ID:   "__separator__",
			Name: "─",
		})
	}

	// Active folders and sessions.
	for _, g := range activeGroups {
		allItems = append(allItems, g.items...)
	}

	// Done folders and their children.
	for _, f := range doneFolders {
		folderItems := buildFolderItems(f, 1, sessionMap, folderMap, mgr, usedSessions)
		doneItems = append(doneItems, folderItems...)
	}

	// Virtual "Done" folder at the bottom.
	if len(doneItems) > 0 {
		totalDone := len(doneItems)
		allItems = append(allItems, ListItem{
			Type:       ItemFolder,
			ID:         "__done__",
			Name:       "Done",
			Depth:      0,
			Expanded:   false,
			DoneCount:  totalDone,
			TotalCount: totalDone,
		})
		for i := range doneItems {
			if doneItems[i].Depth == 0 {
				doneItems[i].Depth = 1
			}
		}
		allItems = append(allItems, doneItems...)
	}

	return allItems
}

// buildFolderItems flattens a folder and its children into ListItems.
func buildFolderItems(f *model.Folder, depth int, sessionMap map[string]*model.Session, folderMap map[string]*model.Folder, mgr *session.Manager, used map[string]bool) []ListItem {
	// Count done vs total children for the progress indicator.
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
		Expanded:   false, // actual expand state is applied by the dashboard
		DoneCount:  doneCount,
		TotalCount: totalCount,
	}}

	// Children: active first, then done.
	var activeChildren []ListItem
	var doneChildren []ListItem
	for _, child := range f.Children {
		switch child.Type {
		case "session":
			s, ok := sessionMap[child.ID]
			if !ok || s.Status == model.StatusArchived {
				continue
			}
			item := buildSessionItem(s, depth+1, mgr)
			if s.Status == model.StatusDone {
				doneChildren = append(doneChildren, item)
			} else {
				activeChildren = append(activeChildren, item)
			}
			used[s.ID] = true

		case "folder":
			cf, ok := folderMap[child.ID]
			if !ok || cf.Status == "archived" {
				continue
			}
			activeChildren = append(activeChildren, buildFolderItems(cf, depth+1, sessionMap, folderMap, mgr, used)...)
		}
	}

	sortItemsByTime(activeChildren)
	sortItemsByTime(doneChildren)
	items = append(items, activeChildren...)
	items = append(items, doneChildren...)

	return items
}

// buildSessionItem creates a ListItem for a single session, querying its
// live sidecar status if the session is active.
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
		Age:       ui.FormatAge(s.UpdatedAt),
		UpdatedAt: s.UpdatedAt,
	}
}

// sortItemsByTime sorts items with most-recently-updated first.
func sortItemsByTime(items []ListItem) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
}
