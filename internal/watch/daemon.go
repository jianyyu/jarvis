package watch

import (
	"context"
	"fmt"
	"log"
	"time"

	"jarvis/internal/config"
	"jarvis/internal/model"
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

	reg := NewRegistry()
	if err := reg.Load(); err != nil {
		log.Printf("watch: registry load error (starting fresh): %v", err)
	}

	return &Daemon{
		cfg:      cfg,
		mgr:      session.NewManager(cfg),
		registry: reg,
		poller:   NewSlackPoller(cfg.Watchers.Slack),
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
		id, err := d.ensureFolder(slackCfg.Folder)
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
	events, err := d.poller.Poll(ctx)
	if err != nil {
		log.Printf("watch: poll error: %v", err)
		return
	}

	for _, ev := range events {
		key := ev.ContextKey()

		if _, found := d.registry.Lookup(key); found {
			continue
		}

		log.Printf("watch: new event: %s — %s", ev.SessionName(), truncate(ev.Text, 60))

		// Fetch full thread context before creating the session.
		d.poller.FetchThreadContext(&ev)

		cwd := d.cfg.RepoPath()
		if cwd == "" {
			cwd = "."
		}
		claudeArgs := []string{
			"claude",
			"--append-system-prompt", ev.SystemPrompt(),
			ev.InitialPrompt(),
		}

		sess, err := d.mgr.Spawn(ev.SessionName(), cwd, claudeArgs)
		if err != nil {
			log.Printf("watch: spawn failed for %s: %v", key, err)
			continue
		}

		if d.folderID != "" {
			d.placeSessionInFolder(sess.ID, d.folderID)
		}

		d.registry.Register(key, sess.ID)
		if err := d.registry.Save(); err != nil {
			log.Printf("watch: registry save error: %v", err)
		}

		log.Printf("watch: created session %q (%s)", sess.Name, sess.ID)
	}
}

// ensureFolder finds a folder by name or creates it.
func (d *Daemon) ensureFolder(name string) (string, error) {
	folders, err := store.ListFolders()
	if err != nil {
		return "", err
	}
	for _, f := range folders {
		if f.Name == name && f.Status == "active" {
			return f.ID, nil
		}
	}

	f := &model.Folder{
		ID:        model.NewID(),
		Type:      "folder",
		Name:      name,
		Status:    "active",
		CreatedAt: time.Now(),
	}
	if err := store.SaveFolder(f); err != nil {
		return "", err
	}
	return f.ID, nil
}

// placeSessionInFolder sets the session's parent and adds it to the folder's children.
func (d *Daemon) placeSessionInFolder(sessionID, folderID string) {
	sess, err := store.GetSession(sessionID)
	if err != nil {
		return
	}
	sess.ParentID = folderID
	store.SaveSession(sess)

	folder, err := store.GetFolder(folderID)
	if err != nil {
		return
	}
	folder.Children = append(folder.Children, model.ChildRef{Type: "session", ID: sessionID})
	store.SaveFolder(folder)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
