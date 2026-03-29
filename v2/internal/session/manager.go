package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"jarvis/v2/internal/config"
	"jarvis/v2/internal/model"
	"jarvis/v2/internal/sidecar"
	"jarvis/v2/internal/store"
)

// Manager manages session lifecycle: spawn, attach, detach, resume.
type Manager struct {
	Config *config.Config
}

func NewManager(cfg *config.Config) *Manager {
	return &Manager{Config: cfg}
}

// findSidecarBinary locates the jarvis-sidecar binary.
func findSidecarBinary() (string, error) {
	// Check next to the current executable
	exe, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "jarvis-v2-sidecar")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	// Check PATH
	path, err := exec.LookPath("jarvis-v2-sidecar")
	if err != nil {
		return "", fmt.Errorf("jarvis-v2-sidecar binary not found (place it next to jarvis-v2 or on PATH)")
	}
	return path, nil
}

// Spawn creates a new session and launches a sidecar daemon.
func (m *Manager) Spawn(name string, cwd string, claudeArgs []string) (*model.Session, error) {
	// Resolve to absolute path
	if !filepath.IsAbs(cwd) {
		abs, err := filepath.Abs(cwd)
		if err == nil {
			cwd = abs
		}
	}

	now := time.Now()
	sess := &model.Session{
		ID:        model.NewID(),
		Type:      "session",
		Name:      name,
		Status:    model.StatusActive,
		CWD:       cwd,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := store.SaveSession(sess); err != nil {
		return nil, fmt.Errorf("save session: %w", err)
	}

	// Build the claude command string
	claudeCmd := strings.Join(claudeArgs, " ")

	if err := m.spawnSidecar(sess, claudeCmd); err != nil {
		store.DeleteSession(sess.ID)
		return nil, fmt.Errorf("spawn sidecar: %w", err)
	}

	return sess, nil
}

func (m *Manager) spawnSidecar(sess *model.Session, claudeCmd string) error {
	sidecarBin, err := findSidecarBinary()
	if err != nil {
		return err
	}

	cols, rows := 80, 24
	// Try to get actual terminal size
	if fd := int(os.Stdout.Fd()); fd >= 0 {
		// Best effort — fall back to defaults
		if c, r, err := getTerminalSize(fd); err == nil {
			cols, rows = c, r
		}
	}

	cmd := exec.Command(sidecarBin,
		"--session-id", sess.ID,
		"--cwd", sess.CWD,
		"--claude-cmd", claudeCmd,
		"--cols", fmt.Sprintf("%d", cols),
		"--rows", fmt.Sprintf("%d", rows),
	)

	// Set environment
	env := os.Environ()
	env = append(env, "JARVIS_SESSION_ID="+sess.ID)
	cmd.Env = env
	cmd.Dir = sess.CWD

	// Detach: new session so it survives parent exit
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start sidecar: %w", err)
	}

	pid := cmd.Process.Pid
	cmd.Process.Release() // Don't wait for it

	// Wait for socket to be ready
	socketPath := sidecar.SocketPath(sess.ID)
	ready := false
	for i := 0; i < 50; i++ { // 5 seconds
		time.Sleep(100 * time.Millisecond)
		if PingSidecar(socketPath) {
			ready = true
			break
		}
	}

	if !ready {
		return fmt.Errorf("sidecar did not become ready within 5s")
	}

	// Update session with sidecar info
	now := time.Now()
	sess.Sidecar = &model.SidecarInfo{
		PID:       pid,
		Socket:    socketPath,
		StartedAt: now,
		State:     model.StateWorking,
	}
	sess.UpdatedAt = now
	return store.SaveSession(sess)
}

// Attach connects to a session's sidecar. If the sidecar is dead, it resumes first.
func (m *Manager) Attach(sessionID string) error {
	sess, err := store.GetSession(sessionID)
	if err != nil {
		return fmt.Errorf("get session: %w", err)
	}

	socketPath := sidecar.SocketPath(sess.ID)

	// Check if sidecar is alive
	if !PingSidecar(socketPath) {
		// Try to resume
		if err := m.Resume(sess); err != nil {
			return fmt.Errorf("resume session: %w", err)
		}
		socketPath = sidecar.SocketPath(sess.ID)
	}

	return AttachToSocket(socketPath)
}

// AttachToSocket connects to a socket and enters raw PTY passthrough mode.
func AttachToSocket(socketPath string) error {
	return Attach(socketPath)
}

// Resume restarts a dead sidecar with claude --resume.
func (m *Manager) Resume(sess *model.Session) error {
	// Clean up stale socket
	os.Remove(sidecar.SocketPath(sess.ID))

	// Build resume command
	var claudeArgs []string

	// Try stored session ID first
	claudeSessionID := sess.ClaudeSessionID

	// If not stored, scan disk for the latest session in this CWD
	if claudeSessionID == "" || !SessionIsValid(claudeSessionID, sess.CWD) {
		claudeSessionID = FindLatestSession(sess.CWD)
	}

	if claudeSessionID != "" {
		claudeArgs = []string{"claude", "--resume", claudeSessionID}
		// Store it for next time
		sess.ClaudeSessionID = claudeSessionID
	} else {
		// Can't resume — start fresh
		claudeArgs = []string{"claude"}
	}

	claudeCmd := strings.Join(claudeArgs, " ")

	sess.Status = model.StatusActive
	sess.UpdatedAt = time.Now()
	if err := store.SaveSession(sess); err != nil {
		return err
	}

	return m.spawnSidecar(sess, claudeCmd)
}

// GetStatus returns the status of a session (live from sidecar or derived).
func (m *Manager) GetStatus(sessionID string) (string, string, error) {
	sess, err := store.GetSession(sessionID)
	if err != nil {
		return "", "", err
	}

	socketPath := sidecar.SocketPath(sess.ID)
	if PingSidecar(socketPath) {
		state, detail, err := GetLiveStatus(socketPath)
		if err == nil {
			return string(state), detail, nil
		}
	}

	// Sidecar dead — use last known or derive from JSONL
	if sess.LastKnownState != "" {
		return sess.LastKnownState, sess.LastKnownDetail, nil
	}

	if sess.ClaudeSessionID != "" {
		state, detail, _ := DeriveStatusFromJSONL(sess.ClaudeSessionID, sess.CWD)
		return state, detail, nil
	}

	return "unknown", "", nil
}

// FindSessionByName finds a session by name prefix (case-insensitive).
func FindSessionByName(name string) (*model.Session, error) {
	sessions, err := store.ListSessions(nil)
	if err != nil {
		return nil, err
	}
	name = strings.ToLower(name)
	for _, s := range sessions {
		if strings.ToLower(s.Name) == name || strings.ToLower(s.ID) == name {
			return s, nil
		}
	}
	// Prefix match
	for _, s := range sessions {
		if strings.HasPrefix(strings.ToLower(s.Name), name) || strings.HasPrefix(strings.ToLower(s.ID), name) {
			return s, nil
		}
	}
	return nil, fmt.Errorf("session %q not found", name)
}

func getTerminalSize(fd int) (int, int, error) {
	// Use x/term
	cols, rows, err := func() (int, int, error) {
		// Import is at package level via golang.org/x/term
		// but we avoid import cycle by using syscall directly
		type winsize struct {
			Row    uint16
			Col    uint16
			Xpixel uint16
			Ypixel uint16
		}
		var ws winsize
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TIOCGWINSZ), uintptr(0))
		if errno != 0 {
			return 0, 0, errno
		}
		_ = ws
		return 80, 24, nil // fallback, actual implementation uses term.GetSize
	}()
	return cols, rows, err
}
