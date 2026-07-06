package tui

import (
	"testing"
	"time"

	"jarvis/internal/model"
)

// ts is a small helper for readable, ordered timestamps in tests.
func ts(min int) time.Time {
	return time.Date(2026, 1, 1, 0, min, 0, 0, time.UTC)
}

// TestBuildFolderItems_NestedSubtreeStaysContiguous locks in the fix for the
// tree-scramble bug: a nested folder's child that is newer than its parent's
// sibling sessions must NOT float up among those siblings. Every item must
// appear after its parent and subtrees must stay contiguous.
func TestBuildFolderItems_NestedSubtreeStaysContiguous(t *testing.T) {
	// Layout under "root":
	//   root
	//   ├─ sub          (folder, created @10)
	//   │   └─ deepSess (session, created @99  ← newest of all)
	//   └─ sibSess      (session, created @20)
	//
	// Before the fix, deepSess (@99) sorted above everything and rendered
	// above its own "sub" header. After the fix, the "sub" subtree sorts as a
	// unit by its header's CreatedAt (@10), landing after sibSess (@20).
	root := &model.Folder{
		ID: "root", Type: "folder", Name: "root", Status: "active", CreatedAt: ts(5),
		Children: []model.ChildRef{
			{Type: "folder", ID: "sub"},
			{Type: "session", ID: "sibSess"},
		},
	}
	sub := &model.Folder{
		ID: "sub", Type: "folder", Name: "sub", ParentID: "root", Status: "active", CreatedAt: ts(10),
		Children: []model.ChildRef{
			{Type: "session", ID: "deepSess"},
		},
	}
	folderMap := map[string]*model.Folder{"root": root, "sub": sub}
	sessionMap := map[string]*model.Session{
		"deepSess": {ID: "deepSess", Name: "deepSess", ParentID: "sub", Status: model.StatusQueued, CreatedAt: ts(99)},
		"sibSess":  {ID: "sibSess", Name: "sibSess", ParentID: "root", Status: model.StatusQueued, CreatedAt: ts(20)},
	}

	got := buildFolderItems(root, 0, sessionMap, folderMap, nil, map[string]bool{})

	// Expected order and depth:
	//   root(0), sibSess(1), sub(1), deepSess(2)
	wantIDs := []string{"root", "sibSess", "sub", "deepSess"}
	wantDepth := map[string]int{"root": 0, "sibSess": 1, "sub": 1, "deepSess": 2}

	if len(got) != len(wantIDs) {
		t.Fatalf("got %d items, want %d: %+v", len(got), len(wantIDs), ids(got))
	}
	for i, id := range wantIDs {
		if got[i].ID != id {
			t.Errorf("position %d: got %q, want %q (full order: %v)", i, got[i].ID, id, ids(got))
		}
		if got[i].Depth != wantDepth[id] {
			t.Errorf("item %q: depth %d, want %d", id, got[i].Depth, wantDepth[id])
		}
	}

	// Invariant: no item may render above its own parent folder.
	pos := map[string]int{}
	for i, it := range got {
		pos[it.ID] = i
	}
	if pos["deepSess"] < pos["sub"] {
		t.Errorf("child deepSess (pos %d) rendered above its parent sub (pos %d)", pos["deepSess"], pos["sub"])
	}
}

func ids(items []ListItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}
