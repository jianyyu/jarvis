package session

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"jarvis/v2/internal/protocol"

	"golang.org/x/term"
)

const detachByte = 0x1c // Ctrl-\

// Attach connects to a sidecar and enters raw PTY passthrough mode.
func Attach(socketPath string) error {
	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("connect to sidecar: %w", err)
	}
	defer conn.Close()

	codec := protocol.NewCodec(conn)

	// Get terminal size and send resize
	fd := int(os.Stdin.Fd())
	cols, rows, err := term.GetSize(fd)
	if err != nil {
		cols, rows = 80, 24
	}
	codec.Send(protocol.Request{Action: "resize", Cols: cols, Rows: rows})

	// Send attach
	codec.Send(protocol.Request{Action: "attach"})

	// Put terminal in raw mode
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("make raw: %w", err)
	}
	defer term.Restore(fd, oldState)

	// Handle SIGWINCH
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle resize signals
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigCh:
				c, r, err := term.GetSize(fd)
				if err == nil {
					codec.Send(protocol.Request{Action: "resize", Cols: c, Rows: r})
				}
			}
		}
	}()

	// Sidecar → terminal
	go func() {
		for {
			var resp protocol.Response
			if err := codec.Receive(&resp); err != nil {
				cancel()
				return
			}
			switch resp.Event {
			case "output", "buffer":
				data, err := base64.StdEncoding.DecodeString(resp.Data)
				if err == nil {
					os.Stdout.Write(data)
				}
			case "session_ended":
				fmt.Fprintf(os.Stderr, "\r\n[session ended with exit code %d]\r\n", resp.ExitCode)
				cancel()
				return
			}
		}
	}()

	// Terminal → sidecar
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				cancel()
				return
			}
			data := buf[:n]

			// Check for detach key (Ctrl-\)
			for _, b := range data {
				if b == detachByte {
					codec.Send(protocol.Request{Action: "detach"})
					cancel()
					return
				}
			}

			codec.Send(protocol.Request{Action: "send_input", Text: string(data)})
		}
	}()

	<-ctx.Done()
	return nil
}
