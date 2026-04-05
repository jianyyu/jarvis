// Integration tests for Jarvis.
//
// These tests build the real jarvis-sidecar and mock_claude binaries, then
// exercise the full session lifecycle: spawn a sidecar, communicate over
// Unix sockets, verify status transitions, and test recovery after crashes.
//
// Run with:  go test -v -count=1 -timeout 120s ./...
//
// The tests use JARVIS_HOME pointed at a temp dir, so they never touch
// your real ~/.jarvis data.
package jarvis_test

import (
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"jarvis/internal/model"
	"jarvis/internal/protocol"
	"jarvis/internal/session"
	"jarvis/internal/sidecar"
	"jarvis/internal/store"
)

// binDir holds the path to the temp directory containing built test binaries.
// Set once in TestMain.
var binDir string

func TestMain(m *testing.M) {
	// Build test binaries into a temp directory.
	dir, err := os.MkdirTemp("", "jarvis-integration-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp dir: %v\n", err)
		os.Exit(1)
	}
	binDir = dir

	// Build jarvis-sidecar (the real daemon binary).
	sidecarBin := filepath.Join(dir, "jarvis-sidecar")
	build := exec.Command("go", "build", "-o", sidecarBin, "./cmd/sidecar/")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build jarvis-sidecar: %v\n", err)
		os.Exit(1)
	}

	// Build mock_claude (simulates a Claude Code session).
	mockBin := filepath.Join(dir, "claude")
	build = exec.Command("go", "build", "-o", mockBin, "./testdata/mock_claude.go")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build mock_claude: %v\n", err)
		os.Exit(1)
	}

	// Build the jarvis CLI binary for CLI-level tests.
	jBin := filepath.Join(dir, "jarvis")
	build = exec.Command("go", "build", "-o", jBin, "./cmd/jarvis/")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build jarvis: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	os.RemoveAll(dir)
	os.Exit(code)
}

// testEnv sets up an isolated JARVIS_HOME and PATH so the sidecar binary
// and mock claude are found. Returns a cleanup function.
func testEnv(t *testing.T) (jarvisHome string, cleanup func()) {
	t.Helper()
	tmp := t.TempDir()

	origHome := os.Getenv("JARVIS_HOME")
	origPath := os.Getenv("PATH")

	os.Setenv("JARVIS_HOME", tmp)
	// Put our test binaries first on PATH so "claude" resolves to mock_claude.
	os.Setenv("PATH", binDir+":"+origPath)

	return tmp, func() {
		os.Setenv("JARVIS_HOME", origHome)
		os.Setenv("PATH", origPath)
	}
}

// waitForSocket polls until the sidecar socket is ready or timeout.
func waitForSocket(socketPath string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socketPath, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// pingSocket sends a ping and expects a pong.
func pingSocket(t *testing.T, socketPath string) {
	t.Helper()
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("connect to socket: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	codec := protocol.NewCodec(conn)
	if err := codec.Send(protocol.Request{Action: "ping"}); err != nil {
		t.Fatalf("send ping: %v", err)
	}
	var resp protocol.Response
	if err := codec.Receive(&resp); err != nil {
		t.Fatalf("receive pong: %v", err)
	}
	if resp.Event != "pong" {
		t.Fatalf("expected pong, got %q", resp.Event)
	}
}

// getStatus queries the sidecar for its current state.
func getStatus(t *testing.T, socketPath string) (state, detail string) {
	t.Helper()
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	codec := protocol.NewCodec(conn)
	codec.Send(protocol.Request{Action: "get_status"})
	var resp protocol.Response
	if err := codec.Receive(&resp); err != nil {
		t.Fatalf("receive status: %v", err)
	}
	return resp.State, resp.Detail
}

// spawnSidecar starts a sidecar daemon for a session and waits for it to be ready.
// Returns the session and the PID of the sidecar process.
func spawnSidecar(t *testing.T, sessionID, name, claudeCmd string) (*model.Session, int) {
	t.Helper()
	now := time.Now()
	sess := &model.Session{
		ID:        sessionID,
		Type:      "session",
		Name:      name,
		Status:    model.StatusActive,
		LaunchDir: t.TempDir(),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.SaveSession(sess); err != nil {
		t.Fatalf("save session: %v", err)
	}

	sidecarBin := filepath.Join(binDir, "jarvis-sidecar")
	socketPath := sidecar.SocketPath(sessionID)

	cmd := exec.Command(sidecarBin,
		"--session-id", sessionID,
		"--cwd", sess.LaunchDir,
		"--claude-cmd", claudeCmd,
		"--cols", "80",
		"--rows", "24",
	)
	cmd.Env = append(os.Environ(), "JARVIS_SESSION_ID="+sessionID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start sidecar: %v", err)
	}
	pid := cmd.Process.Pid
	cmd.Process.Release()

	if !waitForSocket(socketPath, 10*time.Second) {
		t.Fatalf("sidecar socket never became ready at %s", socketPath)
	}

	// Update session with sidecar info.
	sess.Sidecar = &model.SidecarInfo{
		PID:       pid,
		Socket:    socketPath,
		StartedAt: time.Now(),
		State:     model.StateWorking,
	}
	store.SaveSession(sess)

	return sess, pid
}

// killSidecar sends SIGTERM to a sidecar process and waits for its socket to disappear.
func killSidecar(t *testing.T, pid int, socketPath string) {
	t.Helper()
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	proc.Signal(syscall.SIGTERM)

	// Wait for socket to go away.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); os.IsNotExist(err) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Force kill if still alive.
	proc.Signal(syscall.SIGKILL)
	os.Remove(socketPath)
}

// =============================================================================
// Tests
// =============================================================================

// TestSidecarLifecycle exercises the core sidecar flow: spawn, ping, status,
// send input, receive output, and process exit.
func TestSidecarLifecycle(t *testing.T) {
	_, cleanup := testEnv(t)
	defer cleanup()

	// mock_claude reads one line, echoes it, then waits for more.
	sess, pid := spawnSidecar(t, "lifecycle-01", "lifecycle test", "claude")
	socketPath := sidecar.SocketPath(sess.ID)
	defer killSidecar(t, pid, socketPath)

	// 1. Ping
	pingSocket(t, socketPath)

	// 2. Status should be working or idle (mock_claude prints a banner immediately).
	state, _ := getStatus(t, socketPath)
	if state != "working" && state != "idle" {
		t.Errorf("expected working or idle after spawn, got %q", state)
	}

	// 3. Attach, send input, and read output.
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("connect for attach: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	codec := protocol.NewCodec(conn)

	// Attach — we should get a buffer event with mock_claude's startup banner.
	codec.Send(protocol.Request{Action: "attach"})
	var resp protocol.Response
	if err := codec.Receive(&resp); err != nil {
		t.Fatalf("receive buffer: %v", err)
	}
	if resp.Event != "buffer" {
		t.Fatalf("expected buffer event on attach, got %q", resp.Event)
	}
	bufData, _ := base64.StdEncoding.DecodeString(resp.Data)
	if !strings.Contains(string(bufData), "Mock Claude Code") {
		t.Errorf("buffer should contain mock_claude banner, got: %q", string(bufData))
	}

	// 4. Send input and expect echoed output.
	codec.Send(protocol.Request{Action: "send_input", Text: "hello integration\n"})

	// Read output events until we see our echoed text.
	found := false
	for i := 0; i < 20; i++ {
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		var out protocol.Response
		if err := codec.Receive(&out); err != nil {
			break
		}
		if out.Event == "output" {
			decoded, _ := base64.StdEncoding.DecodeString(out.Data)
			if strings.Contains(string(decoded), "Echo: hello integration") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("never received echoed output from mock_claude")
	}

	// 5. Tell mock_claude to exit, expect session_ended event.
	codec.Send(protocol.Request{Action: "send_input", Text: "exit\n"})

	ended := false
	for i := 0; i < 20; i++ {
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		var out protocol.Response
		if err := codec.Receive(&out); err != nil {
			break
		}
		if out.Event == "session_ended" {
			ended = true
			if out.ExitCode != 0 {
				t.Errorf("expected exit code 0, got %d", out.ExitCode)
			}
			break
		}
	}
	if !ended {
		t.Error("never received session_ended event")
	}
}

// TestSidecarApprovalDetection verifies that the sidecar detects approval
// prompts from Claude Code and transitions to waiting_for_approval.
func TestSidecarApprovalDetection(t *testing.T) {
	_, cleanup := testEnv(t)
	defer cleanup()

	sess, pid := spawnSidecar(t, "approval-01", "approval test", "claude")
	socketPath := sidecar.SocketPath(sess.ID)
	defer killSidecar(t, pid, socketPath)

	// Wait for mock_claude to settle.
	time.Sleep(500 * time.Millisecond)

	// Attach so we can send input.
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()
	codec := protocol.NewCodec(conn)
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	codec.Send(protocol.Request{Action: "attach"})

	// Drain the initial buffer event.
	var buf protocol.Response
	codec.Receive(&buf)

	// Send the "approve" command which triggers an approval prompt.
	codec.Send(protocol.Request{Action: "send_input", Text: "approve\n"})

	// Wait for the sidecar to detect the approval pattern.
	time.Sleep(1 * time.Second)

	// Check status via a separate connection.
	state, _ := getStatus(t, socketPath)
	if state != "waiting_for_approval" {
		t.Errorf("expected waiting_for_approval after approval prompt, got %q", state)
	}

	// Answer the prompt to unblock.
	codec.Send(protocol.Request{Action: "send_input", Text: "y\n"})
	time.Sleep(1 * time.Second)

	state, _ = getStatus(t, socketPath)
	if state == "waiting_for_approval" {
		t.Error("should have left waiting_for_approval after answering y")
	}

	// Clean up.
	codec.Send(protocol.Request{Action: "send_input", Text: "exit\n"})
}

// TestSidecarCrashAndRecovery kills a sidecar, verifies the session is marked
// suspended, then verifies RecoverAllSessions does the right thing.
func TestSidecarCrashAndRecovery(t *testing.T) {
	_, cleanup := testEnv(t)
	defer cleanup()

	sess, pid := spawnSidecar(t, "crash-01", "crash test", "claude")
	socketPath := sidecar.SocketPath(sess.ID)

	// Verify it's alive.
	pingSocket(t, socketPath)

	// Kill the sidecar abruptly.
	killSidecar(t, pid, socketPath)

	// The sidecar should have persisted suspended status on exit.
	// Give it a moment to write the final state.
	time.Sleep(500 * time.Millisecond)

	// Run recovery — should mark the session as suspended.
	if err := session.RecoverAllSessions(); err != nil {
		t.Fatalf("RecoverAllSessions: %v", err)
	}

	// Reload session from disk.
	recovered, err := store.GetSession(sess.ID)
	if err != nil {
		t.Fatalf("get session after recovery: %v", err)
	}
	if recovered.Status != model.StatusSuspended {
		t.Errorf("expected status suspended after recovery, got %q", recovered.Status)
	}
	if recovered.Sidecar != nil {
		t.Error("sidecar info should be nil after recovery")
	}
}

// TestMultipleSessions verifies that multiple sidecars can run concurrently
// without interfering with each other.
func TestMultipleSessions(t *testing.T) {
	_, cleanup := testEnv(t)
	defer cleanup()

	const N = 3
	type sessionInfo struct {
		sess       *model.Session
		pid        int
		socketPath string
	}
	var sessions []sessionInfo

	// Spawn N sessions.
	for i := 0; i < N; i++ {
		id := fmt.Sprintf("multi-%02d", i)
		sess, pid := spawnSidecar(t, id, fmt.Sprintf("session %d", i), "claude")
		sp := sidecar.SocketPath(id)
		sessions = append(sessions, sessionInfo{sess, pid, sp})
	}

	// Cleanup all at the end.
	defer func() {
		for _, s := range sessions {
			killSidecar(t, s.pid, s.socketPath)
		}
	}()

	// All should be pingable.
	for _, s := range sessions {
		pingSocket(t, s.socketPath)
	}

	// Send different input to each and verify independent output.
	for i, s := range sessions {
		conn, err := net.DialTimeout("unix", s.socketPath, 2*time.Second)
		if err != nil {
			t.Fatalf("session %d: connect: %v", i, err)
		}
		codec := protocol.NewCodec(conn)
		conn.SetDeadline(time.Now().Add(5 * time.Second))

		codec.Send(protocol.Request{Action: "attach"})
		var buf protocol.Response
		codec.Receive(&buf) // drain buffer

		msg := fmt.Sprintf("hello from session %d\n", i)
		codec.Send(protocol.Request{Action: "send_input", Text: msg})

		found := false
		for j := 0; j < 15; j++ {
			conn.SetDeadline(time.Now().Add(2 * time.Second))
			var out protocol.Response
			if err := codec.Receive(&out); err != nil {
				break
			}
			if out.Event == "output" {
				decoded, _ := base64.StdEncoding.DecodeString(out.Data)
				if strings.Contains(string(decoded), fmt.Sprintf("Echo: hello from session %d", i)) {
					found = true
					break
				}
			}
		}
		if !found {
			t.Errorf("session %d: never got echo back", i)
		}
		conn.Close()
	}
}

// TestSessionStatePersistence verifies that the sidecar's periodic state
// persistence actually writes to disk.
func TestSessionStatePersistence(t *testing.T) {
	_, cleanup := testEnv(t)
	defer cleanup()

	sess, pid := spawnSidecar(t, "persist-01", "persist test", "claude")
	socketPath := sidecar.SocketPath(sess.ID)
	defer killSidecar(t, pid, socketPath)

	// The sidecar persists state every 5 seconds. Wait for at least one cycle.
	time.Sleep(6 * time.Second)

	persisted, err := store.GetSession(sess.ID)
	if err != nil {
		t.Fatalf("read persisted session: %v", err)
	}

	// LastKnownState should have been set by the persist loop.
	if persisted.LastKnownState == "" {
		t.Error("expected LastKnownState to be set after persist cycle")
	}
	if persisted.LastActivityAt == nil {
		t.Error("expected LastActivityAt to be set after persist cycle")
	}
}

// TestGetBuffer verifies the get_buffer command returns recent output.
func TestGetBuffer(t *testing.T) {
	_, cleanup := testEnv(t)
	defer cleanup()

	sess, pid := spawnSidecar(t, "buffer-01", "buffer test", "claude")
	socketPath := sidecar.SocketPath(sess.ID)
	defer killSidecar(t, pid, socketPath)

	// Wait for mock_claude to print its banner.
	time.Sleep(500 * time.Millisecond)

	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	codec := protocol.NewCodec(conn)
	codec.Send(protocol.Request{Action: "get_buffer", Lines: 50})

	var resp protocol.Response
	if err := codec.Receive(&resp); err != nil {
		t.Fatalf("receive buffer: %v", err)
	}
	if resp.Event != "buffer" {
		t.Fatalf("expected buffer event, got %q", resp.Event)
	}

	decoded, _ := base64.StdEncoding.DecodeString(resp.Data)
	if !strings.Contains(string(decoded), "Mock Claude Code") {
		t.Errorf("buffer should contain mock_claude banner, got: %q", string(decoded))
	}
}

// TestFindSessionByName tests the session name lookup with exact and prefix matching.
func TestFindSessionByName(t *testing.T) {
	_, cleanup := testEnv(t)
	defer cleanup()

	now := time.Now()
	for _, name := range []string{"fix auth bug", "refactor database", "fix typo in readme"} {
		s := &model.Session{
			ID: model.NewID(), Type: "session", Name: name,
			Status: model.StatusActive, LaunchDir: "/tmp", CreatedAt: now, UpdatedAt: now,
		}
		store.SaveSession(s)
	}

	// Exact match.
	s, err := session.FindSessionByName("fix auth bug")
	if err != nil {
		t.Fatalf("exact match: %v", err)
	}
	if s.Name != "fix auth bug" {
		t.Errorf("got %q, want %q", s.Name, "fix auth bug")
	}

	// Prefix match.
	s, err = session.FindSessionByName("refactor")
	if err != nil {
		t.Fatalf("prefix match: %v", err)
	}
	if s.Name != "refactor database" {
		t.Errorf("got %q, want %q", s.Name, "refactor database")
	}

	// Case-insensitive.
	s, err = session.FindSessionByName("FIX AUTH BUG")
	if err != nil {
		t.Fatalf("case-insensitive: %v", err)
	}
	if s.Name != "fix auth bug" {
		t.Errorf("got %q, want %q", s.Name, "fix auth bug")
	}

	// Not found.
	_, err = session.FindSessionByName("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent session")
	}
}

// TestCLILs runs "jarvis ls" as a subprocess and verifies the output.
func TestCLILs(t *testing.T) {
	jarvisHome, cleanup := testEnv(t)
	defer cleanup()

	// Create a couple of sessions on disk.
	now := time.Now()
	for _, name := range []string{"session alpha", "session beta"} {
		s := &model.Session{
			ID: model.NewID(), Type: "session", Name: name,
			Status: model.StatusActive, LaunchDir: "/tmp", CreatedAt: now, UpdatedAt: now,
		}
		store.SaveSession(s)
	}

	jarvisBin := filepath.Join(binDir, "jarvis")
	cmd := exec.Command(jarvisBin, "ls")
	cmd.Env = append(os.Environ(),
		"JARVIS_HOME="+jarvisHome,
		"PATH="+binDir+":"+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jarvis ls failed: %v\noutput: %s", err, string(out))
	}

	output := string(out)
	if !strings.Contains(output, "session alpha") {
		t.Errorf("output should contain 'session alpha':\n%s", output)
	}
	if !strings.Contains(output, "session beta") {
		t.Errorf("output should contain 'session beta':\n%s", output)
	}
}

// TestCLIDoneAndRm tests the "jarvis done" and "jarvis rm" commands.
func TestCLIDoneAndRm(t *testing.T) {
	jarvisHome, cleanup := testEnv(t)
	defer cleanup()

	now := time.Now()
	sess := &model.Session{
		ID: "done-rm-01", Type: "session", Name: "done-test",
		Status: model.StatusSuspended, LaunchDir: "/tmp", CreatedAt: now, UpdatedAt: now,
	}
	store.SaveSession(sess)

	jarvisBin := filepath.Join(binDir, "jarvis")
	env := append(os.Environ(),
		"JARVIS_HOME="+jarvisHome,
		"PATH="+binDir+":"+os.Getenv("PATH"),
	)

	// Mark as done.
	cmd := exec.Command(jarvisBin, "done", "done-test")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jarvis done: %v\noutput: %s", err, string(out))
	}

	loaded, _ := store.GetSession("done-rm-01")
	if loaded.Status != model.StatusDone {
		t.Errorf("expected done, got %q", loaded.Status)
	}

	// Delete it.
	cmd = exec.Command(jarvisBin, "rm", "done-test")
	cmd.Env = env
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jarvis rm: %v\noutput: %s", err, string(out))
	}

	_, err = store.GetSession("done-rm-01")
	if err == nil {
		t.Error("session should be deleted after rm")
	}
}

// TestCLIStatus runs "jarvis status <name>" and verifies the output.
func TestCLIStatus(t *testing.T) {
	jarvisHome, cleanup := testEnv(t)
	defer cleanup()

	now := time.Now()
	sess := &model.Session{
		ID: "status-01", Type: "session", Name: "status-test",
		Status: model.StatusActive, LaunchDir: "/tmp/test-cwd",
		CreatedAt: now, UpdatedAt: now,
	}
	store.SaveSession(sess)

	jarvisBin := filepath.Join(binDir, "jarvis")
	cmd := exec.Command(jarvisBin, "status", "status-test")
	cmd.Env = append(os.Environ(),
		"JARVIS_HOME="+jarvisHome,
		"PATH="+binDir+":"+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jarvis status: %v\noutput: %s", err, string(out))
	}

	output := string(out)
	if !strings.Contains(output, "status-test") {
		t.Errorf("output should contain session name:\n%s", output)
	}
	if !strings.Contains(output, "status-01") {
		t.Errorf("output should contain session ID:\n%s", output)
	}
	if !strings.Contains(output, "/tmp/test-cwd") {
		t.Errorf("output should contain launch dir:\n%s", output)
	}
}

// TestSidecarProcessExitMarksSessionSuspended verifies that when mock_claude
// exits, the sidecar marks the session as suspended in the YAML file.
func TestSidecarProcessExitMarksSessionSuspended(t *testing.T) {
	_, cleanup := testEnv(t)
	defer cleanup()

	sess, _ := spawnSidecar(t, "exit-01", "exit test", "claude")
	socketPath := sidecar.SocketPath(sess.ID)

	// Connect and tell mock_claude to exit.
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	codec := protocol.NewCodec(conn)
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	codec.Send(protocol.Request{Action: "attach"})
	var buf protocol.Response
	codec.Receive(&buf) // drain buffer

	codec.Send(protocol.Request{Action: "send_input", Text: "exit\n"})

	// Wait for session_ended.
	for i := 0; i < 20; i++ {
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		var resp protocol.Response
		if err := codec.Receive(&resp); err != nil {
			break
		}
		if resp.Event == "session_ended" {
			break
		}
	}
	conn.Close()

	// Give the sidecar time to persist final state.
	time.Sleep(1 * time.Second)

	// Session should be suspended on disk.
	final, err := store.GetSession(sess.ID)
	if err != nil {
		t.Fatalf("read final session: %v", err)
	}
	if final.Status != model.StatusSuspended {
		t.Errorf("expected suspended after exit, got %q", final.Status)
	}
	if final.LastKnownState != "exited" {
		t.Errorf("expected LastKnownState=exited, got %q", final.LastKnownState)
	}
}

// TestSidecarCrashExitCode verifies that a crash (non-zero exit) is recorded.
func TestSidecarCrashExitCode(t *testing.T) {
	_, cleanup := testEnv(t)
	defer cleanup()

	sess, _ := spawnSidecar(t, "crash-exit-01", "crash exit test", "claude")
	socketPath := sidecar.SocketPath(sess.ID)

	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	codec := protocol.NewCodec(conn)
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	codec.Send(protocol.Request{Action: "attach"})
	var buf protocol.Response
	codec.Receive(&buf)

	// "crash" causes mock_claude to os.Exit(1).
	codec.Send(protocol.Request{Action: "send_input", Text: "crash\n"})

	gotEnded := false
	for i := 0; i < 20; i++ {
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		var resp protocol.Response
		if err := codec.Receive(&resp); err != nil {
			break
		}
		if resp.Event == "session_ended" {
			gotEnded = true
			if resp.ExitCode == 0 {
				t.Error("expected non-zero exit code for crash")
			}
			break
		}
	}
	conn.Close()

	if !gotEnded {
		t.Error("never received session_ended event after crash")
	}

	time.Sleep(1 * time.Second)
	final, _ := store.GetSession(sess.ID)
	if final.LastKnownState != "exited" {
		t.Errorf("expected exited, got %q", final.LastKnownState)
	}
}

// writeConfig writes a config.yaml with auto-approve policies to the test JARVIS_HOME.
func writeConfig(t *testing.T, jarvisHome, yaml string) {
	t.Helper()
	path := filepath.Join(jarvisHome, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// TestAutoApproveMatchingPolicy verifies that when a policy matches an approval
// prompt, the sidecar auto-sends "y\n" without human intervention.
func TestAutoApproveMatchingPolicy(t *testing.T) {
	jarvisHome, cleanup := testEnv(t)
	defer cleanup()

	// Policy: auto-approve Read tool.
	writeConfig(t, jarvisHome, `
policies:
  auto_approve:
    - tool: [Read]
      action: approve
`)

	sess, pid := spawnSidecar(t, "autoapprove-01", "auto-approve test", "claude")
	socketPath := sidecar.SocketPath(sess.ID)
	defer killSidecar(t, pid, socketPath)

	time.Sleep(500 * time.Millisecond)

	// Attach to send the "read" command.
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()
	codec := protocol.NewCodec(conn)
	conn.SetDeadline(time.Now().Add(15 * time.Second))

	codec.Send(protocol.Request{Action: "attach"})
	var buf protocol.Response
	codec.Receive(&buf) // drain buffer

	// Send "read" which triggers "Allow Read? (y/n)".
	// With the policy, sidecar should auto-approve it.
	codec.Send(protocol.Request{Action: "send_input", Text: "read\n"})

	// Wait for output that indicates the approval went through automatically.
	approved := false
	for i := 0; i < 30; i++ {
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		var out protocol.Response
		if err := codec.Receive(&out); err != nil {
			break
		}
		if out.Event == "output" {
			decoded, _ := base64.StdEncoding.DecodeString(out.Data)
			if strings.Contains(string(decoded), "File read successfully") {
				approved = true
				break
			}
		}
	}
	if !approved {
		t.Error("auto-approve did not fire: never saw 'File read successfully' output")
	}
}

// TestAutoApproveDenyPattern verifies that a deny-list policy keeps the session
// blocked and does NOT auto-approve.
func TestAutoApproveDenyPattern(t *testing.T) {
	jarvisHome, cleanup := testEnv(t)
	defer cleanup()

	// Policy: deny Bash commands matching "rm", approve everything else.
	writeConfig(t, jarvisHome, `
policies:
  auto_approve:
    - tool: [Bash]
      command_matches: "rm"
      action: ask_human
    - tool: [Bash]
      action: approve
`)

	sess, pid := spawnSidecar(t, "deny-01", "deny pattern test", "claude")
	socketPath := sidecar.SocketPath(sess.ID)
	defer killSidecar(t, pid, socketPath)

	time.Sleep(500 * time.Millisecond)

	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()
	codec := protocol.NewCodec(conn)
	conn.SetDeadline(time.Now().Add(15 * time.Second))

	codec.Send(protocol.Request{Action: "attach"})
	var buf protocol.Response
	codec.Receive(&buf)

	// "approve" triggers "Allow Bash?" with "rm -rf build/" in the detail.
	// The deny pattern matches "rm", so it should NOT be auto-approved.
	codec.Send(protocol.Request{Action: "send_input", Text: "approve\n"})

	// Wait for the approval prompt to appear, then check quickly before
	// idle detection overwrites the state.
	time.Sleep(1 * time.Second)

	// Read output to see if the command was auto-executed (it should NOT be).
	autoApproved := false
	for i := 0; i < 10; i++ {
		conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
		var out protocol.Response
		if err := codec.Receive(&out); err != nil {
			break
		}
		if out.Event == "output" {
			decoded, _ := base64.StdEncoding.DecodeString(out.Data)
			if strings.Contains(string(decoded), "Running command") {
				autoApproved = true
				break
			}
		}
	}
	if autoApproved {
		t.Error("deny pattern should have blocked auto-approve, but command was executed")
	}

	// Now manually approve to clean up.
	codec.Send(protocol.Request{Action: "send_input", Text: "y\n"})
}

// TestQuickApproveViaSocket verifies that sending "y\n" via send_input
// approves a blocked session without attaching.
func TestQuickApproveViaSocket(t *testing.T) {
	jarvisHome, cleanup := testEnv(t)
	defer cleanup()

	// No auto-approve policies — everything requires manual approval.
	writeConfig(t, jarvisHome, `
policies:
  auto_approve: []
`)

	sess, pid := spawnSidecar(t, "quickapprove-01", "quick approve test", "claude")
	socketPath := sidecar.SocketPath(sess.ID)
	defer killSidecar(t, pid, socketPath)

	time.Sleep(500 * time.Millisecond)

	// Attach and trigger an approval prompt.
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	codec := protocol.NewCodec(conn)
	conn.SetDeadline(time.Now().Add(15 * time.Second))

	codec.Send(protocol.Request{Action: "attach"})
	var buf protocol.Response
	codec.Receive(&buf)

	codec.Send(protocol.Request{Action: "send_input", Text: "approve\n"})

	// Wait for the approval prompt to be detected.
	time.Sleep(1 * time.Second)

	state, _ := getStatus(t, socketPath)
	if state != "waiting_for_approval" {
		t.Errorf("expected waiting_for_approval before quick-approve, got %q", state)
	}

	// Now quick-approve via a separate connection (simulating dashboard behavior).
	conn2, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("connect for quick-approve: %v", err)
	}
	codec2 := protocol.NewCodec(conn2)
	conn2.SetDeadline(time.Now().Add(2 * time.Second))
	codec2.Send(protocol.Request{Action: "send_input", Text: "y\n"})
	conn2.Close()

	// Verify the approval went through by reading output.
	approved := false
	for i := 0; i < 20; i++ {
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		var out protocol.Response
		if err := codec.Receive(&out); err != nil {
			break
		}
		if out.Event == "output" {
			decoded, _ := base64.StdEncoding.DecodeString(out.Data)
			if strings.Contains(string(decoded), "Command completed") || strings.Contains(string(decoded), "Running command") {
				approved = true
				break
			}
		}
	}
	conn.Close()

	if !approved {
		t.Error("quick-approve via socket did not work: never saw command execution output")
	}
}
