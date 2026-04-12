package watch

import (
	"context"
	"fmt"
	"log"
	"time"

	"jarvis/internal/config"
	"jarvis/internal/session"
	"jarvis/internal/store"
)

// Daemon is the persistent Slack watcher process.
type Daemon struct {
	cfg      *config.Config
	mgr      *session.Manager
	registry *Registry
	poller   *SlackPoller
	folderID string // cached folder ID for placing sessions
	polling  bool   // true while a poll is in progress (skip overlapping ticks)
}

// NewDaemon creates a watcher daemon from config.
func NewDaemon(cfg *config.Config) (*Daemon, error) {
	if !cfg.Watchers.Slack.Enabled {
		return nil, fmt.Errorf("slack watcher not enabled in config")
	}
	if cfg.Watchers.Slack.MCPServerCmd == "" {
		return nil, fmt.Errorf("slack watcher mcp_server_cmd not configured")
	}
	if cfg.Watchers.Slack.UserID == "" {
		return nil, fmt.Errorf("slack watcher user_id not configured")
	}

	reg := NewRegistry("slack")
	if err := reg.Load(); err != nil {
		log.Printf("watch: registry load error (starting fresh): %v", err)
	}

	return &Daemon{
		cfg:      cfg,
		mgr:      session.NewManager(cfg),
		registry: reg,
		poller:   NewSlackPoller(cfg.Watchers.Slack, reg.GetLastPollTS()),
	}, nil
}

// Run starts the polling loop. Blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	slackCfg := d.cfg.Watchers.Slack
	interval := time.Duration(slackCfg.PollInterval) * time.Second
	if interval < 10*time.Second {
		interval = 30 * time.Second
	}

	if slackCfg.Folder != "" {
		id, err := ensureFolder(slackCfg.Folder)
		if err != nil {
			return fmt.Errorf("ensure folder: %w", err)
		}
		d.folderID = id
		log.Printf("watch: sessions will be placed in folder %q (%s)", slackCfg.Folder, id)
	}

	log.Printf("watch: polling Slack every %s", interval)

	d.pollOnce(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("watch: shutting down")
			d.poller.Close()
			return nil
		case <-ticker.C:
			d.pollOnce(ctx)
		}
	}
}

func (d *Daemon) pollOnce(ctx context.Context) {
	if d.polling {
		log.Printf("watch: skipping poll — previous poll still running")
		return
	}
	d.polling = true
	defer func() { d.polling = false }()

	events, err := d.poller.Poll(ctx)
	if err != nil {
		log.Printf("watch: poll error: %v", err)
		return
	}

	for _, ev := range events {
		key := ev.ContextKey()

		// If we already have a session for this thread, check it still exists.
		if sessID, found := d.registry.Lookup(key); found {
			if _, err := store.GetSession(sessID); err != nil {
				// Session was deleted — remove stale registry entry so we recreate it.
				log.Printf("watch: session %s deleted, removing stale registry entry for %s", sessID, key)
				d.registry.Unregister(key)
			} else {
				// Session exists — inject the new message as follow-up.
				log.Printf("watch: follow-up in existing session %s — %s", sessID, truncate(ev.Text, 60))
				newContext := fmt.Sprintf("\n[New message from %s at %s]:\n> %s",
					ev.SenderName, ev.Timestamp.Format(time.RFC3339), ev.Text)
				sendInputToSession(sessID, newContext)
				time.Sleep(500 * time.Millisecond)
				sendInputToSession(sessID, "\r")
				continue
			}
		}

		log.Printf("watch: new event: %s — %s", ev.SessionName(), truncate(ev.Text, 60))

		cwd := d.cfg.RepoPath()
		if cwd == "" {
			cwd = "."
		}

		// Spawn a plain claude session.
		sess, err := d.mgr.Spawn(ev.SessionName(), cwd, []string{"claude"})
		if err != nil {
			log.Printf("watch: spawn failed for %s: %v", key, err)
			continue
		}

		if d.folderID != "" {
			placeSessionInFolder(sess.ID, d.folderID)
		}

		d.registry.Register(key, sess.ID)
		if err := d.registry.Save(); err != nil {
			log.Printf("watch: registry save error: %v", err)
		}

		// Inject the prompt via PTY stdin, then submit with \r separately.
		sendInputToSession(sess.ID, ev.InitialPrompt())
		time.Sleep(500 * time.Millisecond)
		sendInputToSession(sess.ID, "\r")

		log.Printf("watch: created session %q (%s)", sess.Name, sess.ID)
	}

	// Persist the poll checkpoint so restarts pick up from here.
	if ts := d.poller.LastTS(); ts != "" {
		d.registry.SetLastPollTS(ts)
		d.registry.Save()
	}
}

