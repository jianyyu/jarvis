package session

import (
	"fmt"
	"net"
	"os"
	"time"

	"jarvis/internal/model"
	"jarvis/internal/protocol"
	"jarvis/internal/sidecar"
	"jarvis/internal/store"
)

// PingSidecar attempts to connect to a sidecar socket and ping it.
func PingSidecar(socketPath string) bool {
	conn, err := net.DialTimeout("unix", socketPath, 500*time.Millisecond)
	if err != nil {
		return false
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	codec := protocol.NewCodec(conn)
	if err := codec.Send(protocol.Request{Action: "ping"}); err != nil {
		return false
	}
	var resp protocol.Response
	if err := codec.Receive(&resp); err != nil {
		return false
	}
	return resp.Event == "pong"
}

// GetLiveStatus gets the status of a running sidecar.
func GetLiveStatus(socketPath string) (model.SidecarState, string, error) {
	conn, err := net.DialTimeout("unix", socketPath, 500*time.Millisecond)
	if err != nil {
		return "", "", err
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	codec := protocol.NewCodec(conn)
	if err := codec.Send(protocol.Request{Action: "get_status"}); err != nil {
		return "", "", err
	}
	var resp protocol.Response
	if err := codec.Receive(&resp); err != nil {
		return "", "", err
	}
	return model.SidecarState(resp.State), resp.Detail, nil
}

// RecoverAllSessions scans all active sessions and marks dead ones as suspended.
func RecoverAllSessions() error {
	sessions, err := store.ListSessions(&store.SessionFilter{
		StatusIn: []model.SessionStatus{model.StatusActive},
	})
	if err != nil {
		return err
	}

	for _, s := range sessions {
		if s.Sidecar == nil {
			continue
		}
		if PingSidecar(s.Sidecar.Socket) {
			continue // sidecar alive
		}

		// Sidecar dead — derive status from JSONL or last known state
		lastState := s.LastKnownState
		lastDetail := s.LastKnownDetail

		if state, detail, err := DeriveStatusFromSession(s); err == nil && state != "unknown" {
			lastState = state
			lastDetail = detail
		}

		s.Status = model.StatusSuspended
		s.LastKnownState = lastState
		s.LastKnownDetail = lastDetail
		s.Sidecar = nil
		now := time.Now()
		s.UpdatedAt = now

		// Clean up stale socket
		os.Remove(sidecar.SocketPath(s.ID))

		if err := store.SaveSession(s); err != nil {
			return fmt.Errorf("update session %s: %w", s.ID, err)
		}
	}
	return nil
}
