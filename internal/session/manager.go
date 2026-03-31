package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"jarvis/internal/config"
	"jarvis/internal/model"
	"jarvis/internal/sidecar"
	"jarvis/internal/store"

	"golang.org/x/term"
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
		candidate := filepath.Join(filepath.Dir(exe), "jarvis-sidecar")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	// Check PATH
	path, err := exec.LookPath("jarvis-sidecar")
	if err != nil {
		return "", fmt.Errorf("jarvis-sidecar binary not found (place it next to jarvis or on PATH)")
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
		ID:          model.NewID(),
		Type:        "session",
		Name:        name,
		Status:      model.StatusActive,
		CWD:         cwd,
		OriginalCWD: cwd,
		CreatedAt:   now,
		UpdatedAt:   now,
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

	// Bump UpdatedAt so recently-attached sessions sort to top
	sess.UpdatedAt = time.Now()
	store.SaveSession(sess)

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

	// Determine the CWD to launch Claude from.
	// If CWD was changed (e.g. by jarvis init to a worktree), launch from
	// the original CWD so claude --resume can find the JSONL, and inject a
	// system prompt telling Claude to work in the worktree.
	launchCWD := sess.CWD
	if sess.OriginalCWD != "" && sess.OriginalCWD != sess.CWD {
		launchCWD = sess.OriginalCWD
	}

	// Try stored session ID first, validating against launchCWD (where the JSONL lives)
	claudeSessionID := sess.ClaudeSessionID
	if claudeSessionID == "" || !SessionIsValid(claudeSessionID, launchCWD) {
		claudeSessionID = FindLatestSession(launchCWD)
	}

	if claudeSessionID != "" {
		claudeArgs = []string{"claude", "--resume", claudeSessionID}
		// Store it for next time
		sess.ClaudeSessionID = claudeSessionID
	} else {
		// Can't resume — start fresh
		claudeArgs = []string{"claude"}
	}

	// If launching from a different CWD than the session's worktree,
	// tell Claude to work in the worktree directory.
	// Quote the prompt value so splitCommand in pty.go keeps it as one arg.
	if launchCWD != sess.CWD {
		prompt := fmt.Sprintf("Your working directory for this session is %s — cd there before making any changes.", sess.CWD)
		claudeArgs = append(claudeArgs,
			"--append-system-prompt",
			fmt.Sprintf("'%s'", prompt))
	}

	claudeCmd := strings.Join(claudeArgs, " ")

	// Temporarily override CWD for sidecar launch
	origCWD := sess.CWD
	sess.CWD = launchCWD
	sess.Status = model.StatusActive
	sess.UpdatedAt = time.Now()
	if err := store.SaveSession(sess); err != nil {
		return err
	}
	err := m.spawnSidecar(sess, claudeCmd)
	// Restore the worktree CWD in the session
	sess.CWD = origCWD
	store.SaveSession(sess)
	return err
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
	return term.GetSize(fd)
}
