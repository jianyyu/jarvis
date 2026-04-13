package tui

import (
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"

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
	tp.emulator.Write(data)
}

// View returns the rendered emulator screen if connected, or a placeholder
// when disconnected.
func (tp *TermPane) View() string {
	tp.mu.Lock()
	connected := tp.connected
	tp.mu.Unlock()

	if connected {
		return tp.emulator.Render()
	}

	// Placeholder for empty state.
	lines := []string{
		"Select a session from the sidebar",
		"or press 'n' to create one",
	}
	var styled []string
	for _, line := range lines {
		styled = append(styled, dimStyle.Render(line))
	}
	return strings.Join(styled, "\n")
}

// ConnectPreview connects to a sidecar socket in read-only preview mode.
// It requests the ring buffer catch-up and starts streaming output, but
// does NOT send an "attach" action. The emulator is reset before connecting.
func (tp *TermPane) ConnectPreview(socketPath, sessionID string) error {
	tp.mu.Lock()
	// If already connected, disconnect first.
	if tp.connected {
		tp.mu.Unlock()
		tp.Disconnect()
		tp.mu.Lock()
	}

	// Reset emulator for fresh session.
	tp.emulator.Close()
	tp.emulator = vt.NewSafeEmulator(tp.cols, tp.rows)
	tp.mu.Unlock()

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("connect to sidecar: %w", err)
	}

	codec := protocol.NewCodec(conn)

	// Request ring buffer catch-up.
	err = codec.Send(protocol.Request{
		Action: "get_buffer",
		Lines:  5000,
	})
	if err != nil {
		conn.Close()
		return fmt.Errorf("request buffer: %w", err)
	}

	tp.mu.Lock()
	tp.conn = conn
	tp.codec = codec
	tp.connected = true
	tp.attached = false
	tp.sessionID = sessionID
	tp.stopCh = make(chan struct{})
	tp.mu.Unlock()

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

	// Reset emulator.
	tp.emulator.Close()
	tp.emulator = vt.NewSafeEmulator(tp.cols, tp.rows)
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
}

// streamOutput is a goroutine that reads responses from the sidecar codec,
// decodes base64-encoded output data, and writes it to the VT emulator.
// It handles "output", "buffer", and "session_ended" events.
func (tp *TermPane) streamOutput() {
	for {
		// Check if we should stop.
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
			// Connection closed or error — stop streaming.
			select {
			case <-stopCh:
				// Expected shutdown.
			default:
				log.Printf("termpane: stream error: %v", err)
			}
			return
		}

		switch resp.Event {
		case "output", "buffer":
			if resp.Data != "" {
				raw, err := base64.StdEncoding.DecodeString(resp.Data)
				if err != nil {
					log.Printf("termpane: base64 decode error: %v", err)
					continue
				}
				tp.emulator.Write(raw)
			}

		case "session_ended":
			log.Printf("termpane: session ended (exit code %d)", resp.ExitCode)
			return
		}
	}
}
