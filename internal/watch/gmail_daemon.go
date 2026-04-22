package watch

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"time"

	"jarvis/internal/config"
)

// GmailDaemon polls Gmail by running Claude in non-interactive mode.
// Each poll cycle: `claude -p "/gmail-monitor all"` → exits when done.
// No session, no PTY, no context accumulation.
type GmailDaemon struct {
	cfg     *config.Config
	polling bool
}

// NewGmailDaemon creates a Gmail watcher daemon from config.
func NewGmailDaemon(cfg *config.Config) (*GmailDaemon, error) {
	if !cfg.Watchers.Gmail.Enabled {
		return nil, fmt.Errorf("gmail watcher not enabled in config")
	}
	return &GmailDaemon{cfg: cfg}, nil
}

// Run starts the Gmail polling loop. Blocks until ctx is cancelled.
func (d *GmailDaemon) Run(ctx context.Context) error {
	gmailCfg := d.cfg.Watchers.Gmail
	interval := time.Duration(gmailCfg.PollInterval) * time.Second
	if interval < 60*time.Second {
		interval = 10 * time.Minute
	}

	log.Printf("gmail-watch: polling every %s", interval)

	d.pollOnce(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("gmail-watch: shutting down")
			return nil
		case <-ticker.C:
			d.pollOnce(ctx)
		}
	}
}

func (d *GmailDaemon) pollOnce(ctx context.Context) {
	if d.polling {
		log.Printf("gmail-watch: skipping poll — previous still running")
		return
	}
	d.polling = true
	defer func() { d.polling = false }()

	log.Printf("gmail-watch: running /gmail-monitor all")

	cwd := d.cfg.RepoPath()
	if cwd == "" {
		cwd = "."
	}

	cmd := exec.CommandContext(ctx, "claude", "-p", "/gmail-monitor all")
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()

	if err != nil {
		log.Printf("gmail-watch: error: %v", err)
		if len(output) > 0 {
			log.Printf("gmail-watch: output: %s", truncate(string(output), 200))
		}
		return
	}

	log.Printf("gmail-watch: done (%d bytes output)", len(output))
}
