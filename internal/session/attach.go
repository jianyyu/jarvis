package session

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"jarvis/internal/protocol"

	"golang.org/x/term"
)

const detachByte = 0x1c // Ctrl-\

// Attach connects to a sidecar and enters raw PTY passthrough mode.
func Attach(socketPath string) error {
	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("connect to sidecar: %w", err)
	}

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
		conn.Close()
		return fmt.Errorf("make raw: %w", err)
	}

	// Ignore SIGQUIT — Ctrl-\ sends SIGQUIT which would kill us
	signal.Ignore(syscall.SIGQUIT)

	// Handle SIGWINCH
	sigWinch := make(chan os.Signal, 1)
	signal.Notify(sigWinch, syscall.SIGWINCH)

	ctx, cancel := context.WithCancel(context.Background())

	// Handle resize signals
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigWinch:
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
	// Use a pipe so we can close it to unblock the reader goroutine.
	// Without this, the goroutine blocks on os.Stdin.Read() forever and
	// competes with bubbletea after we return.
	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		term.Restore(fd, oldState)
		conn.Close()
		return fmt.Errorf("create pipe: %w", err)
	}

	// Copy stdin → pipe (will be killed by closing pipeW)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			if _, err := pipeW.Write(buf[:n]); err != nil {
				return // pipe closed
			}
		}
	}()

	// Read pipe → send to sidecar
	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		buf := make([]byte, 4096)
		for {
			n, err := pipeR.Read(buf)
			if err != nil {
				if err != io.EOF {
					cancel()
				}
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

	// Clean up in order:
	// 1. Restore terminal (so bubbletea gets a clean state)
	term.Restore(fd, oldState)

	// 2. Close pipe to unblock the stdin reader goroutine
	pipeW.Close()
	pipeR.Close()

	// 3. Wait for stdin goroutine to exit
	select {
	case <-stdinDone:
	case <-time.After(500 * time.Millisecond):
	}

	// 4. Stop signals, close connection
	signal.Stop(sigWinch)
	signal.Reset(syscall.SIGQUIT)
	conn.Close()

	return nil
}
