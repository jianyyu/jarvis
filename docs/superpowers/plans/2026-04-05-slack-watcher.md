# Slack Watcher Daemon Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `jarvis-watch`, a persistent daemon that polls Slack for DMs and @mentions, creates jarvis sessions to investigate and draft responses, and places them under a configured folder.

**Architecture:** `jarvis-watch` is a standalone binary that shares only the filesystem (`~/.jarvis/`) with `jarvis`. It uses the Slack Go SDK to poll for new messages, a YAML-based context registry to avoid duplicate sessions, and the existing session manager + sidecar infrastructure to spawn Claude Code sessions. Sessions are created under a configured folder (e.g. "Slack") with a system prompt instructing Claude to analyze the message and prepare a draft response without taking any external actions.

**Tech Stack:** Go 1.24, `github.com/slack-go/slack`, gopkg.in/yaml.v3, existing jarvis internals (session, store, config, sidecar)

---

## File Structure

| Action | Path | Responsibility |
|--------|------|----------------|
| Create | `cmd/watch/main.go` | Entry point for `jarvis-watch` daemon |
| Create | `internal/watch/slack.go` | Slack poller: fetches DMs and mentions |
| Create | `internal/watch/slack_test.go` | Unit tests for Slack message processing |
| Create | `internal/watch/registry.go` | Context registry: maps Slack thread → session ID |
| Create | `internal/watch/registry_test.go` | Unit tests for context registry |
| Create | `internal/watch/daemon.go` | Daemon lifecycle: polling loop, session creation, folder management |
| Create | `internal/watch/daemon_test.go` | Unit tests for daemon logic |
| Modify | `internal/config/config.go` | Add `Watchers` config section |
| Modify | `go.mod` | Add `github.com/slack-go/slack` dependency |

---

### Task 1: Add Watcher Config

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Step 1: Write the failing test**

Create `internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadWatcherConfig(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	configYAML := []byte(`watchers:
  slack:
    enabled: true
    token: "xoxb-test-token"
    poll_interval: 30
    folder: "Slack"
    user_id: "U12345"
`)
	os.MkdirAll(tmp, 0o755)
	os.WriteFile(filepath.Join(tmp, "config.yaml"), configYAML, 0o644)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.Watchers.Slack.Token != "xoxb-test-token" {
		t.Errorf("token: got %q", cfg.Watchers.Slack.Token)
	}
	if cfg.Watchers.Slack.PollInterval != 30 {
		t.Errorf("poll_interval: got %d", cfg.Watchers.Slack.PollInterval)
	}
	if cfg.Watchers.Slack.Folder != "Slack" {
		t.Errorf("folder: got %q", cfg.Watchers.Slack.Folder)
	}
	if cfg.Watchers.Slack.UserID != "U12345" {
		t.Errorf("user_id: got %q", cfg.Watchers.Slack.UserID)
	}
	if !cfg.Watchers.Slack.Enabled {
		t.Error("enabled should be true")
	}
}

func TestLoadConfigNoWatchers(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	configYAML := []byte(`policies:
  auto_approve: []
`)
	os.WriteFile(filepath.Join(tmp, "config.yaml"), configYAML, 0o644)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.Watchers.Slack.Enabled {
		t.Error("slack should not be enabled by default")
	}
	if cfg.Watchers.Slack.PollInterval != 0 {
		t.Errorf("poll_interval should be 0, got %d", cfg.Watchers.Slack.PollInterval)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/jianyu.zhou/jarvis && go test ./internal/config/ -run TestLoadWatcher -v`
Expected: FAIL — `Watchers` field undefined

- [ ] **Step 3: Add watcher config types to `internal/config/config.go`**

Add these types and the `Watchers` field to the existing `Config` struct:

```go
type SlackWatcherConfig struct {
	Enabled      bool   `yaml:"enabled"`
	Token        string `yaml:"token"`
	PollInterval int    `yaml:"poll_interval"` // seconds
	Folder       string `yaml:"folder"`        // folder name to place sessions in
	UserID       string `yaml:"user_id"`       // your Slack user ID (for detecting @mentions)
}

type WatchersConfig struct {
	Slack SlackWatcherConfig `yaml:"slack"`
}
```

Add to the existing `Config` struct:
```go
type Config struct {
	WorktreeBaseDir string         `yaml:"worktree_base_dir,omitempty"`
	Policies        Policies       `yaml:"policies,omitempty"`
	Watchers        WatchersConfig `yaml:"watchers,omitempty"`
	repoPath        string
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/jianyu.zhou/jarvis && go test ./internal/config/ -run TestLoadWatcher -v`
Expected: PASS

- [ ] **Step 5: Run all tests**

Run: `cd /home/jianyu.zhou/jarvis && go test ./internal/config/ -v`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "Add watcher config types for Slack integration"
```

---

### Task 2: Context Registry

**Files:**
- Create: `internal/watch/registry.go`
- Create: `internal/watch/registry_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/watch/registry_test.go
package watch

import (
	"os"
	"testing"
)

func TestRegistryRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	reg := NewRegistry()

	// Register a context
	reg.Register("slack:C123/p456", "sess-abc")

	// Look it up
	sessID, found := reg.Lookup("slack:C123/p456")
	if !found {
		t.Fatal("should find registered context")
	}
	if sessID != "sess-abc" {
		t.Errorf("session ID: got %q, want %q", sessID, "sess-abc")
	}

	// Save and reload
	if err := reg.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	reg2 := NewRegistry()
	if err := reg2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	sessID2, found2 := reg2.Lookup("slack:C123/p456")
	if !found2 {
		t.Fatal("should find after reload")
	}
	if sessID2 != "sess-abc" {
		t.Errorf("after reload: got %q", sessID2)
	}
}

func TestRegistryLookupMiss(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	reg := NewRegistry()
	_, found := reg.Lookup("slack:nonexistent")
	if found {
		t.Error("should not find unregistered context")
	}
}

func TestRegistryLoadEmpty(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	reg := NewRegistry()
	// Load when file doesn't exist — should succeed with empty registry
	if err := reg.Load(); err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if len(reg.entries) != 0 {
		t.Errorf("should be empty, got %d entries", len(reg.entries))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/jianyu.zhou/jarvis && go test ./internal/watch/ -run TestRegistry -v`
Expected: FAIL — package doesn't exist

- [ ] **Step 3: Implement the registry**

```go
// internal/watch/registry.go
package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"jarvis/internal/store"

	"gopkg.in/yaml.v3"
)

// Registry maps external context keys (e.g. "slack:C123/p456") to session IDs.
// It persists to ~/.jarvis/context_registry.yaml for recovery across restarts.
type Registry struct {
	mu      sync.Mutex
	entries map[string]string // context key → session ID
}

func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]string)}
}

func registryPath() string {
	return filepath.Join(store.JarvisHome(), "context_registry.yaml")
}

func (r *Registry) Register(contextKey, sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[contextKey] = sessionID
}

func (r *Registry) Lookup(contextKey string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.entries[contextKey]
	return id, ok
}

type registryFile struct {
	Contexts map[string]string `yaml:"contexts"`
}

func (r *Registry) Save() error {
	r.mu.Lock()
	data, err := yaml.Marshal(registryFile{Contexts: r.entries})
	r.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}
	return store.WriteAtomic(registryPath(), data)
}

func (r *Registry) Load() error {
	data, err := os.ReadFile(registryPath())
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
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/jianyu.zhou/jarvis && go test ./internal/watch/ -run TestRegistry -v`
Expected: all 3 PASS

- [ ] **Step 5: Commit**

```bash
git add internal/watch/registry.go internal/watch/registry_test.go
git commit -m "Add context registry for deduplicating watcher-created sessions"
```

---

### Task 3: Slack Poller

**Files:**
- Create: `internal/watch/slack.go`
- Create: `internal/watch/slack_test.go`
- Modify: `go.mod` (add slack-go dependency)

- [ ] **Step 1: Add slack-go dependency**

Run: `cd /home/jianyu.zhou/jarvis && go get github.com/slack-go/slack`

- [ ] **Step 2: Write the test for message processing logic**

The Slack poller has two parts: (a) calling the Slack API (hard to unit test without mocks), and (b) processing messages into actionable events (easy to test). We test part (b).

```go
// internal/watch/slack_test.go
package watch

import (
	"testing"
	"time"
)

func TestSlackEventKey(t *testing.T) {
	ev := SlackEvent{
		ChannelID:   "C123",
		ThreadTS:    "1234567890.123456",
		Text:        "hey can you check the kafka lag?",
		SenderName:  "alice",
		ChannelName: "#oncall",
	}

	key := ev.ContextKey()
	if key != "slack:C123/1234567890.123456" {
		t.Errorf("context key: got %q", key)
	}
}

func TestSlackEventKeyDM(t *testing.T) {
	ev := SlackEvent{
		ChannelID:  "D456",
		ThreadTS:   "",
		MessageTS:  "1234567890.000001",
		Text:       "hello",
		SenderName: "bob",
		IsDM:       true,
	}

	key := ev.ContextKey()
	// DMs without a thread use message timestamp
	if key != "slack:D456/1234567890.000001" {
		t.Errorf("context key: got %q", key)
	}
}

func TestSlackEventSessionName(t *testing.T) {
	ev := SlackEvent{
		SenderName:  "alice",
		ChannelName: "#oncall",
		IsDM:        false,
	}
	name := ev.SessionName()
	if name != "slack: alice in #oncall" {
		t.Errorf("session name: got %q", name)
	}

	dm := SlackEvent{
		SenderName: "bob",
		IsDM:       true,
	}
	dmName := dm.SessionName()
	if dmName != "slack: DM from bob" {
		t.Errorf("DM session name: got %q", dmName)
	}
}

func TestSlackEventSystemPrompt(t *testing.T) {
	ev := SlackEvent{
		SenderName:  "alice",
		ChannelName: "#oncall",
		Text:        "can you check the kafka lag?",
		Timestamp:   time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC),
	}

	prompt := ev.SystemPrompt()
	if prompt == "" {
		t.Fatal("prompt should not be empty")
	}
	if !contains(prompt, "alice") {
		t.Error("prompt should mention sender")
	}
	if !contains(prompt, "#oncall") {
		t.Error("prompt should mention channel")
	}
	if !contains(prompt, "kafka lag") {
		t.Error("prompt should include message text")
	}
	if !contains(prompt, "Do NOT") {
		t.Error("prompt should include safety guardrail")
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && containsStr(s, substr)
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd /home/jianyu.zhou/jarvis && go test ./internal/watch/ -run TestSlackEvent -v`
Expected: FAIL — `SlackEvent` undefined

- [ ] **Step 4: Implement SlackEvent and the Slack poller**

```go
// internal/watch/slack.go
package watch

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"jarvis/internal/config"

	"github.com/slack-go/slack"
)

// SlackEvent represents an actionable Slack message.
type SlackEvent struct {
	ChannelID   string
	ChannelName string
	ThreadTS    string // thread parent timestamp (empty if top-level)
	MessageTS   string // this message's timestamp
	Text        string
	SenderID    string
	SenderName  string
	IsDM        bool
	Timestamp   time.Time
}

// ContextKey returns a unique key for deduplication in the context registry.
func (e SlackEvent) ContextKey() string {
	ts := e.ThreadTS
	if ts == "" {
		ts = e.MessageTS
	}
	return fmt.Sprintf("slack:%s/%s", e.ChannelID, ts)
}

// SessionName returns a human-readable session name for the dashboard.
func (e SlackEvent) SessionName() string {
	if e.IsDM {
		return fmt.Sprintf("slack: DM from %s", e.SenderName)
	}
	return fmt.Sprintf("slack: %s in %s", e.SenderName, e.ChannelName)
}

// SystemPrompt builds the instruction for the Claude Code session.
func (e SlackEvent) SystemPrompt() string {
	var b strings.Builder
	b.WriteString("You received a Slack message that needs your attention.\n\n")

	if e.IsDM {
		b.WriteString(fmt.Sprintf("**From:** %s (DM)\n", e.SenderName))
	} else {
		b.WriteString(fmt.Sprintf("**From:** %s in %s\n", e.SenderName, e.ChannelName))
	}
	b.WriteString(fmt.Sprintf("**Time:** %s\n", e.Timestamp.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("**Message:**\n> %s\n\n", e.Text))

	b.WriteString("Your job:\n")
	b.WriteString("1. Analyze the message and understand what is being asked\n")
	b.WriteString("2. Investigate if needed (read code, check logs, etc.)\n")
	b.WriteString("3. Prepare a draft response\n\n")
	b.WriteString("**IMPORTANT:** Do NOT send any Slack messages, post any comments, ")
	b.WriteString("or take any external-facing actions. Only investigate and prepare a draft.\n")

	return b.String()
}

// SlackPoller polls the Slack API for new messages directed at the user.
type SlackPoller struct {
	client *slack.Client
	userID string
	lastTS map[string]string // channel ID → last seen message timestamp
}

// NewSlackPoller creates a poller from watcher config.
func NewSlackPoller(cfg config.SlackWatcherConfig) *SlackPoller {
	return &SlackPoller{
		client: slack.New(cfg.Token),
		userID: cfg.UserID,
		lastTS: make(map[string]string),
	}
}

// Poll checks for new DMs and mentions since last poll. Returns actionable events.
func (p *SlackPoller) Poll(ctx context.Context) ([]SlackEvent, error) {
	var events []SlackEvent

	// 1. Check DMs (IM channels)
	dmEvents, err := p.pollDMs(ctx)
	if err != nil {
		log.Printf("slack: DM poll error: %v", err)
	} else {
		events = append(events, dmEvents...)
	}

	// 2. Check mentions via search
	mentionEvents, err := p.pollMentions(ctx)
	if err != nil {
		log.Printf("slack: mention poll error: %v", err)
	} else {
		events = append(events, mentionEvents...)
	}

	return events, nil
}

func (p *SlackPoller) pollDMs(ctx context.Context) ([]SlackEvent, error) {
	// List IM (direct message) channels
	params := &slack.GetConversationsParameters{
		Types:           []string{"im"},
		Limit:           100,
		ExcludeArchived: true,
	}
	channels, _, err := p.client.GetConversationsContext(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list DM channels: %w", err)
	}

	var events []SlackEvent
	for _, ch := range channels {
		lastSeen := p.lastTS[ch.ID]
		histParams := &slack.GetConversationHistoryParameters{
			ChannelID: ch.ID,
			Limit:     10,
			Oldest:    lastSeen,
		}
		hist, err := p.client.GetConversationHistoryContext(ctx, histParams)
		if err != nil {
			log.Printf("slack: DM history %s: %v", ch.ID, err)
			continue
		}

		for _, msg := range hist.Messages {
			// Skip own messages
			if msg.User == p.userID {
				continue
			}
			// Skip messages we've already seen
			if msg.Timestamp <= lastSeen {
				continue
			}

			userName := p.resolveUserName(ctx, msg.User)
			events = append(events, SlackEvent{
				ChannelID:  ch.ID,
				MessageTS:  msg.Timestamp,
				ThreadTS:   msg.ThreadTimestamp,
				Text:       msg.Text,
				SenderID:   msg.User,
				SenderName: userName,
				IsDM:       true,
				Timestamp:  parseSlackTS(msg.Timestamp),
			})
		}

		// Update last seen
		if len(hist.Messages) > 0 {
			newest := hist.Messages[0].Timestamp
			for _, m := range hist.Messages {
				if m.Timestamp > newest {
					newest = m.Timestamp
				}
			}
			p.lastTS[ch.ID] = newest
		}
	}

	return events, nil
}

func (p *SlackPoller) pollMentions(ctx context.Context) ([]SlackEvent, error) {
	// Search for recent messages mentioning the user
	query := fmt.Sprintf("<@%s>", p.userID)
	params := slack.SearchParameters{
		Sort:          "timestamp",
		SortDirection: "desc",
		Count:         20,
	}
	results, err := p.client.SearchMessagesContext(ctx, query, params)
	if err != nil {
		return nil, fmt.Errorf("search mentions: %w", err)
	}

	var events []SlackEvent
	for _, match := range results.Matches {
		// Skip DMs (handled separately)
		if match.Channel.IsIM {
			continue
		}
		// Skip own messages
		if match.User == p.userID {
			continue
		}

		lastSeen := p.lastTS["mentions:"+match.Channel.ID]
		if match.Timestamp <= lastSeen {
			continue
		}

		events = append(events, SlackEvent{
			ChannelID:   match.Channel.ID,
			ChannelName: "#" + match.Channel.Name,
			MessageTS:   match.Timestamp,
			ThreadTS:    match.Previous.Timestamp, // parent thread if exists
			Text:        match.Text,
			SenderID:    match.User,
			SenderName:  match.Username,
			IsDM:        false,
			Timestamp:   parseSlackTS(match.Timestamp),
		})

		if match.Timestamp > lastSeen {
			p.lastTS["mentions:"+match.Channel.ID] = match.Timestamp
		}
	}

	return events, nil
}

func (p *SlackPoller) resolveUserName(ctx context.Context, userID string) string {
	user, err := p.client.GetUserInfoContext(ctx, userID)
	if err != nil {
		return userID
	}
	if user.Profile.DisplayName != "" {
		return user.Profile.DisplayName
	}
	return user.RealName
}

func parseSlackTS(ts string) time.Time {
	// Slack timestamps are "1234567890.123456" (Unix seconds.microseconds)
	parts := strings.SplitN(ts, ".", 2)
	if len(parts) == 0 {
		return time.Time{}
	}
	var sec int64
	for _, c := range parts[0] {
		sec = sec*10 + int64(c-'0')
	}
	return time.Unix(sec, 0)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /home/jianyu.zhou/jarvis && go test ./internal/watch/ -run TestSlackEvent -v`
Expected: all 4 PASS

- [ ] **Step 6: Commit**

```bash
git add internal/watch/slack.go internal/watch/slack_test.go go.mod go.sum
git commit -m "Add Slack poller with event types and message processing"
```

---

### Task 4: Watcher Daemon

**Files:**
- Create: `internal/watch/daemon.go`
- Create: `internal/watch/daemon_test.go`

- [ ] **Step 1: Write test for folder-or-create logic**

```go
// internal/watch/daemon_test.go
package watch

import (
	"os"
	"testing"

	"jarvis/internal/model"
	"jarvis/internal/store"
)

func TestEnsureFolderCreatesNew(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	d := &Daemon{}
	folderID, err := d.ensureFolder("Slack")
	if err != nil {
		t.Fatalf("ensureFolder: %v", err)
	}
	if folderID == "" {
		t.Fatal("folder ID should not be empty")
	}

	// Verify folder exists on disk
	f, err := store.GetFolder(folderID)
	if err != nil {
		t.Fatalf("get folder: %v", err)
	}
	if f.Name != "Slack" {
		t.Errorf("folder name: got %q", f.Name)
	}
}

func TestEnsureFolderReusesExisting(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	// Pre-create a folder
	existing := &model.Folder{
		ID:     model.NewID(),
		Type:   "folder",
		Name:   "Slack",
		Status: "active",
	}
	store.SaveFolder(existing)

	d := &Daemon{}
	folderID, err := d.ensureFolder("Slack")
	if err != nil {
		t.Fatalf("ensureFolder: %v", err)
	}
	if folderID != existing.ID {
		t.Errorf("should reuse existing folder, got %q want %q", folderID, existing.ID)
	}
}

func TestPlaceSessionInFolder(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	// Create folder
	folder := &model.Folder{
		ID:     model.NewID(),
		Type:   "folder",
		Name:   "Slack",
		Status: "active",
	}
	store.SaveFolder(folder)

	// Create session
	sess := &model.Session{
		ID:     model.NewID(),
		Type:   "session",
		Name:   "test",
		Status: model.StatusActive,
	}
	store.SaveSession(sess)

	d := &Daemon{}
	d.placeSessionInFolder(sess.ID, folder.ID)

	// Verify
	updated, _ := store.GetFolder(folder.ID)
	if len(updated.Children) != 1 {
		t.Fatalf("children: got %d, want 1", len(updated.Children))
	}
	if updated.Children[0].ID != sess.ID {
		t.Errorf("child ID: got %q", updated.Children[0].ID)
	}

	updatedSess, _ := store.GetSession(sess.ID)
	if updatedSess.ParentID != folder.ID {
		t.Errorf("parent ID: got %q", updatedSess.ParentID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/jianyu.zhou/jarvis && go test ./internal/watch/ -run TestEnsureFolder -v`
Expected: FAIL — `Daemon` type undefined

- [ ] **Step 3: Implement the daemon**

```go
// internal/watch/daemon.go
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
	if cfg.Watchers.Slack.Token == "" {
		return nil, fmt.Errorf("slack watcher token not configured")
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

	// Ensure target folder exists
	if slackCfg.Folder != "" {
		id, err := d.ensureFolder(slackCfg.Folder)
		if err != nil {
			return fmt.Errorf("ensure folder: %w", err)
		}
		d.folderID = id
		log.Printf("watch: sessions will be placed in folder %q (%s)", slackCfg.Folder, id)
	}

	log.Printf("watch: polling Slack every %s", interval)

	// Initial poll
	d.pollOnce(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("watch: shutting down")
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

		// Skip if we already have a session for this context
		if _, found := d.registry.Lookup(key); found {
			continue
		}

		log.Printf("watch: new event: %s — %s", ev.SessionName(), truncate(ev.Text, 60))

		// Create session
		cwd := d.cfg.RepoPath()
		if cwd == "" {
			cwd = "."
		}
		claudeArgs := []string{
			"claude",
			"--append-system-prompt", ev.SystemPrompt(),
		}

		sess, err := d.mgr.Spawn(ev.SessionName(), cwd, claudeArgs)
		if err != nil {
			log.Printf("watch: spawn failed for %s: %v", key, err)
			continue
		}

		// Place in folder
		if d.folderID != "" {
			d.placeSessionInFolder(sess.ID, d.folderID)
		}

		// Register context
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

	// Create new folder
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/jianyu.zhou/jarvis && go test ./internal/watch/ -run "TestEnsureFolder|TestPlaceSession" -v`
Expected: all 3 PASS

- [ ] **Step 5: Run all watch tests**

Run: `cd /home/jianyu.zhou/jarvis && go test ./internal/watch/ -v`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/watch/daemon.go internal/watch/daemon_test.go
git commit -m "Add watcher daemon with folder management and session creation"
```

---

### Task 5: Watcher Binary

**Files:**
- Create: `cmd/watch/main.go`

- [ ] **Step 1: Create the binary entry point**

```go
// cmd/watch/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"jarvis/internal/config"
	"jarvis/internal/watch"
)

func main() {
	log.SetPrefix("jarvis-watch: ")
	log.SetFlags(log.Ldate | log.Ltime)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
		os.Exit(1)
	}

	daemon, err := watch.NewDaemon(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Fprintln(os.Stderr, "\nConfigure the Slack watcher in ~/.jarvis/config.yaml:")
		fmt.Fprintln(os.Stderr, "  watchers:")
		fmt.Fprintln(os.Stderr, "    slack:")
		fmt.Fprintln(os.Stderr, "      enabled: true")
		fmt.Fprintln(os.Stderr, "      token: \"xoxb-your-bot-token\"")
		fmt.Fprintln(os.Stderr, "      user_id: \"U12345\"")
		fmt.Fprintln(os.Stderr, "      poll_interval: 30")
		fmt.Fprintln(os.Stderr, "      folder: \"Slack\"")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Graceful shutdown on SIGINT/SIGTERM
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		log.Println("received shutdown signal")
		cancel()
	}()

	log.Println("starting Slack watcher")
	if err := daemon.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/jianyu.zhou/jarvis && go build -o jarvis-watch ./cmd/watch/`
Expected: no errors, binary created at `./jarvis-watch`

- [ ] **Step 3: Verify it prints help when not configured**

Run: `cd /home/jianyu.zhou/jarvis && JARVIS_HOME=/tmp/jarvis-watch-test ./jarvis-watch 2>&1; rm -rf /tmp/jarvis-watch-test`
Expected: error message with config instructions

- [ ] **Step 4: Commit**

```bash
git add cmd/watch/main.go
git commit -m "Add jarvis-watch binary entry point"
```

---

### Task 6: Full Integration Test

- [ ] **Step 1: Run all tests**

Run: `cd /home/jianyu.zhou/jarvis && go test ./...`
Expected: all PASS

- [ ] **Step 2: Build both binaries**

Run: `cd /home/jianyu.zhou/jarvis && go build -o jarvis ./cmd/jarvis/ && go build -o jarvis-watch ./cmd/watch/`
Expected: both binaries build successfully

- [ ] **Step 3: Clean up test binaries**

Run: `rm -f jarvis jarvis-watch`

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "Slack watcher: complete Phase 4 implementation"
```
