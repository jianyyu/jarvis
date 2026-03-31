package sidecar

import (
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"jarvis/internal/model"
	"jarvis/internal/protocol"
	"jarvis/internal/store"

	"github.com/creack/pty"
)

// DaemonConfig holds the configuration for a sidecar daemon.
type DaemonConfig struct {
	SessionID string
	CWD       string
	ClaudeCmd string
	Env       []string
	Cols      uint16
	Rows      uint16
}

// Daemon manages a Claude Code session with PTY and IPC.
type Daemon struct {
	cfg        DaemonConfig
	socketPath string
	master     *os.File
	cmd        *exec.Cmd
	ringBuf    *RingBuffer
	listener   net.Listener

	state       atomic.Value // model.SidecarState
	detail      atomic.Value // string
	lastOutputT atomic.Value // time.Time

	attachMu     sync.Mutex
	attachedConn net.Conn
	attachedCodec *protocol.Codec
}

func NewDaemon(cfg DaemonConfig) *Daemon {
	d := &Daemon{
		cfg:        cfg,
		socketPath: SocketPath(cfg.SessionID),
		ringBuf:    NewRingBuffer(10000),
	}
	d.state.Store(model.StateIdle)
	d.detail.Store("")
	d.lastOutputT.Store(time.Now())
	return d
}

// SocketPath returns the Unix socket path for a session.
func SocketPath(sessionID string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".jarvis", "sockets", sessionID+".sock")
}

// Run starts the sidecar daemon. Blocks until the Claude process exits.
func (d *Daemon) Run() error {
	// Ensure socket directory exists
	if err := os.MkdirAll(filepath.Dir(d.socketPath), 0o755); err != nil {
		return fmt.Errorf("create socket dir: %w", err)
	}

	// Clean up stale socket
	os.Remove(d.socketPath)

	// Start Claude process with PTY
	master, cmd, err := StartProcessWithPTY(d.cfg.ClaudeCmd, d.cfg.CWD, d.cfg.Env, d.cfg.Cols, d.cfg.Rows)
	if err != nil {
		return fmt.Errorf("start process: %w", err)
	}
	d.master = master
	d.cmd = cmd
	d.state.Store(model.StateWorking)

	log.Printf("sidecar: started process PID %d for session %s", cmd.Process.Pid, d.cfg.SessionID)

	// Detect Claude session ID by watching for new JSONL files
	go d.detectClaudeSessionID()

	// Start socket listener
	listener, err := net.Listen("unix", d.socketPath)
	if err != nil {
		return fmt.Errorf("listen socket: %w", err)
	}
	d.listener = listener
	os.Chmod(d.socketPath, 0o600)

	// Goroutine: read PTY output
	go d.readPTY()

	// Goroutine: accept socket connections
	go d.acceptConnections()

	// Goroutine: periodic state persistence
	go d.persistStateLoop()

	// Goroutine: idle detection — checks if output has stopped
	go d.idleDetectionLoop()

	// Wait for Claude process to exit
	err = cmd.Wait()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	log.Printf("sidecar: process exited with code %d", exitCode)
	d.state.Store(model.StateExited)
	d.detail.Store(fmt.Sprintf("exit code %d", exitCode))

	// Mark session as suspended (not done — user marks done explicitly)
	// This way re-entering the session will resume the Claude conversation.
	if s, err := store.GetSession(d.cfg.SessionID); err == nil {
		s.Status = model.StatusSuspended
		s.LastKnownState = "exited"
		s.LastKnownDetail = fmt.Sprintf("exit code %d", exitCode)
		now := time.Now()
		s.UpdatedAt = now
		s.LastActivityAt = &now
		s.Sidecar = nil
		store.SaveSession(s)
	}

	// Notify attached client
	d.attachMu.Lock()
	if d.attachedCodec != nil {
		d.attachedCodec.Send(protocol.Response{
			Event:    "session_ended",
			ExitCode: exitCode,
		})
	}
	d.attachMu.Unlock()

	// Persist final state
	d.persistState()

	// Cleanup
	d.listener.Close()
	d.master.Close()
	os.Remove(d.socketPath)

	return nil
}

func (d *Daemon) readPTY() {
	buf := make([]byte, 4096)
	for {
		n, err := d.master.Read(buf)
		if err != nil {
			if err != io.EOF {
				log.Printf("sidecar: pty read error: %v", err)
			}
			return
		}
		if n == 0 {
			continue
		}
		data := buf[:n]
		now := time.Now()

		// Compute elapsed since last output BEFORE updating the timestamp
		lastOutput := d.lastOutputT.Load().(time.Time)
		elapsed := now.Sub(lastOutput)
		d.lastOutputT.Store(now)

		// Write to ring buffer
		d.ringBuf.Write(data)

		// Status detection on raw bytes
		state, det := DetectState(data, elapsed)
		d.state.Store(state)
		if det != "" {
			d.detail.Store(det)
		}

		// Forward to attached client
		d.attachMu.Lock()
		if d.attachedCodec != nil {
			encoded := base64.StdEncoding.EncodeToString(data)
			if err := d.attachedCodec.Send(protocol.Response{
				Event: "output",
				Data:  encoded,
			}); err != nil {
				// Client disconnected
				d.attachedConn = nil
				d.attachedCodec = nil
			}
		}
		d.attachMu.Unlock()
	}
}

func (d *Daemon) acceptConnections() {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go d.handleConnection(conn)
	}
}

func (d *Daemon) handleConnection(conn net.Conn) {
	codec := protocol.NewCodec(conn)
	defer func() {
		// Clean up attached reference if this connection was the attached client
		d.attachMu.Lock()
		if d.attachedConn == conn {
			d.attachedConn = nil
			d.attachedCodec = nil
		}
		d.attachMu.Unlock()
		conn.Close()
	}()

	for {
		var req protocol.Request
		if err := codec.Receive(&req); err != nil {
			return
		}

		switch req.Action {
		case "ping":
			codec.Send(protocol.Response{Event: "pong"})

		case "get_status":
			state := d.state.Load().(model.SidecarState)
			det := d.detail.Load().(string)
			codec.Send(protocol.Response{
				Event:  "status",
				State:  string(state),
				Detail: det,
			})

		case "attach":
			d.attachMu.Lock()
			// Detach previous client if any
			if d.attachedConn != nil {
				d.attachedConn.Close()
			}
			d.attachedConn = conn
			d.attachedCodec = codec
			d.attachMu.Unlock()

			// Send buffer catch-up
			bufData := d.ringBuf.Bytes()
			if len(bufData) > 0 {
				encoded := base64.StdEncoding.EncodeToString(bufData)
				codec.Send(protocol.Response{
					Event: "buffer",
					Data:  encoded,
				})
			}

			// Stay in this connection handler — reads will continue in the main loop
			// The attached client just keeps receiving output events from readPTY
			// and can send more requests on this same connection
			continue

		case "detach":
			d.attachMu.Lock()
			if d.attachedConn == conn {
				d.attachedConn = nil
				d.attachedCodec = nil
			}
			d.attachMu.Unlock()
			codec.Send(protocol.Response{Event: "status", State: "detached"})

		case "send_input":
			if d.master != nil {
				d.master.Write([]byte(req.Text))
			}

		case "resize":
			if d.master != nil && req.Cols > 0 && req.Rows > 0 {
				pty.Setsize(d.master, &pty.Winsize{
					Cols: uint16(req.Cols),
					Rows: uint16(req.Rows),
				})
			}

		case "get_buffer":
			n := req.Lines
			if n <= 0 {
				n = 100
			}
			lines := d.ringBuf.LastN(n)
			var allBytes []byte
			for _, l := range lines {
				allBytes = append(allBytes, l...)
			}
			encoded := base64.StdEncoding.EncodeToString(allBytes)
			codec.Send(protocol.Response{Event: "buffer", Data: encoded})
		}
	}
}

// detectClaudeSessionID watches for new JSONL files to capture the Claude session ID.
func (d *Daemon) detectClaudeSessionID() {
	// Compute the Claude project dir for this CWD
	encoded := nonAlphaNum.ReplaceAllString(d.cfg.CWD, "-")
	home, _ := os.UserHomeDir()
	projectDir := filepath.Join(home, ".claude", "projects", encoded)

	// Snapshot existing files
	existingFiles := make(map[string]bool)
	if entries, err := os.ReadDir(projectDir); err == nil {
		for _, e := range entries {
			existingFiles[e.Name()] = true
		}
	}

	// Poll for new JSONL file (check every 1s for up to 30s)
	for i := 0; i < 30; i++ {
		time.Sleep(1 * time.Second)

		state := d.state.Load().(model.SidecarState)
		if state == model.StateExited {
			return
		}

		entries, err := os.ReadDir(projectDir)
		if err != nil {
			continue
		}

		for _, e := range entries {
			name := e.Name()
			if !existingFiles[name] && filepath.Ext(name) == ".jsonl" {
				sessionID := name[:len(name)-len(".jsonl")]
				log.Printf("sidecar: detected Claude session ID: %s", sessionID)

				// Store it in session.yaml
				if s, err := store.GetSession(d.cfg.SessionID); err == nil {
					s.ClaudeSessionID = sessionID
					s.UpdatedAt = time.Now()
					store.SaveSession(s)
				}
				return
			}
		}
	}
	log.Printf("sidecar: could not detect Claude session ID within 30s")
}

var nonAlphaNum = regexp.MustCompile(`[^a-zA-Z0-9]`)

func (d *Daemon) idleDetectionLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		state := d.state.Load().(model.SidecarState)
		if state == model.StateExited {
			return
		}
		lastOutput := d.lastOutputT.Load().(time.Time)
		elapsed := time.Since(lastOutput)
		if elapsed >= idleTimeout && state == model.StateWorking {
			d.state.Store(model.StateIdle)
			d.detail.Store("waiting for user input")
		}
	}
}

func (d *Daemon) persistStateLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		state := d.state.Load().(model.SidecarState)
		if state == model.StateExited {
			return
		}
		d.persistState()
	}
}

func (d *Daemon) persistState() {
	s, err := store.GetSession(d.cfg.SessionID)
	if err != nil {
		log.Printf("sidecar: failed to read session for state persist: %v", err)
		return
	}

	state := d.state.Load().(model.SidecarState)
	det := d.detail.Load().(string)
	now := time.Now()

	s.LastKnownState = string(state)
	s.LastKnownDetail = det
	s.LastActivityAt = &now
	s.UpdatedAt = now

	if s.Sidecar != nil {
		s.Sidecar.State = state
	}

	if err := store.SaveSession(s); err != nil {
		log.Printf("sidecar: failed to persist state: %v", err)
	}
}
