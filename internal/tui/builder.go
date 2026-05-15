package tui

// builder.go — Constructs the flat list of ListItems for the dashboard.
//
// The dashboard displays a tree of folders and sessions, but Bubble Tea
// works with a flat list.  buildItemList() reads all sessions and folders
// from disk and flattens the tree into a single []ListItem with Depth
// fields indicating nesting.
//
// Layout of the final list:
//
//   ├─ Active folders and unfiled sessions (sorted by CreatedAt, newest first)
//   │   └─ children at Depth+1
//   └─ "Done" virtual folder (collapsed by default)
//       └─ done sessions (regardless of original folder) and done folders at Depth+1
//
// Order is stable: it only changes when sessions/folders are created,
// renamed, or deleted — not on activity-driven UpdatedAt bumps.

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

	// ── Walk top-level folders: separate active from done ──
	var doneItems []ListItem
	usedSessions := make(map[string]bool) // track sessions claimed by a folder

	type itemGroup struct {
		createdAt time.Time
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
		activeGroups = append(activeGroups, itemGroup{createdAt: items[0].CreatedAt, items: items})
	}

	// ── Sessions: done ones go to Done section regardless of parent;
	//    active unfiled ones become their own top-level group. ──
	for _, s := range sessions {
		if usedSessions[s.ID] || s.Status == model.StatusArchived {
			continue
		}
		if s.Status == model.StatusDone {
			doneItems = append(doneItems, buildSessionItem(s, 0, mgr))
			usedSessions[s.ID] = true
			continue
		}
		if s.ParentID == "" {
			item := buildSessionItem(s, 0, mgr)
			activeGroups = append(activeGroups, itemGroup{createdAt: s.CreatedAt, items: []ListItem{item}})
			usedSessions[s.ID] = true
		}
	}

	// ── Sort active groups by CreatedAt (newest first) for stable order ──
	sort.SliceStable(activeGroups, func(i, j int) bool {
		return activeGroups[i].createdAt.After(activeGroups[j].createdAt)
	})

	// ── Assemble final flat list ──
	var allItems []ListItem

	for _, g := range activeGroups {
		allItems = append(allItems, g.items...)
	}

	// Done folders and their children.
	for _, f := range doneFolders {
		folderItems := buildFolderItems(f, 1, sessionMap, folderMap, mgr, usedSessions)
		doneItems = append(doneItems, folderItems...)
	}

	// Sort done items so the newest-completed appear first within Done.
	sortItemsByCreatedAt(doneItems)

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

// buildFolderItems flattens a folder and its active children into ListItems.
// Done sessions are intentionally skipped here — they're collected by
// buildItemList into the top-level "Done" virtual folder.
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
		CreatedAt:  f.CreatedAt,
		DoneCount:  doneCount,
		TotalCount: totalCount,
	}}

	var activeChildren []ListItem
	for _, child := range f.Children {
		switch child.Type {
		case "session":
			s, ok := sessionMap[child.ID]
			if !ok || s.Status == model.StatusArchived {
				continue
			}
			if s.Status == model.StatusDone {
				// Skip — buildItemList collects this into the global Done section.
				continue
			}
			activeChildren = append(activeChildren, buildSessionItem(s, depth+1, mgr))
			used[s.ID] = true

		case "folder":
			cf, ok := folderMap[child.ID]
			if !ok || cf.Status == "archived" {
				continue
			}
			activeChildren = append(activeChildren, buildFolderItems(cf, depth+1, sessionMap, folderMap, mgr, used)...)
		}
	}

	sortItemsByCreatedAt(activeChildren)
	items = append(items, activeChildren...)

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
		CreatedAt: s.CreatedAt,
	}
}

// sortItemsByCreatedAt sorts items with most-recently-created first.
func sortItemsByCreatedAt(items []ListItem) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
}
