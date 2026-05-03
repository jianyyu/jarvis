package session

import (
	"encoding/json"
	"fmt"
	"log"
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
		ID:        model.NewID(),
		Type:      "session",
		Name:      name,
		Status:    model.StatusActive,
		LaunchDir: cwd,
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

	// Wire up Claude Code's SessionStart hook so it pushes the canonical
	// Claude session UUID back to our sidecar, replacing the racy snapshot+poll
	// detection. See docs/session-id-detection-via-hook.md.
	settingsPath, err := writeClaudeSettings(sess.ID)
	if err != nil {
		return fmt.Errorf("write claude settings: %w", err)
	}
	claudeCmd = injectClaudeSettings(claudeCmd, settingsPath)

	cols, rows := 80, 24
	// Try to get actual terminal size
	if fd := int(os.Stdout.Fd()); fd >= 0 {
		// Best effort — fall back to defaults
		if c, r, err := getTerminalSize(fd); err == nil {
			cols, rows = c, r
		}
	}

	args := []string{
		"--session-id", sess.ID,
		"--cwd", sess.LaunchDir,
		"--claude-cmd", claudeCmd,
		"--cols", fmt.Sprintf("%d", cols),
		"--rows", fmt.Sprintf("%d", rows),
	}
	// Pass known Claude session ID so the sidecar can log it on startup; the
	// authoritative id still arrives via the SessionStart hook handler.
	if sess.ClaudeSessionID != "" {
		args = append(args, "--claude-session-id", sess.ClaudeSessionID)
	}

	cmd := exec.Command(sidecarBin, args...)

	// Set environment
	env := os.Environ()
	env = append(env, "JARVIS_SESSION_ID="+sess.ID)
	cmd.Env = env
	cmd.Dir = sess.LaunchDir

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

	workspaceDir := sess.WorkspaceDir()
	launchDir := sess.LaunchDir

	// Use stored session ID if valid. If no stored ID exists or it's invalid,
	// start fresh rather than guessing — FindLatestSession is unreliable when
	// multiple jarvis sessions share the same project directory.
	claudeSessionID := sess.ClaudeSessionID
	if claudeSessionID != "" {
		if !SessionIsValid(claudeSessionID, launchDir) && !SessionIsValid(claudeSessionID, workspaceDir) {
			log.Printf("session: stored Claude session %s is invalid, starting fresh", claudeSessionID)
			claudeSessionID = ""
		}
	} else {
		log.Printf("session: no stored Claude session ID, starting fresh")
	}

	if claudeSessionID != "" {
		claudeArgs = []string{"claude", "--resume", claudeSessionID}
	} else {
		// Can't resume — start fresh. Clear the stale ID so it isn't
		// persisted back to disk.
		sess.ClaudeSessionID = ""
		claudeArgs = []string{"claude"}
	}

	// LaunchDir vs worktree: Claude must start in LaunchDir for JSONL; user edits in workspaceDir.
	// Quote the prompt value so splitCommand in pty.go keeps it as one arg.
	if launchDir != workspaceDir {
		prompt := fmt.Sprintf("Your working directory for this session is %s — cd there before making any changes.", workspaceDir)
		claudeArgs = append(claudeArgs,
			"--append-system-prompt",
			fmt.Sprintf("'%s'", prompt))
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
		state, detail, _ := DeriveStatusFromSession(sess)
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

// writeClaudeSettings emits a per-session settings JSON file that registers a
// SessionStart hook pointing back at `jarvis hook-relay SessionStart`. Returns
// the absolute path to the file, suitable for passing to `claude --settings`.
//
// Resolved from os.Executable() so dev builds and prod installs both work.
// On any failure to locate the jarvis binary, returns an error and leaves the
// file unwritten — the caller will surface the error and abort spawn.
func writeClaudeSettings(sessionID string) (string, error) {
	jarvisBin, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve jarvis executable: %w", err)
	}
	if abs, err := filepath.EvalSymlinks(jarvisBin); err == nil {
		jarvisBin = abs
	}

	settings := map[string]any{
		"hooks": map[string]any{
			"SessionStart": []map[string]any{
				{
					"matcher": "",
					"hooks": []map[string]any{
						{
							"type":    "command",
							"command": shellQuote(jarvisBin) + " hook-relay SessionStart",
						},
					},
				},
			},
		},
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal settings: %w", err)
	}

	dir := filepath.Join(store.JarvisHome(), "sessions", sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "claude-settings.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// injectClaudeSettings inserts `--settings <path>` immediately after the
// `claude` executable in a command line such as
//
//	claude --resume <id> --append-system-prompt '...'
//
// The settings path is single-quoted so splitCommand in pty.go preserves it
// as one argument (matches how --append-system-prompt is already escaped).
func injectClaudeSettings(claudeCmd, settingsPath string) string {
	quoted := "'" + settingsPath + "'"
	parts := strings.SplitN(claudeCmd, " ", 2)
	if len(parts) == 1 {
		return parts[0] + " --settings " + quoted
	}
	return parts[0] + " --settings " + quoted + " " + parts[1]
}

// shellQuote wraps a path in single quotes for embedding inside a hook
// command string (which Claude Code executes via its own shell). Single
// quotes inside the path are escaped using the standard '"'"' trick.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
