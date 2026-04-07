package watch

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"jarvis/internal/config"
	"jarvis/internal/model"
	"jarvis/internal/protocol"
	"jarvis/internal/session"
	"jarvis/internal/sidecar"
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

	reg := NewRegistry()
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

		// If we already have a session for this thread, inject the new message.
		if sessID, found := d.registry.Lookup(key); found {
			log.Printf("watch: follow-up in existing session %s — %s", sessID, truncate(ev.Text, 60))
			newContext := fmt.Sprintf("\n[New message from %s at %s]:\n> %s",
				ev.SenderName, ev.Timestamp.Format(time.RFC3339), ev.Text)
			d.sendInput(sessID, newContext)
			time.Sleep(500 * time.Millisecond)
			d.sendInput(sessID, "\r")
			continue
		}

		log.Printf("watch: new event: %s — %s", ev.SessionName(), truncate(ev.Text, 60))

		// Fetch full thread context before creating the session.
		d.poller.FetchThreadContext(&ev)

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
			d.placeSessionInFolder(sess.ID, d.folderID)
		}

		d.registry.Register(key, sess.ID)
		if err := d.registry.Save(); err != nil {
			log.Printf("watch: registry save error: %v", err)
		}

		// Inject the prompt via PTY stdin, then submit with \r separately.
		// Sending text + \r in one write doesn't work — Claude Code treats
		// the \r as part of the pasted text. Split into two writes with a delay.
		prompt := ev.SystemPrompt() + "\n" + ev.InitialPrompt()
		d.sendInput(sess.ID, prompt)
		time.Sleep(500 * time.Millisecond)
		d.sendInput(sess.ID, "\r")

		log.Printf("watch: created session %q (%s)", sess.Name, sess.ID)
	}

	// Persist the poll checkpoint so restarts pick up from here.
	if ts := d.poller.LastTS(); ts != "" {
		d.registry.SetLastPollTS(ts)
		d.registry.Save()
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

// sendInput writes text to a session's PTY stdin via the sidecar socket.
func (d *Daemon) sendInput(sessionID, text string) {
	socketPath := sidecar.SocketPath(sessionID)

	// Wait briefly for Claude to be ready to accept input.
	time.Sleep(2 * time.Second)

	conn, err := net.DialTimeout("unix", socketPath, 3*time.Second)
	if err != nil {
		log.Printf("watch: sendInput connect failed for %s: %v", sessionID, err)
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))

	codec := protocol.NewCodec(conn)
	if err := codec.Send(protocol.Request{Action: "send_input", Text: text}); err != nil {
		log.Printf("watch: sendInput failed for %s: %v", sessionID, err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
