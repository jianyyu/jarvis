package tui

import (
	"time"

	"jarvis/internal/model"
)

// ItemType distinguishes folders from sessions in the flat list.
type ItemType int

const (
	ItemFolder  ItemType = iota
	ItemSession
)

// ListItem is a single row in the dashboard, either a folder or session.
type ListItem struct {
	Type     ItemType
	ID       string
	Name     string
	ParentID string // folder ID this item belongs to ("" = top-level)
	Depth    int    // nesting level (0 = top-level)
	Expanded bool   // only for folders

	// Session-only fields
	Status   model.SessionStatus
	State    model.SidecarState
	Detail   string
	Age      string

	// Sorting
	UpdatedAt time.Time

	// Folder-only fields
	DoneCount  int
	TotalCount int
}

func (i ListItem) IsFolder() bool  { return i.Type == ItemFolder }
func (i ListItem) IsSession() bool { return i.Type == ItemSession }
