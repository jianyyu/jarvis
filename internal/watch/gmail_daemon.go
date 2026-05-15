package watch

import (
	"context"
	"fmt"
	"log"
	"time"

	"jarvis/internal/config"
	"jarvis/internal/session"
)

// GmailDaemon spawns an interactive Claude session per poll cycle.
// Each session runs /gmail-monitor, which fetches unread emails, classifies
// them, and waits for the user to attach and interact. Emails are only marked
// as read when the user says "done" in the session.
type GmailDaemon struct {
	cfg      *config.Config
	mgr      *session.Manager
	folderID string
	polling  bool
}

// NewGmailDaemon creates a Gmail watcher daemon from config.
func NewGmailDaemon(cfg *config.Config) (*GmailDaemon, error) {
	if !cfg.Watchers.Gmail.Enabled {
		return nil, fmt.Errorf("gmail watcher not enabled in config")
	}
	return &GmailDaemon{
		cfg: cfg,
		mgr: session.NewManager(cfg),
	}, nil
}

// Run starts the Gmail polling loop. Blocks until ctx is cancelled.
func (d *GmailDaemon) Run(ctx context.Context) error {
	gmailCfg := d.cfg.Watchers.Gmail
	interval := time.Duration(gmailCfg.PollInterval) * time.Second
	if interval < 60*time.Second {
		interval = 1 * time.Hour
	}

	if gmailCfg.Folder != "" {
		id, err := ensureFolder(gmailCfg.Folder)
		if err != nil {
			return fmt.Errorf("ensure folder: %w", err)
		}
		d.folderID = id
		log.Printf("gmail-watch: sessions will be placed in folder %q (%s)", gmailCfg.Folder, id)
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

	log.Printf("gmail-watch: spawning new batch session")

	cwd := d.cfg.RepoPath()
	if cwd == "" {
		cwd = "."
	}

	name := fmt.Sprintf("Gmail %s", time.Now().Format("Jan 2 15:04"))
	sess, err := d.mgr.Spawn(name, cwd, []string{"claude"})
	if err != nil {
		log.Printf("gmail-watch: spawn failed: %v", err)
		return
	}

	if d.folderID != "" {
		placeSessionInFolder(sess.ID, d.folderID)
	}

	if !waitForSessionReady(sess.ID, 90*time.Second) {
		log.Printf("gmail-watch: session %s not ready, sending prompt anyway", sess.ID)
	}

	sendInputToSession(sess.ID, "/gmail-monitor all")
	time.Sleep(500 * time.Millisecond)
	sendInputToSession(sess.ID, "\r")

	log.Printf("gmail-watch: created session %q (%s)", sess.Name, sess.ID)
}
