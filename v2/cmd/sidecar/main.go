package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

	"jarvis/v2/internal/sidecar"
	"jarvis/v2/internal/store"
)

func main() {
	sessionID := flag.String("session-id", "", "session ID")
	cwd := flag.String("cwd", ".", "working directory")
	claudeCmd := flag.String("claude-cmd", "claude", "command to run")
	cols := flag.Int("cols", 80, "terminal columns")
	rows := flag.Int("rows", 24, "terminal rows")
	flag.Parse()

	if *sessionID == "" {
		log.Fatal("--session-id is required")
	}

	// Set up logging to a file
	logDir := filepath.Join(store.JarvisHome(), "sessions", *sessionID)
	os.MkdirAll(logDir, 0o755)
	logFile, err := os.OpenFile(filepath.Join(logDir, "sidecar.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Fatalf("failed to open log file: %v", err)
	}
	log.SetOutput(logFile)

	cfg := sidecar.DaemonConfig{
		SessionID: *sessionID,
		CWD:       *cwd,
		ClaudeCmd: *claudeCmd,
		Env:       os.Environ(),
		Cols:      uint16(*cols),
		Rows:      uint16(*rows),
	}

	d := sidecar.NewDaemon(cfg)
	if err := d.Run(); err != nil {
		log.Fatalf("sidecar daemon failed: %v", err)
	}
}
