package sidecar

import (
	"os"
	"os/exec"
	"strings"

	"github.com/creack/pty"
)

// StartProcessWithPTY starts a command in a PTY with the given size.
// Returns the PTY master fd and the started command.
func StartProcessWithPTY(cmdLine string, cwd string, env []string, cols, rows uint16) (*os.File, *exec.Cmd, error) {
	parts := splitCommand(cmdLine)
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Dir = cwd
	cmd.Env = env

	winsize := &pty.Winsize{Rows: rows, Cols: cols}
	master, err := pty.StartWithSize(cmd, winsize)
	if err != nil {
		return nil, nil, err
	}
	return master, cmd, nil
}

// splitCommand splits a command string into parts, respecting quoted strings.
func splitCommand(s string) []string {
	var parts []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)

	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote {
			if c == quoteChar {
				inQuote = false
			} else {
				current.WriteByte(c)
			}
		} else if c == '"' || c == '\'' {
			inQuote = true
			quoteChar = c
		} else if c == ' ' || c == '\t' {
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		} else {
			current.WriteByte(c)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}
