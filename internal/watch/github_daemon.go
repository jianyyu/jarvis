package watch

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"jarvis/internal/config"
	"jarvis/internal/model"
	"jarvis/internal/session"
	"jarvis/internal/store"
)

// GitHubDaemon polls GitHub notifications and creates/routes Claude sessions per PR.
type GitHubDaemon struct {
	cfg      *config.Config
	mgr      *session.Manager
	registry *Registry
	poller   *GitHubPoller
	folderID string
	polling  bool
}

// NewGitHubDaemon creates a GitHub watcher daemon from config.
func NewGitHubDaemon(cfg *config.Config) (*GitHubDaemon, error) {
	ghCfg := cfg.Watchers.GitHub
	if !ghCfg.Enabled {
		return nil, fmt.Errorf("github watcher not enabled in config")
	}
	if ghCfg.Owner == "" || ghCfg.Repo == "" {
		return nil, fmt.Errorf("github watcher owner and repo must be configured")
	}
	if ghCfg.Username == "" {
		return nil, fmt.Errorf("github watcher username must be configured")
	}

	// Validate gh CLI auth
	if _, err := ghAPI("GET", "/user", nil); err != nil {
		return nil, fmt.Errorf("gh CLI not authenticated: %w", err)
	}

	reg := NewRegistry("github")
	if err := reg.Load(); err != nil {
		log.Printf("github-watch: registry load error (starting fresh): %v", err)
	}

	return &GitHubDaemon{
		cfg:      cfg,
		mgr:      session.NewManager(cfg),
		registry: reg,
		poller:   NewGitHubPoller(ghCfg, reg.GetLastPollTS()),
	}, nil
}

// Run starts the polling loop. Blocks until ctx is cancelled.
func (d *GitHubDaemon) Run(ctx context.Context) error {
	ghCfg := d.cfg.Watchers.GitHub
	interval := time.Duration(ghCfg.PollInterval) * time.Second
	if interval < 30*time.Second {
		interval = 60 * time.Second
	}

	if ghCfg.Folder != "" {
		id, err := ensureFolder(ghCfg.Folder)
		if err != nil {
			return fmt.Errorf("ensure folder: %w", err)
		}
		d.folderID = id
		log.Printf("github-watch: sessions will be placed in folder %q (%s)", ghCfg.Folder, id)
	}

	log.Printf("github-watch: polling every %s (repo: %s/%s)", interval, ghCfg.Owner, ghCfg.Repo)

	d.pollOnce(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("github-watch: shutting down")
			return nil
		case <-ticker.C:
			d.pollOnce(ctx)
		}
	}
}

func (d *GitHubDaemon) pollOnce(ctx context.Context) {
	if d.polling {
		log.Printf("github-watch: skipping poll — previous still running")
		return
	}
	d.polling = true
	defer func() { d.polling = false }()

	ghCfg := d.cfg.Watchers.GitHub
	repoDir := d.cfg.RepoPath()

	// Step 1: PR Discovery — link manual sessions to their PRs
	d.discoveryScan(ghCfg.Owner, ghCfg.Repo, repoDir)

	// Step 2: Poll notifications and process events
	events, err := d.poller.Poll(ctx, d.registry.GetCommentTS)
	if err != nil {
		log.Printf("github-watch: poll error: %v", err)
		return
	}

	for _, ev := range events {
		d.processEvent(ev, repoDir)
	}

	// Step 3: Persist poll checkpoint
	d.registry.SetLastPollTS(d.poller.LastPollTS())
	if err := d.registry.Save(); err != nil {
		log.Printf("github-watch: registry save error: %v", err)
	}

	log.Printf("github-watch: poll done — %d events", len(events))
}

func (d *GitHubDaemon) discoveryScan(owner, repo, repoDir string) {
	if repoDir == "" {
		return
	}

	branchToPR := DiscoverPRs(owner, repo, repoDir)
	if len(branchToPR) == 0 {
		return
	}

	sessions, err := store.ListSessions(&store.SessionFilter{
		StatusIn: []model.SessionStatus{model.StatusActive, model.StatusSuspended},
	})
	if err != nil {
		log.Printf("github-watch: list sessions: %v", err)
		return
	}

	fullName := owner + "/" + repo
	for _, sess := range sessions {
		if sess.WorktreeDir == "" || sess.WorktreeDir == repoDir {
			continue // skip master/repo-root sessions
		}

		branch := getBranchForWorktree(sess.WorktreeDir)
		if branch == "" {
			continue
		}

		prNum, ok := branchToPR[branch]
		if !ok {
			continue
		}

		key := fmt.Sprintf("github:%s#%d", fullName, prNum)
		if _, found := d.registry.Lookup(key); found {
			continue // already registered
		}

		d.registry.Register(key, sess.ID)
		log.Printf("github-watch: linked session %q (%s) to PR #%d via branch %s",
			sess.Name, sess.ID, prNum, branch)
	}
}

func (d *GitHubDaemon) processEvent(ev GitHubEvent, repoDir string) {
	key := ev.ContextKey()

	// Check if we already have a session for this PR
	if sessID, found := d.registry.Lookup(key); found {
		sess, err := store.GetSession(sessID)
		if err != nil {
			// Session was deleted — remove stale entry and recreate
			log.Printf("github-watch: session %s deleted, removing stale registry entry for PR #%d", sessID, ev.PRNumber)
			d.registry.Unregister(key)
		} else {
			// Session exists — send follow-up
			d.sendFollowUp(sess, ev)
			d.registry.SetCommentTS(key, time.Now().UTC().Format(time.RFC3339))
			d.poller.markNotificationRead(ev.NotifID)
			d.registry.Save()
			return
		}
	}

	// No existing session — spawn new one
	log.Printf("github-watch: new event: PR #%d (%s) — %s", ev.PRNumber, ev.Reason, truncate(ev.PRTitle, 60))

	cwd := d.cfg.RepoPath()
	if cwd == "" {
		cwd = "."
	}

	// For author events, try to find the correct worktree
	if ev.Reason == "author" && ev.HeadRef != "" && repoDir != "" {
		if wtDir := FindWorktreeForBranch(repoDir, ev.HeadRef); wtDir != "" {
			cwd = wtDir
			log.Printf("github-watch: using worktree %s for branch %s", wtDir, ev.HeadRef)
		}
	}

	sess, err := d.mgr.Spawn(ev.SessionName(), cwd, []string{"claude"})
	if err != nil {
		log.Printf("github-watch: spawn failed for PR #%d: %v", ev.PRNumber, err)
		return
	}

	if d.folderID != "" {
		placeSessionInFolder(sess.ID, d.folderID)
	}

	d.registry.Register(key, sess.ID)
	d.registry.SetCommentTS(key, time.Now().UTC().Format(time.RFC3339))
	d.poller.markNotificationRead(ev.NotifID)
	if err := d.registry.Save(); err != nil {
		log.Printf("github-watch: registry save error: %v", err)
	}

	// Inject prompt via PTY
	sendInputToSession(sess.ID, ev.InitialPrompt())
	time.Sleep(500 * time.Millisecond)
	sendInputToSession(sess.ID, "\r")

	log.Printf("github-watch: created session %q (%s) for PR #%d", sess.Name, sess.ID, ev.PRNumber)
}

func (d *GitHubDaemon) sendFollowUp(sess *model.Session, ev GitHubEvent) {
	if len(ev.Comments) == 0 && ev.Reason != "review_requested" {
		return
	}

	// Resume if sidecar is dead
	if !isSidecarAlive(sess.ID) {
		log.Printf("github-watch: resuming suspended session %s for PR #%d", sess.ID, ev.PRNumber)
		if err := d.mgr.Resume(sess); err != nil {
			log.Printf("github-watch: resume failed for %s: %v", sess.ID, err)
			return
		}
		if !waitForSidecar(sess.ID, 10*time.Second) {
			log.Printf("github-watch: sidecar not ready after 10s for %s, will retry next poll", sess.ID)
			return
		}
	}

	prompt := ev.FollowUpPrompt()
	log.Printf("github-watch: follow-up in session %s — %d new comments on PR #%d",
		sess.ID, len(ev.Comments), ev.PRNumber)
	sendInputToSession(sess.ID, prompt)
	time.Sleep(500 * time.Millisecond)
	sendInputToSession(sess.ID, "\r")
}

// getBranchForWorktree returns the current branch of a worktree directory.
func getBranchForWorktree(worktreeDir string) string {
	out, err := exec.Command("git", "-C", worktreeDir, "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
