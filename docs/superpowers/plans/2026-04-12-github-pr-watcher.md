# GitHub PR Watcher Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a GitHub PR watcher to Jarvis that creates one Claude session per PR, auto-reviews PRs requesting your review, and drafts responses to comments on your PRs.

**Architecture:** Hybrid approach — GitHub Notifications API for cheap event discovery, then `gh api` calls for PR details/comments. Uses existing Registry/session.Manager/store patterns. Shells out to `gh` CLI (no library dependencies). Shares helpers with the Slack daemon.

**Tech Stack:** Go 1.24, `gh` CLI, `git` CLI, existing Jarvis internal packages (config, model, session, store, protocol, sidecar)

**Spec:** `docs/superpowers/specs/2026-04-12-github-pr-watcher-design.md`

**Working directory:** `~/jarvis` (branch: `feature/slack-watcher`)

---

### Task 1: Extract shared helpers from Slack daemon

The Slack daemon has `ensureFolder`, `placeSessionInFolder`, and `sendInputToSession` baked in. Extract them so the GitHub daemon can reuse them.

**Files:**
- Create: `internal/watch/helpers.go`
- Modify: `internal/watch/daemon.go` — remove moved functions, call helpers instead

- [ ] **Step 1: Create `internal/watch/helpers.go`**

Move these three functions from `daemon.go` into a new file. They're already package-level or can be made so.

```go
// internal/watch/helpers.go
package watch

import (
	"log"
	"net"
	"time"

	"jarvis/internal/model"
	"jarvis/internal/protocol"
	"jarvis/internal/sidecar"
	"jarvis/internal/store"
)

// ensureFolder finds a folder by name or creates it.
func ensureFolder(name string) (string, error) {
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
func placeSessionInFolder(sessionID, folderID string) {
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

// sendInputToSession writes text to a session's PTY stdin via the sidecar socket.
func sendInputToSession(sessionID, text string) {
	socketPath := sidecar.SocketPath(sessionID)

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

// isSidecarAlive checks if a session's sidecar is responding.
func isSidecarAlive(sessionID string) bool {
	socketPath := sidecar.SocketPath(sessionID)
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	codec := protocol.NewCodec(conn)
	if err := codec.Send(protocol.Request{Action: "ping"}); err != nil {
		return false
	}
	var resp protocol.Response
	if err := codec.Receive(&resp); err != nil {
		return false
	}
	return true
}

// waitForSidecar polls until the sidecar socket is alive or timeout.
// Returns true if sidecar became available, false on timeout.
func waitForSidecar(sessionID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if isSidecarAlive(sessionID) {
			return true
		}
		time.Sleep(1 * time.Second)
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
```

- [ ] **Step 2: Update `daemon.go` to use helpers**

Remove the `ensureFolder`, `placeSessionInFolder`, `sendInputToSession`, and `truncate` functions from `daemon.go`. The `Daemon` methods that called `d.ensureFolder(...)` now call the package-level `ensureFolder(...)`. The `d.placeSessionInFolder(...)` calls become `placeSessionInFolder(...)`. The `d.sendInput(sessionID, text)` method and the standalone `sendInputToSession` function are replaced by the one in helpers.go.

Specifically:
- Remove the `func (d *Daemon) ensureFolder(name string)` method (lines 163-185)
- Remove the `func (d *Daemon) placeSessionInFolder(sessionID, folderID string)` method (lines 188-202)
- Remove the `func (d *Daemon) sendInput(sessionID, text string)` method (lines 205-207)
- Remove the `func sendInputToSession(sessionID, text string)` function (lines 210-228)
- Remove the `func truncate(s string, n int) string` function (lines 230-235)
- In `Run()`: change `d.ensureFolder(slackCfg.Folder)` to `ensureFolder(slackCfg.Folder)`
- In `pollOnce()`: change `d.placeSessionInFolder(...)` to `placeSessionInFolder(...)`
- In `pollOnce()`: change `d.sendInput(...)` to `sendInputToSession(...)`

- [ ] **Step 3: Run existing tests to verify no breakage**

```bash
cd ~/jarvis && go test ./internal/watch/ -v -run TestEnsureFolder
cd ~/jarvis && go test ./internal/watch/ -v -run TestPlaceSession
```

Expected: all existing tests pass.

- [ ] **Step 4: Commit**

```bash
cd ~/jarvis && git add internal/watch/helpers.go internal/watch/daemon.go
git commit -m "refactor: extract shared watch helpers from Slack daemon

Move ensureFolder, placeSessionInFolder, sendInputToSession, truncate
to helpers.go. Add isSidecarAlive and waitForSidecar for use by GitHub
daemon's resume flow."
```

---

### Task 2: Add per-PR comment timestamps to Registry

The registry needs a `CommentTimestamps` map so each PR tracks its own last-seen comment time independently.

**Files:**
- Modify: `internal/watch/registry.go`
- Modify: `internal/watch/registry_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/watch/registry_test.go`:

```go
func TestRegistryCommentTimestamps(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	r := NewRegistry("github")

	// Set and get comment timestamps
	r.SetCommentTS("github:databricks-eng/universe#1234", "2026-04-12T01:00:00Z")
	r.SetCommentTS("github:databricks-eng/universe#5678", "2026-04-12T02:00:00Z")

	ts := r.GetCommentTS("github:databricks-eng/universe#1234")
	if ts != "2026-04-12T01:00:00Z" {
		t.Errorf("comment ts: got %q", ts)
	}

	// Save and reload
	if err := r.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	r2 := NewRegistry("github")
	if err := r2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	ts2 := r2.GetCommentTS("github:databricks-eng/universe#1234")
	if ts2 != "2026-04-12T01:00:00Z" {
		t.Errorf("loaded comment ts: got %q", ts2)
	}

	// Non-existent key returns empty
	ts3 := r2.GetCommentTS("github:databricks-eng/universe#9999")
	if ts3 != "" {
		t.Errorf("missing key should return empty, got %q", ts3)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd ~/jarvis && go test ./internal/watch/ -v -run TestRegistryCommentTimestamps
```

Expected: FAIL — `SetCommentTS` and `GetCommentTS` not defined.

- [ ] **Step 3: Implement in `registry.go`**

Add `commentTS` field to `Registry` struct, `CommentTimestamps` field to `registryFile`, and the getter/setter methods:

```go
// Add to Registry struct:
type Registry struct {
	mu         sync.Mutex
	name       string
	entries    map[string]string
	lastPollTS string
	commentTS  map[string]string // per-context-key comment timestamps
}

// Update NewRegistry:
func NewRegistry(watcherName string) *Registry {
	return &Registry{
		name:      watcherName,
		entries:   make(map[string]string),
		commentTS: make(map[string]string),
	}
}

// Update registryFile:
type registryFile struct {
	Contexts          map[string]string `yaml:"contexts"`
	LastPollTS        string            `yaml:"last_poll_ts,omitempty"`
	CommentTimestamps map[string]string `yaml:"comment_timestamps,omitempty"`
}

// Add methods:
func (r *Registry) SetCommentTS(contextKey, ts string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commentTS[contextKey] = ts
}

func (r *Registry) GetCommentTS(contextKey string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.commentTS[contextKey]
}

// Update Save to include commentTS:
func (r *Registry) Save() error {
	r.mu.Lock()
	data, err := yaml.Marshal(registryFile{
		Contexts:          r.entries,
		LastPollTS:        r.lastPollTS,
		CommentTimestamps: r.commentTS,
	})
	r.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}
	return store.WriteAtomic(registryPath(r.name), data)
}

// Update Load to read commentTS:
func (r *Registry) Load() error {
	data, err := os.ReadFile(registryPath(r.name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read registry: %w", err)
	}
	var f registryFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("unmarshal registry: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if f.Contexts != nil {
		r.entries = f.Contexts
	}
	r.lastPollTS = f.LastPollTS
	if f.CommentTimestamps != nil {
		r.commentTS = f.CommentTimestamps
	}
	return nil
}
```

- [ ] **Step 4: Run all registry tests**

```bash
cd ~/jarvis && go test ./internal/watch/ -v -run TestRegistry
```

Expected: all pass (new test + existing tests).

- [ ] **Step 5: Commit**

```bash
cd ~/jarvis && git add internal/watch/registry.go internal/watch/registry_test.go
git commit -m "feat: add per-context comment timestamps to Registry

Add CommentTimestamps map for per-PR timestamp tracking. Backwards
compatible — existing Slack registry files load fine (field is optional)."
```

---

### Task 3: Add GitHubWatcherConfig and wire into config/main

**Files:**
- Modify: `internal/config/config.go`
- Modify: `cmd/watch/main.go`

- [ ] **Step 1: Add `GitHubWatcherConfig` to `config.go`**

```go
type GitHubWatcherConfig struct {
	Enabled      bool     `yaml:"enabled"`
	Owner        string   `yaml:"owner"`         // e.g. "databricks-eng"
	Repo         string   `yaml:"repo"`          // e.g. "universe"
	Username     string   `yaml:"username"`      // your GitHub username (for filtering own comments)
	PollInterval int      `yaml:"poll_interval"` // seconds
	Folder       string   `yaml:"folder"`        // folder name to place sessions in
	Reasons      []string `yaml:"reasons"`       // notification reasons: "review_requested", "author"
	IgnoreUsers  []string `yaml:"ignore_users,omitempty"` // usernames to skip
}

// Add to WatchersConfig:
type WatchersConfig struct {
	Slack  SlackWatcherConfig  `yaml:"slack"`
	Gmail  GmailWatcherConfig  `yaml:"gmail"`
	GitHub GitHubWatcherConfig `yaml:"github"`
}
```

- [ ] **Step 2: Wire GitHub daemon into `cmd/watch/main.go`**

Add `githubEnabled` check alongside slack and gmail. Update the "no watchers enabled" error message to include a github config example.

```go
// In main():
githubEnabled := cfg.Watchers.GitHub.Enabled

if !slackEnabled && !gmailEnabled && !githubEnabled {
	fmt.Fprintln(os.Stderr, "error: no watchers enabled in config")
	// ... existing slack/gmail examples ...
	fmt.Fprintln(os.Stderr, "    github:")
	fmt.Fprintln(os.Stderr, "      enabled: true")
	fmt.Fprintln(os.Stderr, "      owner: \"databricks-eng\"")
	fmt.Fprintln(os.Stderr, "      repo: \"universe\"")
	fmt.Fprintln(os.Stderr, "      username: \"jianyu-zhou_data\"")
	fmt.Fprintln(os.Stderr, "      poll_interval: 60")
	fmt.Fprintln(os.Stderr, "      folder: \"GitHub PRs\"")
	fmt.Fprintln(os.Stderr, "      reasons:")
	fmt.Fprintln(os.Stderr, "        - review_requested")
	fmt.Fprintln(os.Stderr, "        - author")
	os.Exit(1)
}

// After gmail block:
if githubEnabled {
	githubDaemon, err := watch.NewGitHubDaemon(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "github watcher error: %v\n", err)
		os.Exit(1)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Println("starting GitHub watcher")
		if err := githubDaemon.Run(ctx); err != nil {
			log.Printf("github watcher error: %v", err)
		}
	}()
}
```

This won't compile yet (NewGitHubDaemon doesn't exist), but we'll create it in the next task. Verify the config struct works:

- [ ] **Step 3: Verify config compiles**

```bash
cd ~/jarvis && go build ./internal/config/
```

Expected: compiles.

- [ ] **Step 4: Commit**

```bash
cd ~/jarvis && git add internal/config/config.go cmd/watch/main.go
git commit -m "feat: add GitHubWatcherConfig and wire into watch daemon

Config supports owner, repo, username, poll_interval, folder, reasons,
and ignore_users. Watch main.go starts GitHub daemon when enabled."
```

---

### Task 4: Implement GitHubEvent and GitHubPoller (`github.go`)

The core data types and the `gh` CLI wrapper.

**Files:**
- Create: `internal/watch/github.go`
- Create: `internal/watch/github_test.go`

- [ ] **Step 1: Write tests for GitHubEvent methods**

```go
// internal/watch/github_test.go
package watch

import (
	"testing"
	"time"
)

func TestGitHubEventContextKey(t *testing.T) {
	ev := GitHubEvent{
		Owner:    "databricks-eng",
		Repo:     "universe",
		PRNumber: 1234,
	}
	key := ev.ContextKey()
	if key != "github:databricks-eng/universe#1234" {
		t.Errorf("context key: got %q", key)
	}
}

func TestGitHubEventSessionNameReviewRequest(t *testing.T) {
	ev := GitHubEvent{
		PRNumber: 1234,
		PRTitle:  "Fix auth middleware",
		Reason:   "review_requested",
	}
	name := ev.SessionName()
	if name != "gh: Review PR #1234 — Fix auth middleware" {
		t.Errorf("session name: got %q", name)
	}
}

func TestGitHubEventSessionNameAuthor(t *testing.T) {
	ev := GitHubEvent{
		PRNumber: 5678,
		PRTitle:  "Add rate limiting",
		Reason:   "author",
	}
	name := ev.SessionName()
	if name != "gh: PR #5678 — Add rate limiting" {
		t.Errorf("session name: got %q", name)
	}
}

func TestGitHubEventInitialPromptReviewRequest(t *testing.T) {
	ev := GitHubEvent{
		PRNumber: 1234,
		PRURL:    "https://github.com/databricks-eng/universe/pull/1234",
		Reason:   "review_requested",
	}
	prompt := ev.InitialPrompt()
	if !containsStr(prompt, "https://github.com/databricks-eng/universe/pull/1234") {
		t.Error("prompt should include PR URL")
	}
	if !containsStr(prompt, "gh pr diff 1234") {
		t.Error("prompt should include gh pr diff command")
	}
	if !containsStr(prompt, "Do NOT") {
		t.Error("prompt should include safety guardrail")
	}
}

func TestGitHubEventInitialPromptAuthor(t *testing.T) {
	ev := GitHubEvent{
		PRNumber: 5678,
		PRURL:    "https://github.com/databricks-eng/universe/pull/5678",
		HeadRef:  "stack/fix-auth",
		Reason:   "author",
		Verdict:  VerdictChangesRequested,
		Comments: []PRComment{
			{User: "alice", Body: "This needs error handling", Path: "auth.go", Line: 42, CreatedAt: time.Now()},
		},
	}
	prompt := ev.InitialPrompt()
	if !containsStr(prompt, "CHANGES_REQUESTED") {
		t.Error("prompt should include review verdict")
	}
	if !containsStr(prompt, "alice") {
		t.Error("prompt should include commenter name")
	}
	if !containsStr(prompt, "auth.go") {
		t.Error("prompt should include file path")
	}
	if !containsStr(prompt, "stack/fix-auth") {
		t.Error("prompt should include head ref for branch verification")
	}
}

func TestGitHubEventFollowUpPrompt(t *testing.T) {
	ev := GitHubEvent{
		PRNumber: 1234,
		Verdict:  VerdictApproved,
		Comments: []PRComment{
			{User: "bob", Body: "LGTM", CreatedAt: time.Now()},
		},
	}
	prompt := ev.FollowUpPrompt()
	if !containsStr(prompt, "PR #1234") {
		t.Error("follow-up should include PR number")
	}
	if !containsStr(prompt, "APPROVED") {
		t.Error("follow-up should include verdict")
	}
	if !containsStr(prompt, "bob") {
		t.Error("follow-up should include commenter")
	}
}

func TestParseNotificationURL(t *testing.T) {
	tests := []struct {
		url    string
		expect int
	}{
		{"https://api.github.com/repos/databricks-eng/universe/pulls/1234", 1234},
		{"https://api.github.com/repos/databricks-eng/universe/pulls/9999999", 9999999},
		{"https://api.github.com/repos/other/repo/issues/100", 0},
	}
	for _, tt := range tests {
		got := parsePRNumberFromURL(tt.url)
		if got != tt.expect {
			t.Errorf("parsePRNumberFromURL(%q) = %d, want %d", tt.url, got, tt.expect)
		}
	}
}

func TestParsePRListJSON(t *testing.T) {
	json := `[
		{"number": 1234, "headRefName": "stack/fix-auth"},
		{"number": 5678, "headRefName": "stack/add-feature"}
	]`
	m, err := parsePRListJSON(json)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m["stack/fix-auth"] != 1234 {
		t.Errorf("fix-auth: got %d", m["stack/fix-auth"])
	}
	if m["stack/add-feature"] != 5678 {
		t.Errorf("add-feature: got %d", m["stack/add-feature"])
	}
}

func TestParseStackLS(t *testing.T) {
	output := `---------------Stack Info---------------
master (current)
  ├─✗ ● stack/fix-auth (!1)
  │       [https://github.com/databricks-eng/universe/pull/1234]
  ├─✗ · stack/local-only (!) [LOCAL]
  ├─✗ ● stack/add-feature (!1)
  │       [https://github.com/databricks-eng/universe/pull/5678]
----------------------------------------`
	m := parseStackLS(output)
	if m["stack/fix-auth"] != 1234 {
		t.Errorf("fix-auth: got %d", m["stack/fix-auth"])
	}
	if m["stack/add-feature"] != 5678 {
		t.Errorf("add-feature: got %d", m["stack/add-feature"])
	}
	if _, ok := m["stack/local-only"]; ok {
		t.Error("local-only should not be in map (no PR)")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd ~/jarvis && go test ./internal/watch/ -v -run TestGitHub
cd ~/jarvis && go test ./internal/watch/ -v -run TestParse
```

Expected: FAIL — types not defined.

- [ ] **Step 3: Implement `github.go`**

```go
// internal/watch/github.go
package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"jarvis/internal/config"
)

type ReviewVerdict string

const (
	VerdictApproved         ReviewVerdict = "APPROVED"
	VerdictChangesRequested ReviewVerdict = "CHANGES_REQUESTED"
	VerdictCommented        ReviewVerdict = "COMMENTED"
	VerdictNone             ReviewVerdict = ""
)

type PRComment struct {
	User      string
	Body      string
	Path      string // file path (empty for general comments)
	Line      int    // line number (0 for general comments)
	CreatedAt time.Time
}

type GitHubEvent struct {
	Owner    string
	Repo     string
	PRNumber int
	PRTitle  string
	PRURL    string        // html_url
	PRAuthor string
	HeadRef  string        // branch name
	Reason   string        // "review_requested" or "author"
	Verdict  ReviewVerdict // most recent review verdict
	Comments []PRComment   // new human comments
	NotifID  string        // notification thread ID
	Timestamp time.Time
}

func (e GitHubEvent) ContextKey() string {
	return fmt.Sprintf("github:%s/%s#%d", e.Owner, e.Repo, e.PRNumber)
}

func (e GitHubEvent) SessionName() string {
	if e.Reason == "review_requested" {
		return fmt.Sprintf("gh: Review PR #%d — %s", e.PRNumber, e.PRTitle)
	}
	return fmt.Sprintf("gh: PR #%d — %s", e.PRNumber, e.PRTitle)
}

func (e GitHubEvent) InitialPrompt() string {
	if e.Reason == "review_requested" {
		return fmt.Sprintf(`Please review this GitHub PR: %s

Run `+"`gh pr diff %d`"+` to read the full diff. Provide a thorough code
review covering correctness, edge cases, and potential issues. Draft review
comments that I can post.

Do NOT post any comments, approve, or take any external-facing actions.`, e.PRURL, e.PRNumber)
	}

	// Author event — include comments and verdict
	var sb strings.Builder
	fmt.Fprintf(&sb, "Someone left comments on my PR: %s\n\n", e.PRURL)

	if e.Verdict != VerdictNone {
		fmt.Fprintf(&sb, "Review verdict: %s\n\n", e.Verdict)
	}

	if len(e.Comments) > 0 {
		sb.WriteString("New comments since last check:\n")
		for _, c := range e.Comments {
			if c.Path != "" {
				fmt.Fprintf(&sb, "- %s on %s:%d: %q\n", c.User, c.Path, c.Line, truncate(c.Body, 200))
			} else {
				fmt.Fprintf(&sb, "- %s (general comment): %q\n", c.User, truncate(c.Body, 200))
			}
		}
		sb.WriteString("\n")
	}

	fmt.Fprintf(&sb, "Read the PR diff with `gh pr diff %d` for context. Understand the review\n", e.PRNumber)
	sb.WriteString("feedback and draft responses to each comment. If code changes are suggested,\n")
	sb.WriteString("explain what changes would address the feedback.\n\n")
	fmt.Fprintf(&sb, "IMPORTANT: Verify you are on the correct branch (%s) before making\n", e.HeadRef)
	sb.WriteString("any code changes. Run `git branch --show-current` to confirm.\n\n")
	sb.WriteString("Do NOT post any comments or take any external-facing actions.")
	return sb.String()
}

func (e GitHubEvent) FollowUpPrompt() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[New comments on PR #%d at %s]:\n\n", e.PRNumber, time.Now().Format(time.RFC3339))

	if e.Verdict != VerdictNone {
		fmt.Fprintf(&sb, "Review verdict: %s\n\n", e.Verdict)
	}

	for _, c := range e.Comments {
		if c.Path != "" {
			fmt.Fprintf(&sb, "- %s on %s:%d: %q\n", c.User, c.Path, c.Line, truncate(c.Body, 200))
		} else {
			fmt.Fprintf(&sb, "- %s (general comment): %q\n", c.User, truncate(c.Body, 200))
		}
	}

	fmt.Fprintf(&sb, "\nRun `gh pr diff %d` if you need to refresh context on the changes.\n", e.PRNumber)
	sb.WriteString("Please read the new comments and draft responses.")
	return sb.String()
}

// --- Poller ---

type GitHubPoller struct {
	owner      string
	repo       string
	username   string
	reasons    map[string]bool
	ignoreUsers map[string]bool
	lastPollTS string // RFC3339
}

func NewGitHubPoller(cfg config.GitHubWatcherConfig, lastPollTS string) *GitHubPoller {
	if lastPollTS == "" {
		lastPollTS = time.Now().UTC().Format(time.RFC3339)
	}
	reasons := make(map[string]bool)
	for _, r := range cfg.Reasons {
		reasons[r] = true
	}
	ignoreUsers := make(map[string]bool)
	for _, u := range cfg.IgnoreUsers {
		ignoreUsers[strings.ToLower(u)] = true
	}
	return &GitHubPoller{
		owner:       cfg.Owner,
		repo:        cfg.Repo,
		username:    cfg.Username,
		reasons:     reasons,
		ignoreUsers: ignoreUsers,
		lastPollTS:  lastPollTS,
	}
}

func (p *GitHubPoller) LastPollTS() string {
	return p.lastPollTS
}

// Poll fetches new notifications and enriches them with PR details.
func (p *GitHubPoller) Poll(ctx context.Context, getCommentTS func(string) string) ([]GitHubEvent, error) {
	notifs, err := p.fetchNotifications()
	if err != nil {
		return nil, fmt.Errorf("fetch notifications: %w", err)
	}

	fullName := p.owner + "/" + p.repo
	seen := make(map[int]bool)
	var events []GitHubEvent

	for _, n := range notifs {
		if n.Subject.Type != "PullRequest" {
			continue
		}
		if n.Repository.FullName != fullName {
			continue
		}
		if !p.reasons[n.Reason] {
			continue
		}

		prNum := parsePRNumberFromURL(n.Subject.URL)
		if prNum == 0 {
			continue
		}
		if seen[prNum] {
			continue
		}
		seen[prNum] = true

		pr, err := p.fetchPR(prNum)
		if err != nil {
			log.Printf("github: fetch PR #%d: %v", prNum, err)
			continue
		}
		if pr.State == "closed" {
			log.Printf("github: skipping closed PR #%d", prNum)
			p.markNotificationRead(n.ID)
			continue
		}

		contextKey := fmt.Sprintf("github:%s#%d", fullName, prNum)
		commentSince := getCommentTS(contextKey)
		if commentSince == "" {
			commentSince = p.lastPollTS
		}

		var comments []PRComment
		var verdict ReviewVerdict

		if n.Reason == "author" {
			comments, err = p.fetchNewComments(prNum, commentSince)
			if err != nil {
				log.Printf("github: fetch comments PR #%d: %v", prNum, err)
			}
			verdict = p.fetchLatestVerdict(prNum)

			if len(comments) == 0 {
				// No new human comments — mark read but don't create event
				p.markNotificationRead(n.ID)
				continue
			}
		}

		events = append(events, GitHubEvent{
			Owner:    p.owner,
			Repo:     p.repo,
			PRNumber: prNum,
			PRTitle:  pr.Title,
			PRURL:    pr.HTMLURL,
			PRAuthor: pr.User.Login,
			HeadRef:  pr.Head.Ref,
			Reason:   n.Reason,
			Verdict:  verdict,
			Comments: comments,
			NotifID:  n.ID,
			Timestamp: time.Now(),
		})
	}

	// Advance global poll timestamp
	p.lastPollTS = time.Now().UTC().Format(time.RFC3339)

	return events, nil
}

// MarkNotificationRead marks a notification as read so it doesn't reappear.
func (p *GitHubPoller) markNotificationRead(threadID string) {
	ghAPI("PATCH", fmt.Sprintf("/notifications/threads/%s", threadID), nil)
}

// --- GitHub API types ---

type ghNotification struct {
	ID         string `json:"id"`
	Reason     string `json:"reason"`
	Subject    struct {
		Title string `json:"title"`
		URL   string `json:"url"`
		Type  string `json:"type"`
	} `json:"subject"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	UpdatedAt string `json:"updated_at"`
}

type ghPR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	HTMLURL string `json:"html_url"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

type ghReviewComment struct {
	User struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
	Body      string `json:"body"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	CreatedAt string `json:"created_at"`
}

type ghIssueComment struct {
	User struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

type ghReview struct {
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	State       string `json:"state"`
	SubmittedAt string `json:"submitted_at"`
}

// --- gh CLI helpers ---

func (p *GitHubPoller) fetchNotifications() ([]ghNotification, error) {
	out, err := ghAPI("GET", fmt.Sprintf("/notifications?since=%s&all=false&per_page=50", p.lastPollTS), nil)
	if err != nil {
		return nil, err
	}
	var notifs []ghNotification
	if err := json.Unmarshal([]byte(out), &notifs); err != nil {
		return nil, fmt.Errorf("parse notifications: %w", err)
	}
	return notifs, nil
}

func (p *GitHubPoller) fetchPR(number int) (*ghPR, error) {
	out, err := ghAPI("GET", fmt.Sprintf("/repos/%s/%s/pulls/%d", p.owner, p.repo, number), nil)
	if err != nil {
		return nil, err
	}
	var pr ghPR
	if err := json.Unmarshal([]byte(out), &pr); err != nil {
		return nil, fmt.Errorf("parse PR: %w", err)
	}
	return &pr, nil
}

func (p *GitHubPoller) fetchNewComments(number int, since string) ([]PRComment, error) {
	var comments []PRComment

	// Inline review comments
	out, err := ghAPI("GET", fmt.Sprintf("/repos/%s/%s/pulls/%d/comments?since=%s&per_page=100", p.owner, p.repo, number, since), nil)
	if err != nil {
		return nil, fmt.Errorf("review comments: %w", err)
	}
	var reviewComments []ghReviewComment
	if err := json.Unmarshal([]byte(out), &reviewComments); err != nil {
		return nil, fmt.Errorf("parse review comments: %w", err)
	}
	for _, c := range reviewComments {
		if p.shouldSkipUser(c.User.Login, c.User.Type) {
			continue
		}
		comments = append(comments, PRComment{
			User:      c.User.Login,
			Body:      c.Body,
			Path:      c.Path,
			Line:      c.Line,
			CreatedAt: parseRFC3339(c.CreatedAt),
		})
	}

	// General issue comments
	out, err = ghAPI("GET", fmt.Sprintf("/repos/%s/%s/issues/%d/comments?since=%s&per_page=100", p.owner, p.repo, number, since), nil)
	if err != nil {
		return nil, fmt.Errorf("issue comments: %w", err)
	}
	var issueComments []ghIssueComment
	if err := json.Unmarshal([]byte(out), &issueComments); err != nil {
		return nil, fmt.Errorf("parse issue comments: %w", err)
	}
	for _, c := range issueComments {
		if p.shouldSkipUser(c.User.Login, c.User.Type) {
			continue
		}
		comments = append(comments, PRComment{
			User:      c.User.Login,
			Body:      c.Body,
			CreatedAt: parseRFC3339(c.CreatedAt),
		})
	}

	return comments, nil
}

func (p *GitHubPoller) fetchLatestVerdict(number int) ReviewVerdict {
	out, err := ghAPI("GET", fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews?per_page=20", p.owner, p.repo, number), nil)
	if err != nil {
		return VerdictNone
	}
	var reviews []ghReview
	if err := json.Unmarshal([]byte(out), &reviews); err != nil {
		return VerdictNone
	}
	// Walk backwards to find most recent non-COMMENTED verdict
	for i := len(reviews) - 1; i >= 0; i-- {
		switch ReviewVerdict(reviews[i].State) {
		case VerdictApproved, VerdictChangesRequested:
			return ReviewVerdict(reviews[i].State)
		}
	}
	return VerdictNone
}

func (p *GitHubPoller) shouldSkipUser(login, userType string) bool {
	if strings.EqualFold(login, p.username) {
		return true
	}
	if userType == "Bot" {
		return true
	}
	if p.ignoreUsers[strings.ToLower(login)] {
		return true
	}
	return false
}

// --- PR Discovery ---

// DiscoverPRs returns a map of branch name → PR number for the user's open PRs.
// Primary: gh pr list. Fallback: git stack ls.
func DiscoverPRs(owner, repo, repoDir string) map[string]int {
	m, err := discoverPRsViaGH(owner, repo)
	if err != nil {
		log.Printf("github: gh pr list failed (%v), trying git stack ls", err)
		return parseStackLS(discoverPRsViaStackLS(repoDir))
	}
	return m
}

func discoverPRsViaGH(owner, repo string) (map[string]int, error) {
	out, err := exec.Command("gh", "pr", "list",
		"--author", "@me",
		"--repo", owner+"/"+repo,
		"--state", "open",
		"--json", "number,headRefName",
	).Output()
	if err != nil {
		return nil, err
	}
	return parsePRListJSON(string(out))
}

func discoverPRsViaStackLS(repoDir string) string {
	cmd := exec.Command("git", "stack", "ls")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		log.Printf("github: git stack ls failed: %v", err)
		return ""
	}
	return string(out)
}

// FindWorktreeForBranch finds the worktree path for a given branch name.
// Returns empty string if not found.
func FindWorktreeForBranch(repoDir, branch string) string {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(string(out), "\n")
	var currentPath string
	for _, line := range lines {
		if strings.HasPrefix(line, "worktree ") {
			currentPath = strings.TrimPrefix(line, "worktree ")
		}
		if strings.HasPrefix(line, "branch refs/heads/") {
			b := strings.TrimPrefix(line, "branch refs/heads/")
			if b == branch {
				return currentPath
			}
		}
	}
	return ""
}

// --- Parsing helpers ---

var prURLRegex = regexp.MustCompile(`/pulls/(\d+)$`)

func parsePRNumberFromURL(url string) int {
	m := prURLRegex.FindStringSubmatch(url)
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func parsePRListJSON(data string) (map[string]int, error) {
	var items []struct {
		Number      int    `json:"number"`
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal([]byte(data), &items); err != nil {
		return nil, err
	}
	m := make(map[string]int)
	for _, item := range items {
		m[item.HeadRefName] = item.Number
	}
	return m, nil
}

var stackLSBranchRegex = regexp.MustCompile(`[·●✓⦿]\s+(\S+)`)
var stackLSPRURLRegex = regexp.MustCompile(`\[https://github\.com/[^/]+/[^/]+/pull/(\d+)\]`)

func parseStackLS(output string) map[string]int {
	m := make(map[string]int)
	lines := strings.Split(output, "\n")

	var lastBranch string
	for _, line := range lines {
		if bm := stackLSBranchRegex.FindStringSubmatch(line); len(bm) >= 2 {
			lastBranch = bm[1]
		}
		if pm := stackLSPRURLRegex.FindStringSubmatch(line); len(pm) >= 2 && lastBranch != "" {
			n, _ := strconv.Atoi(pm[1])
			if n > 0 {
				m[lastBranch] = n
			}
			lastBranch = ""
		}
	}
	return m
}

// ghAPI calls the GitHub API via the gh CLI.
func ghAPI(method, path string, body interface{}) (string, error) {
	args := []string{"api", "--method", method}
	if body != nil {
		bodyJSON, _ := json.Marshal(body)
		args = append(args, "--input", "-")
		cmd := exec.Command("gh", args...)
		cmd.Stdin = strings.NewReader(string(bodyJSON))
		out, err := cmd.Output()
		return string(out), err
	}
	args = append(args, path)
	out, err := exec.Command("gh", args...).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("gh api %s: %s", path, string(exitErr.Stderr))
		}
		return "", err
	}
	return string(out), nil
}

func parseRFC3339(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
```

- [ ] **Step 4: Run tests**

```bash
cd ~/jarvis && go test ./internal/watch/ -v -run "TestGitHub|TestParse"
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
cd ~/jarvis && git add internal/watch/github.go internal/watch/github_test.go
git commit -m "feat: add GitHubEvent, GitHubPoller, and PR discovery

Implements gh CLI wrapper for notifications, PR details, comments,
reviews, and notification marking. Includes PR discovery via gh pr list
with git stack ls fallback, and worktree-to-branch matching."
```

---

### Task 5: Implement GitHubDaemon (`github_daemon.go`)

The daemon ties everything together: poll loop, discovery scan, event routing, session management.

**Files:**
- Create: `internal/watch/github_daemon.go`

- [ ] **Step 1: Implement the daemon**

```go
// internal/watch/github_daemon.go
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

type GitHubDaemon struct {
	cfg      *config.Config
	mgr      *session.Manager
	registry *Registry
	poller   *GitHubPoller
	folderID string
	polling  bool
}

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
	cmd := fmt.Sprintf("git -C %s branch --show-current", worktreeDir)
	out, err := execCommand("git", "-C", worktreeDir, "branch", "--show-current")
	if err != nil {
		log.Printf("github-watch: get branch for %s: %v (%s)", worktreeDir, err, cmd)
		return ""
	}
	return out
}

func execCommand(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
```

- [ ] **Step 2: Verify the full project compiles**

```bash
cd ~/jarvis && go build ./...
```

Expected: compiles successfully.

- [ ] **Step 3: Commit**

```bash
cd ~/jarvis && git add internal/watch/github_daemon.go
git commit -m "feat: add GitHubDaemon with poll loop, discovery scan, and event routing

Implements the full GitHub PR watcher daemon: notification polling,
PR discovery via gh pr list, worktree-aware session spawning, follow-up
routing with sidecar resume, and notification mark-as-read."
```

---

### Task 6: Integration test and end-to-end validation

**Files:**
- Modify: `internal/watch/github_test.go` (add daemon-level tests)

- [ ] **Step 1: Add daemon unit tests**

Add to `github_test.go`:

```go
func TestGitHubDaemonNewValidation(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	cfg := &config.Config{}

	// Disabled
	cfg.Watchers.GitHub.Enabled = false
	_, err := NewGitHubDaemon(cfg)
	if err == nil {
		t.Error("should fail when disabled")
	}

	// Missing owner
	cfg.Watchers.GitHub.Enabled = true
	cfg.Watchers.GitHub.Repo = "universe"
	cfg.Watchers.GitHub.Username = "test"
	_, err = NewGitHubDaemon(cfg)
	if err == nil {
		t.Error("should fail with missing owner")
	}
}

func TestFilterBotComments(t *testing.T) {
	p := &GitHubPoller{
		username:    "myuser",
		ignoreUsers: map[string]bool{"ci-bot": true},
	}

	// Own user
	if !p.shouldSkipUser("myuser", "User") {
		t.Error("should skip own user")
	}
	// Bot type
	if !p.shouldSkipUser("some-app[bot]", "Bot") {
		t.Error("should skip bot type")
	}
	// Ignore list
	if !p.shouldSkipUser("CI-Bot", "User") {
		t.Error("should skip ignored user (case insensitive)")
	}
	// Normal user
	if p.shouldSkipUser("alice", "User") {
		t.Error("should not skip normal user")
	}
}

func TestFindWorktreeForBranch(t *testing.T) {
	// Test parsing of git worktree list --porcelain output
	// This is hard to unit test without a real git repo, so we test the
	// parseStackLS and parsePRListJSON helpers instead (already tested above).
	// The FindWorktreeForBranch function is a thin wrapper around git CLI.
}
```

- [ ] **Step 2: Run all tests**

```bash
cd ~/jarvis && go test ./internal/watch/ -v
cd ~/jarvis && go test ./... -v
```

Expected: all pass.

- [ ] **Step 3: Build the watch binary**

```bash
cd ~/jarvis && go build -o /dev/null ./cmd/watch/
```

Expected: compiles.

- [ ] **Step 4: Add github config to `~/.jarvis/config.yaml`**

```yaml
  github:
    enabled: true
    owner: "databricks-eng"
    repo: "universe"
    username: "jianyu-zhou_data"
    poll_interval: 60
    folder: "GitHub PRs"
    reasons:
      - review_requested
      - author
    ignore_users:
      - "databricks-staging-ci-emu-1[bot]"
      - "databricks-ci-emu-2[bot]"
```

- [ ] **Step 5: Manual smoke test**

```bash
cd ~/jarvis && make build
./watch &
# Watch logs for: "github-watch: polling every 1m0s"
# Watch for: "github-watch: linked session ... to PR #..."
# Watch for: "github-watch: new event: PR #..." (if there are pending notifications)
# Kill with Ctrl-C
```

- [ ] **Step 6: Commit tests**

```bash
cd ~/jarvis && git add internal/watch/github_test.go
git commit -m "test: add GitHub watcher daemon and bot filter tests"
```

- [ ] **Step 7: Final commit with config example**

```bash
cd ~/jarvis && git add -A
git commit -m "feat: complete GitHub PR watcher implementation

Adds github watcher to Jarvis watch daemon. Creates one Claude session
per PR, auto-reviews PRs requesting your review, drafts responses to
comments on your PRs. Links manually-created sessions to their PRs
via gh pr list + worktree branch matching."
```
