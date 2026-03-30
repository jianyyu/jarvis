package protocol

// Request is sent from jarvis CLI to sidecar.
type Request struct {
	Action string `json:"action"`           // ping, get_status, attach, detach, send_input, resize, get_buffer
	Text   string `json:"text,omitempty"`   // for send_input
	Cols   int    `json:"cols,omitempty"`   // for resize
	Rows   int    `json:"rows,omitempty"`   // for resize
	Lines  int    `json:"lines,omitempty"` // for get_buffer
}

// Response is sent from sidecar to jarvis CLI.
type Response struct {
	Event     string `json:"event"`                // pong, status, output, session_started, session_ended, buffer
	State     string `json:"state,omitempty"`      // for status: working, waiting_for_approval, idle, exited
	Detail    string `json:"detail,omitempty"`     // human-readable detail
	Data      string `json:"data,omitempty"`       // base64-encoded PTY output for output/buffer events
	SessionID string `json:"session_id,omitempty"` // for session_started
	ExitCode  int    `json:"exit_code,omitempty"`  // for session_ended
}
