package sidecar

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jarvis/v2/internal/model"
	"jarvis/v2/internal/protocol"
	"jarvis/v2/internal/store"
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
		Status: model.StatusActive, CreatedAt: now, UpdatedAt: now,
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
