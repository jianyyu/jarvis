package watch

import (
	"log"
	"net"
	"time"

	"jarvis/internal/model"
	"jarvis/internal/protocol"
	"jarvis/internal/sidecar"
	"jarvis/internal/store"
)

// ensureFolder finds a folder by name or creates it.
func ensureFolder(name string) (string, error) {
	folders, err := store.ListFolders()
	if err != nil {
		return "", err
	}
	for _, f := range folders {
		if f.Name == name && f.Status == "active" {
			return f.ID, nil
		}
	}

	f := &model.Folder{
		ID:        model.NewID(),
		Type:      "folder",
		Name:      name,
		Status:    "active",
		CreatedAt: time.Now(),
	}
	if err := store.SaveFolder(f); err != nil {
		return "", err
	}
	return f.ID, nil
}

// placeSessionInFolder sets the session's parent and adds it to the folder's children.
func placeSessionInFolder(sessionID, folderID string) {
	sess, err := store.GetSession(sessionID)
	if err != nil {
		return
	}
	sess.ParentID = folderID
	store.SaveSession(sess)

	folder, err := store.GetFolder(folderID)
	if err != nil {
		return
	}
	folder.Children = append(folder.Children, model.ChildRef{Type: "session", ID: sessionID})
	store.SaveFolder(folder)
}

// sendInputToSession writes text to a session's PTY stdin via the sidecar socket.
func sendInputToSession(sessionID, text string) {
	socketPath := sidecar.SocketPath(sessionID)

	time.Sleep(2 * time.Second)

	conn, err := net.DialTimeout("unix", socketPath, 3*time.Second)
	if err != nil {
		log.Printf("watch: sendInput connect failed for %s: %v", sessionID, err)
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))

	codec := protocol.NewCodec(conn)
	if err := codec.Send(protocol.Request{Action: "send_input", Text: text}); err != nil {
		log.Printf("watch: sendInput failed for %s: %v", sessionID, err)
	}
}

// isSidecarAlive checks if a session's sidecar is responding.
func isSidecarAlive(sessionID string) bool {
	socketPath := sidecar.SocketPath(sessionID)
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	codec := protocol.NewCodec(conn)
	if err := codec.Send(protocol.Request{Action: "ping"}); err != nil {
		return false
	}
	var resp protocol.Response
	if err := codec.Receive(&resp); err != nil {
		return false
	}
	return true
}

// waitForSidecar polls until the sidecar socket is alive or timeout.
// Returns true if sidecar became available, false on timeout.
func waitForSidecar(sessionID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if isSidecarAlive(sessionID) {
			return true
		}
		time.Sleep(1 * time.Second)
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
