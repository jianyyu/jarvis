package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"jarvis/internal/protocol"
	"jarvis/internal/sidecar"

	"github.com/spf13/cobra"
)

// sessionStartPayload mirrors the relevant fields of Claude Code's SessionStart
// hook stdin payload. Other fields (transcript_path, cwd, hook_event_name,
// source, ...) are ignored.
type sessionStartPayload struct {
	SessionID string `json:"session_id"`
	Source    string `json:"source"`
}

var hookRelayCmd = &cobra.Command{
	Use:   "hook-relay <event>",
	Short: "Internal: relay a Claude Code hook payload to the jarvis sidecar",
	Long: `Invoked by Claude Code via the hooks settings written by jarvis. Reads the
hook JSON payload from stdin, identifies the jarvis session via the
JARVIS_SESSION_ID environment variable, and forwards the relevant fields to
the sidecar over its Unix socket. Always exits 0 — a hook failure must not
block Claude.`,
	Args:                  cobra.ExactArgs(1),
	DisableFlagsInUseLine: true,
	SilenceErrors:         true,
	SilenceUsage:          true,
	RunE: func(cmd *cobra.Command, args []string) error {
		event := args[0]

		payload, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "hook-relay: read stdin: %v\n", err)
			return nil
		}

		jarvisSessionID := os.Getenv("JARVIS_SESSION_ID")
		if jarvisSessionID == "" {
			fmt.Fprintln(os.Stderr, "hook-relay: JARVIS_SESSION_ID not set; ignoring hook")
			return nil
		}

		switch event {
		case "SessionStart":
			var p sessionStartPayload
			if err := json.Unmarshal(payload, &p); err != nil {
				fmt.Fprintf(os.Stderr, "hook-relay: parse SessionStart payload: %v\n", err)
				return nil
			}
			if p.SessionID == "" {
				fmt.Fprintln(os.Stderr, "hook-relay: SessionStart payload missing session_id")
				return nil
			}
			req := protocol.Request{
				Action:    "set_session_id",
				SessionID: p.SessionID,
			}
			if err := sendToSidecar(jarvisSessionID, req); err != nil {
				fmt.Fprintf(os.Stderr, "hook-relay: send set_session_id: %v\n", err)
			}
		default:
			fmt.Fprintf(os.Stderr, "hook-relay: unknown event %q (ignored)\n", event)
		}

		return nil
	},
}

// sendToSidecar dials the per-session socket, sends one Request, waits briefly
// for a Response so the sidecar has a chance to apply the change before we
// exit, and closes. Errors are returned but not fatal to the caller.
func sendToSidecar(jarvisSessionID string, req protocol.Request) error {
	socketPath := sidecar.SocketPath(jarvisSessionID)

	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return fmt.Errorf("dial %s: %w", socketPath, err)
	}
	defer conn.Close()

	codec := protocol.NewCodec(conn)
	if err := codec.Send(req); err != nil {
		return fmt.Errorf("send: %w", err)
	}

	// Best-effort read of the ack so we don't race the sidecar.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var resp protocol.Response
	_ = codec.Receive(&resp)
	return nil
}
