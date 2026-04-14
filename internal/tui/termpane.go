package tui

import (
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"jarvis/internal/protocol"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/vt"
)

// termPaneRedrawMsg is a lightweight signal to trigger View().
type termPaneRedrawMsg struct{}

// sidecarEndedMsg signals that the session's sidecar has exited.
type sidecarEndedMsg struct {
	sessionID string
	exitCode  int
}

// TermPane wraps a VT emulator and a sidecar socket connection.
// It supports two modes: preview (read-only output streaming) and
// attached (full interactive input/output).
type TermPane struct {
	mu sync.Mutex

	emulator *vt.SafeEmulator
	cols     int
	rows     int

	conn      net.Conn
	codec     *protocol.Codec
	connected bool
	attached  bool
	sessionID string

	stopCh  chan struct{}
	closed  bool
	program *tea.Program // for sending messages from streamOutput goroutine

	// pendingData accumulates raw bytes from streamOutput. Drained and
	// written to the emulator in View() on the main goroutine, avoiding
	// concurrent Write/Render on SafeEmulator which causes deadlocks.
	pendingData     []byte
}

// NewTermPane creates a new TermPane with a VT emulator sized to cols x rows.
func NewTermPane(cols, rows int) *TermPane {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	em := vt.NewSafeEmulator(cols, rows)
	// Drain emulator responses continuously. Claude Code sends terminal
	// queries (\x1b[c, \x1b[>0q) that the emulator answers via Read().
	// Without draining, Write() blocks waiting for someone to read.
	go drainEmulatorResponses(em)
	return &TermPane{
		emulator: em,
		cols:     cols,
		rows:     rows,
	}
}

// drainEmulatorResponses continuously reads from the emulator to prevent
// Write() from blocking on terminal query responses.
func drainEmulatorResponses(em *vt.SafeEmulator) {
	buf := make([]byte, 4096)
	for {
		_, err := em.Read(buf)
		if err != nil {
			return
		}
	}
}

// SetProgram sets the Bubble Tea program reference for sending messages
// from the streamOutput goroutine back to the main Update loop.
func (tp *TermPane) SetProgram(p *tea.Program) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.program = p
}

// HasPendingData returns true if there's buffered data from streamOutput.
func (tp *TermPane) HasPendingData() bool {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return len(tp.pendingData) > 0
}

// IsAttached returns true if the pane is in full interactive mode.
func (tp *TermPane) IsAttached() bool {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.attached
}

// IsConnected returns true if the pane has an active sidecar connection.
func (tp *TermPane) IsConnected() bool {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.connected
}

// SessionID returns the session ID currently displayed in this pane.
func (tp *TermPane) SessionID() string {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.sessionID
}

// WriteOutput feeds raw bytes directly into the VT emulator.
// This is primarily useful for testing.
func (tp *TermPane) WriteOutput(data []byte) {
	tp.mu.Lock()
	em := tp.emulator
	tp.mu.Unlock()
	em.Write(data)
}

// View returns the rendered emulator screen if connected, or a placeholder
// when disconnected. Always produces exactly tp.rows lines.
func (tp *TermPane) View() string {
	if !tp.mu.TryLock() {
		// Lock is held — skip this frame to avoid blocking Bubble Tea.
		log.Printf("termPane.View: LOCK CONTENTION — skipping frame")
		return padToHeight("  Loading...", tp.rows)
	}
	connected := tp.connected
	rows := tp.rows
	cols := tp.cols
	em := tp.emulator

	// Drain pending data. Cap how much we write per frame so View()
	// stays fast (< 5ms). Excess carries over to the next frame.
	const maxBytesPerFrame = 8192
	pending := tp.pendingData
	if len(pending) > maxBytesPerFrame {
		tp.pendingData = pending[maxBytesPerFrame:]
		pending = pending[:maxBytesPerFrame]
	} else {
		tp.pendingData = nil
	}
	tp.mu.Unlock()

	var content string
	if connected && em != nil {
		if len(pending) > 0 {
			em.Write(pending)
		}
		content = sanitizeForBubbletea(em.Render())
	} else {
		content = tp.placeholderView(cols, rows)
	}

	return padToHeight(content, rows)
}

// placeholderView returns centered placeholder text.
func (tp *TermPane) placeholderView(cols, rows int) string {
	lines := make([]string, rows)
	mid := rows / 2
	msg1 := "Select a session from the sidebar"
	msg2 := "or press 'n' to create one"
	if mid-1 >= 0 && mid-1 < rows {
		lines[mid-1] = dimStyle.Render(msg1)
	}
	if mid < rows {
		lines[mid] = dimStyle.Render(msg2)
	}
	return strings.Join(lines, "\n")
}

// padToHeight ensures s has exactly n lines (pads with empty or truncates).
func padToHeight(s string, n int) string {
	lines := strings.Split(s, "\n")
	for len(lines) < n {
		lines = append(lines, "")
	}
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// ConnectPreview connects to a sidecar socket and prepares for streaming.
// It does NOT request a buffer or send "attach" — those happen in Attach().
// The emulator is reset before connecting.
func (tp *TermPane) ConnectPreview(socketPath, sessionID string) error {
	tp.mu.Lock()
	if tp.connected {
		tp.mu.Unlock()
		tp.Disconnect()
		tp.mu.Lock()
	}

	// Create fresh emulator. Don't Close() the old one — the old
	// streamOutput goroutine may still hold a captured reference to it.
	// It will be GCed after the goroutine exits.
	tp.emulator = vt.NewSafeEmulator(tp.cols, tp.rows)
	go drainEmulatorResponses(tp.emulator)
	tp.mu.Unlock()

	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return fmt.Errorf("connect to sidecar: %w", err)
	}

	codec := protocol.NewCodec(conn)

	// Tell the sidecar our pane dimensions so its PTY output fits.
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	codec.Send(protocol.Request{
		Action: "resize",
		Cols:   tp.cols,
		Rows:   tp.rows,
	})
	conn.SetDeadline(time.Time{})

	tp.mu.Lock()
	tp.conn = conn
	tp.codec = codec
	tp.connected = true
	tp.attached = false
	tp.sessionID = sessionID
	tp.stopCh = make(chan struct{})
	tp.mu.Unlock()

	// Start output goroutine — it will receive the buffer data once
	// Attach() sends the "attach" action.
	go tp.streamOutput()
	return nil
}

// Attach transitions from preview to full interactive (attached) mode
// by sending the "attach" action to the sidecar.
func (tp *TermPane) Attach() error {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	if !tp.connected || tp.codec == nil {
		return fmt.Errorf("not connected to a sidecar")
	}
	if tp.attached {
		return nil // already attached
	}

	err := tp.codec.Send(protocol.Request{Action: "attach"})
	if err != nil {
		return fmt.Errorf("send attach: %w", err)
	}
	tp.attached = true
	return nil
}

// Detach transitions from attached back to preview mode by sending the
// "detach" action to the sidecar.
func (tp *TermPane) Detach() {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	if !tp.connected || !tp.attached || tp.codec == nil {
		return
	}

	_ = tp.codec.Send(protocol.Request{Action: "detach"})
	tp.attached = false
}

// Disconnect closes the sidecar connection entirely and resets state.
// The emulator is recreated so old content is cleared.
func (tp *TermPane) Disconnect() {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.disconnectLocked()
}

// disconnectLocked performs the actual disconnect; caller must hold tp.mu.
func (tp *TermPane) disconnectLocked() {
	if tp.stopCh != nil {
		select {
		case <-tp.stopCh:
			// already closed
		default:
			close(tp.stopCh)
		}
		tp.stopCh = nil
	}

	if tp.conn != nil {
		tp.conn.Close()
		tp.conn = nil
	}
	tp.codec = nil
	tp.connected = false
	tp.attached = false
	tp.sessionID = ""
	tp.pendingData = nil

	// Do NOT close the old emulator here — the old streamOutput goroutine
	// may still be calling em.Write() with a captured reference. Closing it
	// while Write() is running causes hangs. Instead, just orphan it and
	// let GC clean up after the goroutine exits. A fresh emulator is created
	// in ConnectPreview.
}

// SendInput sends keystrokes to the sidecar. Only works in attached mode.
func (tp *TermPane) SendInput(data string) {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	if !tp.attached || tp.codec == nil {
		return
	}

	_ = tp.codec.Send(protocol.Request{
		Action: "send_input",
		Text:   data,
	})
}

// SendResize sends a resize request to the sidecar without resizing the
// local emulator.
func (tp *TermPane) SendResize(cols, rows int) {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	if !tp.connected || tp.codec == nil {
		return
	}

	_ = tp.codec.Send(protocol.Request{
		Action: "resize",
		Cols:   cols,
		Rows:   rows,
	})
}

// Resize resizes both the local VT emulator and sends a resize to the
// sidecar (if connected).
func (tp *TermPane) Resize(cols, rows int) {
	if cols <= 0 {
		cols = 1
	}
	if rows <= 0 {
		rows = 1
	}

	tp.mu.Lock()
	tp.cols = cols
	tp.rows = rows
	tp.emulator.Resize(cols, rows)

	codec := tp.codec
	connected := tp.connected
	tp.mu.Unlock()

	if connected && codec != nil {
		_ = codec.Send(protocol.Request{
			Action: "resize",
			Cols:   cols,
			Rows:   rows,
		})
	}
}

// Close releases all resources held by the TermPane.
func (tp *TermPane) Close() {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	if tp.closed {
		return
	}
	tp.closed = true
	tp.disconnectLocked()
	if tp.emulator != nil {
		tp.emulator.Close()
	}
}

// streamOutput is a goroutine that reads responses from the sidecar codec,
// decodes base64-encoded output data, and writes it to the VT emulator.
// It handles "output", "buffer", and "session_ended" events.
func (tp *TermPane) streamOutput() {
	for {
		tp.mu.Lock()
		stopCh := tp.stopCh
		codec := tp.codec
		tp.mu.Unlock()

		if stopCh == nil || codec == nil {
			return
		}

		select {
		case <-stopCh:
			return
		default:
		}

		var resp protocol.Response
		if err := codec.Receive(&resp); err != nil {
			select {
			case <-stopCh:
			default:
				log.Printf("streamOutput: error: %v", err)
			}
			return
		}

		switch resp.Event {
		case "buffer":
			if resp.Data != "" {
				raw, err := base64.StdEncoding.DecodeString(resp.Data)
				if err != nil {
					continue
				}
				const maxBufferBytes = 16384
				if len(raw) > maxBufferBytes {
					raw = raw[len(raw)-maxBufferBytes:]
				}
				tp.mu.Lock()
				tp.pendingData = append(tp.pendingData, raw...)
				tp.mu.Unlock()
			}

		case "output":
			if resp.Data != "" {
				raw, err := base64.StdEncoding.DecodeString(resp.Data)
				if err != nil {
					continue
				}
				tp.mu.Lock()
				tp.pendingData = append(tp.pendingData, raw...)
				tp.mu.Unlock()
			}

		case "session_ended":
			tp.mu.Lock()
			prog := tp.program
			sid := tp.sessionID
			tp.mu.Unlock()
			if prog != nil {
				prog.Send(sidecarEndedMsg{sessionID: sid})
			}
			return
		}
	}
}

// nonSGRescapeRe matches ANSI escape sequences that are NOT SGR (Select
// Graphic Rendition, ending in 'm'). These include cursor positioning,
// screen mode changes, scroll regions, etc. that confuse Bubble Tea's
// diff renderer.
var nonSGRescapeRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-LN-Za-ln-z]|\x1b\[\?[0-9;]*[a-z]|\x1b[()][A-Z0-9]|\x1b=|\x1b>`)

// sanitizeForBubbletea strips non-SGR escape sequences from VT emulator
// output so Bubble Tea's renderer can diff it correctly.
func sanitizeForBubbletea(s string) string {
	return nonSGRescapeRe.ReplaceAllString(s, "")
}

