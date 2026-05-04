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
	"sync"
	"sync/atomic"
	"time"

	"jarvis/internal/config"
	"jarvis/internal/model"
	"jarvis/internal/protocol"
	"jarvis/internal/store"

	"github.com/creack/pty"
)

// attachedWriteTimeout bounds how long the daemon will block writing to an
// attached client before declaring it dead. Without this bound, a stuck client
// (terminal closed without detach, paused tmux pane, ssh half-disconnect) would
// fill the socket buffer and block readPTY/shutdown indefinitely while holding
// attachMu, freezing all subsequent attach attempts.
const attachedWriteTimeout = 2 * time.Second

// DaemonConfig holds the configuration for a sidecar daemon.
type DaemonConfig struct {
	SessionID       string
	CWD             string
	ClaudeCmd       string
	ClaudeSessionID string // Last known Claude session UUID, if any. Authoritative updates arrive via the SessionStart hook (set_session_id action).
	Env             []string
	Cols            uint16
	Rows            uint16
}

// Daemon manages a Claude Code session with PTY and IPC.
type Daemon struct {
	cfg        DaemonConfig
	policies   []config.ApprovalRule
	socketPath string
	master     *os.File
	cmd        *exec.Cmd
	ringBuf    *RingBuffer
	listener   net.Listener

	state           atomic.Value // model.SidecarState
	detail          atomic.Value // string
	lastOutputT     atomic.Value // time.Time
	lastApproveTime time.Time          // debounce: last time we sent an auto-approve
	prevState       model.SidecarState // track state transitions for debounce reset
	handledApproval bool               // true after approval answered; suppresses stale ring buffer matches

	attachMu      sync.Mutex
	attachedConn  net.Conn
	attachedCodec *protocol.Codec
}

func NewDaemon(cfg DaemonConfig) *Daemon {
	// Load auto-approve policies from config.
	var policies []config.ApprovalRule
	if jarvisCfg, err := config.Load(); err == nil {
		policies = jarvisCfg.Policies.AutoApprove
	}
	if len(policies) > 0 {
		log.Printf("sidecar: loaded %d auto-approve policy rules", len(policies))
	}

	d := &Daemon{
		cfg:        cfg,
		policies:   policies,
		socketPath: SocketPath(cfg.SessionID),
		ringBuf:    NewRingBuffer(10000),
	}
	d.state.Store(model.StateIdle)
	d.detail.Store("")
	d.lastOutputT.Store(time.Now())
	return d
}

// SocketPath returns the Unix socket path for a session.
// It respects the JARVIS_HOME environment variable via store.JarvisHome().
func SocketPath(sessionID string) string {
	return filepath.Join(store.JarvisHome(), "sockets", sessionID+".sock")
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

	// Claude session ID is delivered by the SessionStart hook, which calls
	// `jarvis hook-relay SessionStart` and routes a set_session_id request
	// to acceptConnections below. If we already have an ID (e.g. from a
	// fresh --resume), the hook will simply reconfirm it.
	if d.cfg.ClaudeSessionID != "" {
		log.Printf("sidecar: Claude session ID already known: %s", d.cfg.ClaudeSessionID)
	}
	go d.watchSessionIDHook()

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

	// Notify attached client (bounded so a stuck client can't wedge shutdown).
	d.sendToAttached(protocol.Response{
		Event:    "session_ended",
		ExitCode: exitCode,
	})

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

		// Status detection on raw bytes.
		// Pass recent ring buffer content so approval prompts split across
		// multiple PTY reads can still be detected.
		var recentCtx []byte
		if recent := d.ringBuf.LastN(30); len(recent) > 0 {
			for _, l := range recent {
				recentCtx = append(recentCtx, l...)
				recentCtx = append(recentCtx, '\n')
			}
		}
		// First check the current chunk alone (without ring buffer context).
		// If the current chunk matches an approval pattern, it's a fresh prompt.
		// If only the ring buffer matches, it may be stale.
		chunkState, _ := DetectState(data, elapsed, nil)
		state, det := DetectState(data, elapsed, recentCtx)

		// Suppress stale approval detections: if the approval was only found
		// in the ring buffer context (not the current chunk), and we already
		// handled an approval recently, it's the old prompt lingering.
		if state == model.StateWaitingForApproval && chunkState != model.StateWaitingForApproval && d.handledApproval {
			state = model.StateWorking
			det = ""
		}

		d.state.Store(state)
		if det != "" {
			d.detail.Store(det)
		}

		// When state transitions away from approval (prompt was answered),
		// reset debounce and mark as handled so ring buffer matches are suppressed.
		if d.prevState == model.StateWaitingForApproval && state != model.StateWaitingForApproval {
			d.lastApproveTime = time.Time{}
			d.handledApproval = true
		}
		// A fresh approval in the current chunk clears the handled flag.
		if chunkState == model.StateWaitingForApproval {
			d.handledApproval = false
		}
		d.prevState = state

		// Auto-approve: if the prompt matches a policy, send \r to confirm
		// the pre-selected "Yes" option in Claude Code's numbered selection menu.
		// Debounce: only send once per approval prompt to avoid spamming keystrokes
		// when the same prompt is detected across multiple PTY chunks.
		if state == model.StateWaitingForApproval && len(d.policies) > 0 && (d.lastApproveTime.IsZero() || time.Since(d.lastApproveTime) > 3*time.Second) {
			decision := EvaluateApproval(d.policies, det)
			if decision.Action == config.ActionApprove {
				d.lastApproveTime = now
				toolName := ExtractToolName(det)
				log.Printf("sidecar: auto-approved %q (matched rule for %v)", toolName, decision.Rule.Tool)
				// Delay to let the prompt fully render and the input handler initialize.
				go func() {
					time.Sleep(500 * time.Millisecond)
					// Send \r (carriage return) — confirmed working via debug-approval
					// tool with PTY I/O logging. The key requirement is timing: the
					// menu must be fully rendered before sending (detected via
					// approvalReadyPatterns in status.go).
					d.master.Write([]byte("\r"))
				}()
			} else {
				log.Printf("sidecar: approval withheld for %q (action=%s)", ExtractToolName(det), decision.Action)
			}
		}

		// Forward to attached client. sendToAttached releases attachMu before
		// the network write so a slow/dead client cannot block readPTY (and
		// thereby PTY draining + every other attach attempt).
		d.sendToAttached(protocol.Response{
			Event: "output",
			Data:  base64.StdEncoding.EncodeToString(data),
		})
	}
}

// sendToAttached delivers resp to the currently attached client (if any) with
// a bounded write deadline. The attachMu lock is NOT held across the network
// write — a stuck client must not be able to wedge readPTY or the daemon
// shutdown path. On write error or timeout, the client is detached and its
// connection is closed so handleConnection unblocks and exits cleanly.
func (d *Daemon) sendToAttached(resp protocol.Response) {
	d.attachMu.Lock()
	conn := d.attachedConn
	codec := d.attachedCodec
	d.attachMu.Unlock()
	if codec == nil {
		return
	}

	conn.SetWriteDeadline(time.Now().Add(attachedWriteTimeout))
	err := codec.Send(resp)
	conn.SetWriteDeadline(time.Time{})
	if err == nil {
		return
	}

	log.Printf("sidecar: dropping attached client (write failed: %v)", err)
	d.attachMu.Lock()
	if d.attachedConn == conn {
		d.attachedConn = nil
		d.attachedCodec = nil
	}
	d.attachMu.Unlock()
	conn.Close()
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
				// If we're in approval state and the user sends input,
				// mark the approval as handled to suppress stale re-detection.
				if d.state.Load() == model.StateWaitingForApproval {
					d.handledApproval = true
				}
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

		case "set_session_id":
			if req.SessionID == "" {
				codec.Send(protocol.Response{Event: "error", Detail: "missing session_id"})
				continue
			}
			if s, err := store.GetSession(d.cfg.SessionID); err == nil {
				if s.ClaudeSessionID != req.SessionID {
					log.Printf("sidecar: set Claude session ID via hook: %s (was %q)", req.SessionID, s.ClaudeSessionID)
					s.ClaudeSessionID = req.SessionID
					s.UpdatedAt = time.Now()
					if err := store.SaveSession(s); err != nil {
						log.Printf("sidecar: failed to persist Claude session ID: %v", err)
					}
				}
				d.cfg.ClaudeSessionID = req.SessionID
			} else {
				log.Printf("sidecar: set_session_id: failed to load session %s: %v", d.cfg.SessionID, err)
			}
			codec.Send(protocol.Response{Event: "ok"})
		}
	}
}

// watchSessionIDHook logs a single warning if Claude's SessionStart hook
// has not pushed a session id within 60s — preserves a regression signal
// without re-introducing the polling logic.
func (d *Daemon) watchSessionIDHook() {
	const timeout = 60 * time.Second
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-deadline.C:
			s, err := store.GetSession(d.cfg.SessionID)
			if err == nil && s.ClaudeSessionID == "" {
				log.Printf("sidecar: warning — no SessionStart hook received within %s for session %s; check Claude --settings wiring", timeout, d.cfg.SessionID)
			}
			return
		case <-ticker.C:
			if d.state.Load().(model.SidecarState) == model.StateExited {
				return
			}
			if s, err := store.GetSession(d.cfg.SessionID); err == nil && s.ClaudeSessionID != "" {
				return
			}
		}
	}
}

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
