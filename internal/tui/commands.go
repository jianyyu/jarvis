package tui

// commands.go — Business-logic commands triggered by user actions in the dashboard.
//
// Each function here returns a tea.Cmd (a function that produces a tea.Msg).
// Bubble Tea runs these asynchronously so the UI stays responsive while I/O
// (spawning processes, writing YAML, killing sidecars) happens in the background.

import (
	"log"
	"net"
	"os"
	"time"

	"jarvis/internal/model"
	"jarvis/internal/protocol"
	"jarvis/internal/session"
	"jarvis/internal/sidecar"
	"jarvis/internal/store"

	tea "github.com/charmbracelet/bubbletea"
)

// createSession spawns a new Claude Code session and immediately attaches.
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

		// Place the new session inside the parent folder (if any).
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

// createChat spawns a quick untitled session and attaches.
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

// createFolder creates a new folder for organising sessions.
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

// renameSession changes a session's display name.
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

// renameFolder changes a folder's display name.
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

// markDone transitions a session to the "done" status.
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

// markFolderDone marks all child sessions as done and the folder itself as "done".
func (d Dashboard) markFolderDone(folderID string) tea.Cmd {
	return func() tea.Msg {
		f, err := store.GetFolder(folderID)
		if err != nil {
			return refreshMsg{items: buildItemList(d.mgr)}
		}

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

		f.Status = "done"
		store.SaveFolder(f)

		return refreshMsg{items: buildItemList(d.mgr)}
	}
}

// deleteSession kills the sidecar (if alive), removes the session from its
// parent folder, and deletes the session's data directory.
func (d Dashboard) deleteSession(sessionID, name string) tea.Cmd {
	return func() tea.Msg {
		killSessionSidecar(sessionID)

		// Remove from parent folder.
		s, err := store.GetSession(sessionID)
		if err == nil && s.ParentID != "" {
			removeChildFromFolder(s.ParentID, "session", sessionID)
		}

		store.DeleteSession(sessionID)
		return refreshMsg{items: buildItemList(d.mgr)}
	}
}

// deleteFolder recursively deletes a folder and all its descendants.
func (d Dashboard) deleteFolder(folderID, name string) tea.Cmd {
	return func() tea.Msg {
		deleteFolderRecursive(folderID)
		return refreshMsg{items: buildItemList(d.mgr)}
	}
}

// deleteFolderRecursive walks a folder tree, killing sidecars and deleting
// every child session and sub-folder before deleting the folder itself.
func deleteFolderRecursive(folderID string) {
	f, err := store.GetFolder(folderID)
	if err != nil {
		return
	}

	for _, child := range f.Children {
		switch child.Type {
		case "session":
			killSessionSidecar(child.ID)
			store.DeleteSession(child.ID)
		case "folder":
			deleteFolderRecursive(child.ID)
		}
	}

	// Remove this folder from its parent (if nested).
	if f.ParentID != "" {
		removeChildFromFolder(f.ParentID, "folder", folderID)
	}

	store.DeleteFolder(folderID)
}

// quickApprove sends "y\n" to a blocked session's sidecar without attaching.
func (d Dashboard) quickApprove(sessionID string) tea.Cmd {
	return func() tea.Msg {
		socketPath := sidecar.SocketPath(sessionID)
		conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
		if err != nil {
			log.Printf("quick-approve: connect failed for %s: %v", sessionID, err)
			return refreshMsg{items: buildItemList(d.mgr)}
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))

		codec := protocol.NewCodec(conn)
		codec.Send(protocol.Request{Action: "send_input", Text: "y\n"})

		return refreshMsg{items: buildItemList(d.mgr)}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────

// killSessionSidecar sends SIGKILL to a session's sidecar if it is still alive.
func killSessionSidecar(sessionID string) {
	socketPath := sidecar.SocketPath(sessionID)
	if !session.PingSidecar(socketPath) {
		return
	}
	s, _ := store.GetSession(sessionID)
	if s != nil && s.Sidecar != nil && s.Sidecar.PID > 0 {
		if p, err := os.FindProcess(s.Sidecar.PID); err == nil {
			p.Signal(os.Kill)
		}
	}
	os.Remove(socketPath)
}

// removeChildFromFolder removes a child reference from a parent folder's Children slice.
func removeChildFromFolder(parentID, childType, childID string) {
	parent, err := store.GetFolder(parentID)
	if err != nil {
		return
	}
	var kept []model.ChildRef
	for _, c := range parent.Children {
		if !(c.Type == childType && c.ID == childID) {
			kept = append(kept, c)
		}
	}
	parent.Children = kept
	store.SaveFolder(parent)
}
