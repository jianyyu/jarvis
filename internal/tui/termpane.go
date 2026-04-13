package tui

import (
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"jarvis/internal/protocol"

	"github.com/charmbracelet/x/vt"
)

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

	stopCh chan struct{}
	closed bool
}

// NewTermPane creates a new TermPane with a VT emulator sized to cols x rows.
func NewTermPane(cols, rows int) *TermPane {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	return &TermPane{
		emulator: vt.NewSafeEmulator(cols, rows),
		cols:     cols,
		rows:     rows,
	}
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
	tp.mu.Lock()
	connected := tp.connected
	rows := tp.rows
	cols := tp.cols
	em := tp.emulator // capture under lock to avoid race with ConnectPreview
	tp.mu.Unlock()

	var content string
	if connected && em != nil {
		content = em.Render()
	} else {
		content = tp.placeholderView(cols, rows)
	}

	// Ensure exactly 'rows' lines for stable layout with JoinHorizontal.
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
	log.Printf("streamOutput: started for session %s", tp.sessionID)
	loopCount := 0
	for {
		loopCount++
		if loopCount%1000 == 0 {
			log.Printf("streamOutput: loop iteration %d", loopCount)
		}

		// Check if we should stop, and capture emulator/codec under lock.
		tp.mu.Lock()
		stopCh := tp.stopCh
		codec := tp.codec
		em := tp.emulator // capture to avoid race with ConnectPreview
		tp.mu.Unlock()

		if stopCh == nil || codec == nil || em == nil {
			log.Printf("streamOutput: exiting (nil stopCh/codec/emulator)")
			return
		}

		select {
		case <-stopCh:
			log.Printf("streamOutput: exiting (stopCh closed)")
			return
		default:
		}

		var resp protocol.Response
		if err := codec.Receive(&resp); err != nil {
			select {
			case <-stopCh:
				log.Printf("streamOutput: exiting (stopCh closed after error)")
			default:
				log.Printf("streamOutput: error: %v", err)
			}
			return
		}
		log.Printf("streamOutput: event=%s datalen=%d", resp.Event, len(resp.Data))

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
				em.Write(raw)
			}

		case "output":
			if resp.Data != "" {
				raw, err := base64.StdEncoding.DecodeString(resp.Data)
				if err != nil {
					continue
				}
				em.Write(raw)
			}

		case "session_ended":
			log.Printf("termpane: session ended (exit code %d)", resp.ExitCode)
			return
		}
	}
}
