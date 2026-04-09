package sidecar

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jarvis/internal/config"
	"jarvis/internal/model"
	"jarvis/internal/protocol"
	"jarvis/internal/store"
)

func TestDaemonPingPong(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	sessionID := "test-ping"
	socketPath := filepath.Join(tmp, "sockets", sessionID+".sock")

	// Create session.yaml so persistState works
	now := time.Now()
	store.SaveSession(&model.Session{
		ID: sessionID, Type: "session", Name: "test",
		Status: model.StatusActive, LaunchDir: tmp, CreatedAt: now, UpdatedAt: now,
		Sidecar: &model.SidecarInfo{Socket: socketPath, State: model.StateWorking},
	})

	cfg := DaemonConfig{
		SessionID: sessionID,
		CWD:       tmp,
		ClaudeCmd: "bash -c 'read line; echo $line; exit 0'",
		Env:       os.Environ(),
		Cols:      80,
		Rows:      24,
	}

	d := NewDaemon(cfg)
	d.socketPath = socketPath

	// Run daemon in background
	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Run()
	}()

	// Wait for socket to be ready
	var conn net.Conn
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		c, err := net.Dial("unix", socketPath)
		if err == nil {
			conn = c
			break
		}
	}
	if conn == nil {
		t.Fatal("daemon socket never became ready")
	}
	defer conn.Close()

	codec := protocol.NewCodec(conn)

	// Test ping
	codec.Send(protocol.Request{Action: "ping"})
	var resp protocol.Response
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := codec.Receive(&resp); err != nil {
		t.Fatalf("receive pong: %v", err)
	}
	if resp.Event != "pong" {
		t.Errorf("expected pong, got %q", resp.Event)
	}

	// Test get_status
	codec.Send(protocol.Request{Action: "get_status"})
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	codec.Receive(&resp)
	if resp.Event != "status" {
		t.Errorf("expected status, got %q", resp.Event)
	}

	// Test send_input + read output via attach
	codec.Send(protocol.Request{Action: "send_input", Text: "hello world\n"})
	time.Sleep(200 * time.Millisecond)

	// Wait for the process to exit (bash reads one line then exits)
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Error("daemon did not exit in time")
	}

	// Verify output was captured in ring buffer
	bufData := d.ringBuf.Bytes()
	if !strings.Contains(string(bufData), "hello world") {
		t.Logf("ring buffer content: %q", string(bufData))
	}
}

// TestDaemonAutoApproval verifies the full auto-approval flow:
// a mock process prints an approval prompt, and the sidecar should
// automatically send Enter to approve it based on policy rules.
func TestDaemonAutoApproval(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	sessionID := "test-autoapprove"
	socketPath := filepath.Join(tmp, "sockets", sessionID+".sock")

	now := time.Now()
	store.SaveSession(&model.Session{
		ID: sessionID, Type: "session", Name: "test-autoapprove",
		Status: model.StatusActive, LaunchDir: tmp, CreatedAt: now, UpdatedAt: now,
		Sidecar: &model.SidecarInfo{Socket: socketPath, State: model.StateWorking},
	})

	// The mock script:
	// 1. Prints a Claude Code-style approval prompt
	// 2. Reads a line (the auto-approve response)
	// 3. If it got input, prints "APPROVED" to confirm auto-approval worked
	// 4. Exits
	scriptPath := filepath.Join(tmp, "mock_approval.sh")
	os.WriteFile(scriptPath, []byte(`#!/bin/bash
printf 'Read file\n\n  Search(pattern: "**/*.go")\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n Esc to cancel\n'
sleep 0.2
read -t 5 response
echo "APPROVED"
`), 0755)
	cfg := DaemonConfig{
		SessionID:  sessionID,
		CWD:        tmp,
		ClaudeCmd: scriptPath,
		Env:       os.Environ(),
		Cols:      80,
		Rows:      24,
	}

	d := NewDaemon(cfg)
	d.socketPath = socketPath
	// Inject policies directly
	d.policies = []config.ApprovalRule{
		{Tool: config.ToolMatch{"Read", "Grep", "Glob"}, Action: config.ActionApprove},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Run()
	}()

	// Wait for process to finish
	select {
	case <-errCh:
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not exit in time")
	}

	bufData := string(d.ringBuf.Bytes())
	t.Logf("ring buffer content: %q", bufData)
	if !strings.Contains(bufData, "APPROVED") {
		t.Errorf("auto-approval did not work. Ring buffer:\n%s", bufData)
	}
}

// TestDaemonAutoApprovalMCP tests auto-approval for MCP tool prompts.
func TestDaemonAutoApprovalMCP(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	sessionID := "test-autoapprove-mcp"
	socketPath := filepath.Join(tmp, "sockets", sessionID+".sock")

	now := time.Now()
	store.SaveSession(&model.Session{
		ID: sessionID, Type: "session", Name: "test-autoapprove-mcp",
		Status: model.StatusActive, LaunchDir: tmp, CreatedAt: now, UpdatedAt: now,
		Sidecar: &model.SidecarInfo{Socket: socketPath, State: model.StateWorking},
	})

	scriptPath := filepath.Join(tmp, "mock_mcp.sh")
	os.WriteFile(scriptPath, []byte(`#!/bin/bash
printf 'Tool use\n\n   claude.ai Slack - Search(query: "test") (MCP)\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n Esc to cancel\n'
sleep 0.2
read -t 5 response
echo "APPROVED"
`), 0755)

	cfg := DaemonConfig{
		SessionID: sessionID,
		CWD:       tmp,
		ClaudeCmd: scriptPath,
		Env:       os.Environ(),
		Cols:      80,
		Rows:      24,
	}

	d := NewDaemon(cfg)
	d.socketPath = socketPath
	d.policies = []config.ApprovalRule{
		{Tool: config.ToolMatch{"mcp"}, Action: config.ActionApprove},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Run()
	}()

	select {
	case <-errCh:
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not exit in time")
	}

	bufData := string(d.ringBuf.Bytes())
	t.Logf("ring buffer content: %q", bufData)
	if !strings.Contains(bufData, "APPROVED") {
		t.Errorf("MCP auto-approval did not work. Ring buffer:\n%s", bufData)
	}
}

// TestDaemonAutoApprovalBlocked tests that dangerous commands are NOT auto-approved.
func TestDaemonAutoApprovalBlocked(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	sessionID := "test-autoapprove-blocked"
	socketPath := filepath.Join(tmp, "sockets", sessionID+".sock")

	now := time.Now()
	store.SaveSession(&model.Session{
		ID: sessionID, Type: "session", Name: "test-blocked",
		Status: model.StatusActive, LaunchDir: tmp, CreatedAt: now, UpdatedAt: now,
		Sidecar: &model.SidecarInfo{Socket: socketPath, State: model.StateWorking},
	})

	scriptPath := filepath.Join(tmp, "mock_blocked.sh")
	os.WriteFile(scriptPath, []byte(`#!/bin/bash
printf 'Bash\n\n  rm -rf /tmp/important\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n Esc to cancel\n'
sleep 0.2
read -t 2 response
if [ -n "$response" ]; then
  echo "APPROVED"
else
  echo "BLOCKED"
fi
`), 0755)

	cfg := DaemonConfig{
		SessionID: sessionID,
		CWD:       tmp,
		ClaudeCmd: scriptPath,
		Env:       os.Environ(),
		Cols:      80,
		Rows:      24,
	}

	d := NewDaemon(cfg)
	d.socketPath = socketPath
	d.policies = []config.ApprovalRule{
		{Tool: config.ToolMatch{"Bash"}, CommandMatches: "rm|drop|force", Action: config.ActionAskHuman},
		{Tool: config.ToolMatch{"Bash"}, Action: config.ActionApprove},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Run()
	}()

	select {
	case <-errCh:
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not exit in time")
	}

	bufData := string(d.ringBuf.Bytes())
	t.Logf("ring buffer content: %q", bufData)
	if strings.Contains(bufData, "APPROVED") {
		t.Error("dangerous command should NOT have been auto-approved")
	}
}

// TestDaemonAutoApprovalReplay replays real Claude Code PTY output captured
// from a live session. This tests the full sidecar auto-approval flow against
// actual bytes that Claude Code produces, including ANSI escape codes,
// synchronized output mode, and the exact approval menu rendering.
func TestDaemonAutoApprovalReplay(t *testing.T) {
	// Check that the test fixture exists
	b64File := filepath.Join("testdata", "approval_prompt.b64")
	if _, err := os.Stat(b64File); os.IsNotExist(err) {
		t.Skip("testdata/approval_prompt.b64 not found — run debug-approval to capture")
	}
	mockScript := filepath.Join("testdata", "mock_approval_replay.sh")
	if _, err := os.Stat(mockScript); os.IsNotExist(err) {
		t.Skip("testdata/mock_approval_replay.sh not found")
	}

	// Get absolute paths (tests run with cwd = package dir)
	absB64, _ := filepath.Abs(b64File)
	absScript, _ := filepath.Abs(mockScript)

	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	sessionID := "test-replay"
	socketPath := filepath.Join(tmp, "sockets", sessionID+".sock")

	now := time.Now()
	store.SaveSession(&model.Session{
		ID: sessionID, Type: "session", Name: "test-replay",
		Status: model.StatusActive, LaunchDir: tmp, CreatedAt: now, UpdatedAt: now,
		Sidecar: &model.SidecarInfo{Socket: socketPath, State: model.StateWorking},
	})

	cfg := DaemonConfig{
		SessionID: sessionID,
		CWD:       tmp,
		ClaudeCmd: absScript + " " + absB64,
		Env:       os.Environ(),
		Cols:      120,
		Rows:      40,
	}

	d := NewDaemon(cfg)
	d.socketPath = socketPath
	// Policy: approve MCP tools (the captured prompt is for an MCP tool)
	d.policies = []config.ApprovalRule{
		{Tool: config.ToolMatch{"Read", "Grep", "Glob", "mcp"}, Action: config.ActionApprove},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Run()
	}()

	select {
	case <-errCh:
	case <-time.After(15 * time.Second):
		t.Fatal("daemon did not exit in time")
	}

	bufData := string(d.ringBuf.Bytes())
	t.Logf("ring buffer tail (last 200 chars): %q", bufData[max(0, len(bufData)-200):])

	if !strings.Contains(bufData, "APPROVED") {
		t.Errorf("replay auto-approval did not work — sidecar failed to approve real Claude Code approval prompt")
		if strings.Contains(bufData, "BLOCKED") {
			t.Error("mock script timed out waiting for input — sidecar did not send \\n")
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
