# Auto-Rename Untitled Sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** On TUI startup, automatically rename `(untitled chat)` sessions in the background using each session's full Claude conversation context, via headless `claude -p --resume --fork-session`.

**Architecture:** A new `internal/autorename` package finds untitled candidates, generates a title per session with a one-shot headless claude call (full context via `--resume`, original transcript untouched via `--fork-session`), and persists it through a new shared `store.RenameSession` helper. The TUI starts the scan as a background goroutine and refreshes the list per rename via the Bubble Tea channel-subscription pattern.

**Tech Stack:** Go, Bubble Tea (existing TUI), `claude` CLI (headless print mode), existing packages `internal/store`, `internal/session`, `internal/searchindex`.

**Spec:** `docs/superpowers/specs/2026-07-12-auto-rename-untitled-design.md`

**Deviation from spec (approved rationale):** the spec's fallback to `FindLatestSession(LaunchDir)` when `ClaudeSessionID` is empty is unsafe — all `jarvis chat` sessions share the same LaunchDir, so "latest JSONL in that project dir" can belong to a *different* session and produce a wrong title. Instead, sessions with empty `ClaudeSessionID` are skipped (it is normally set by the hook relay; such sessions are retried on the next startup once it is set).

**Working directory:** `.worktrees/auto-rename-untitled` (branch `feat/auto-rename-untitled`). All commands below run from this worktree root.

---

### Task 1: Shared `store.RenameSession` helper

The rename store-write currently exists twice (`cmd/jarvis/main.go` renameCmd, `internal/tui/commands.go` renameSession). Extract it; autorename will be the third caller.

**Files:**
- Modify: `internal/store/session.go`
- Create: `internal/store/session_test.go`
- Modify: `internal/tui/commands.go:103-114`
- Modify: `cmd/jarvis/main.go:288-317`

- [ ] **Step 1: Write the failing test**

Create `internal/store/session_test.go`:

```go
package store

import (
	"testing"
	"time"

	"jarvis/internal/model"
)

func TestRenameSession(t *testing.T) {
	t.Setenv("JARVIS_HOME", t.TempDir())

	before := time.Now().Add(-time.Hour)
	sess := &model.Session{
		ID:        "sess-1",
		Name:      "(untitled chat)",
		Status:    model.StatusActive,
		UpdatedAt: before,
	}
	if err := SaveSession(sess); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	renamed, err := RenameSession("sess-1", "Fix Login Bug")
	if err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	if renamed.Name != "Fix Login Bug" {
		t.Errorf("returned name = %q, want %q", renamed.Name, "Fix Login Bug")
	}

	got, err := GetSession("sess-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Name != "Fix Login Bug" {
		t.Errorf("persisted name = %q, want %q", got.Name, "Fix Login Bug")
	}
	if !got.UpdatedAt.After(before) {
		t.Errorf("UpdatedAt not bumped: %v", got.UpdatedAt)
	}
}

func TestRenameSessionNotFound(t *testing.T) {
	t.Setenv("JARVIS_HOME", t.TempDir())

	if _, err := RenameSession("nope", "X"); err == nil {
		t.Fatal("expected error for missing session, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestRenameSession -v`
Expected: FAIL — `undefined: RenameSession` (compile error).

- [ ] **Step 3: Write minimal implementation**

In `internal/store/session.go`, add `"time"` to imports and append:

```go
// RenameSession updates a session's display name and bumps UpdatedAt.
func RenameSession(id, name string) (*model.Session, error) {
	s, err := GetSession(id)
	if err != nil {
		return nil, err
	}
	s.Name = name
	s.UpdatedAt = time.Now()
	if err := SaveSession(s); err != nil {
		return nil, err
	}
	return s, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestRenameSession -v`
Expected: PASS (both tests).

- [ ] **Step 5: Switch the two existing call sites to the helper**

In `internal/tui/commands.go`, replace the body of `renameSession` (lines 103-114):

```go
// renameSession changes a session's display name.
func (d Dashboard) renameSession(sessionID, name string) tea.Cmd {
	return func() tea.Msg {
		_, _ = store.RenameSession(sessionID, name)
		return refreshMsg{items: buildItemList(d.mgr)}
	}
}
```

(If `time` becomes unused in commands.go after this, remove it from imports — check with `go build`.)

In `cmd/jarvis/main.go`, replace the `RunE` body of `renameCmd` (lines 292-316):

```go
		RunE: func(cmd *cobra.Command, args []string) error {
			newName := strings.Join(args, " ")

			sessionID, _ := cmd.Flags().GetString("session-id")
			if sessionID == "" {
				sessionID = os.Getenv("JARVIS_SESSION_ID")
			}
			if sessionID == "" {
				return fmt.Errorf("no session ID: set JARVIS_SESSION_ID or pass --session-id")
			}

			sess, err := store.GetSession(sessionID)
			if err != nil {
				return fmt.Errorf("session %q not found", sessionID)
			}
			oldName := sess.Name

			if _, err := store.RenameSession(sessionID, newName); err != nil {
				return err
			}
			fmt.Printf("Renamed %q → %q\n", oldName, newName)
			return nil
		},
```

(If `time` becomes unused in main.go, remove the import — `initCmd` still uses it, so it likely stays.)

- [ ] **Step 6: Build and run full test suite**

Run: `go build ./... && go test ./internal/store/ ./internal/tui/ -count=1`
Expected: build OK, tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/store/session.go internal/store/session_test.go internal/tui/commands.go cmd/jarvis/main.go
git commit -m "refactor(store): extract shared RenameSession helper

Co-authored-by: Isaac"
```

---

### Task 2: `internal/autorename` — candidate filtering

Pure filtering logic: which sessions are eligible for auto-rename.

**Files:**
- Create: `internal/autorename/autorename.go`
- Create: `internal/autorename/autorename_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/autorename/autorename_test.go`:

```go
package autorename

import (
	"testing"

	"jarvis/internal/model"
)

func TestFindCandidates(t *testing.T) {
	sessions := []*model.Session{
		{ID: "a", Name: "(untitled chat)", Status: model.StatusActive, ClaudeSessionID: "csid-a"},
		{ID: "b", Name: "(untitled chat)", Status: model.StatusSuspended, ClaudeSessionID: "csid-b"},
		{ID: "c", Name: "Real Name", Status: model.StatusActive, ClaudeSessionID: "csid-c"},          // named
		{ID: "d", Name: "(untitled chat)", Status: model.StatusDone, ClaudeSessionID: "csid-d"},      // done
		{ID: "e", Name: "(untitled chat)", Status: model.StatusArchived, ClaudeSessionID: "csid-e"},  // archived
		{ID: "f", Name: "(untitled chat)", Status: model.StatusActive, ClaudeSessionID: ""},          // no claude session yet
	}

	got := FindCandidates(sessions)

	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	want := []string{"a", "b"}
	if len(ids) != len(want) {
		t.Fatalf("candidates = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", ids, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/autorename/ -v`
Expected: FAIL — package/function undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/autorename/autorename.go`:

```go
// Package autorename gives untitled sessions a real name at TUI startup.
// It infers each title from the session's full Claude conversation via a
// headless `claude -p --resume --fork-session` call, so the running session
// is never attached to and its transcript is never modified.
package autorename

import (
	"jarvis/internal/model"
)

// UntitledName is the placeholder name given to `jarvis chat` sessions.
const UntitledName = "(untitled chat)"

// FindCandidates returns the sessions eligible for auto-rename: still
// untitled, not finished, and with a known Claude session to read context
// from. Sessions without a ClaudeSessionID are skipped (not guessed at):
// untitled chats share a LaunchDir, so "latest JSONL in the project dir"
// could belong to a different session.
func FindCandidates(sessions []*model.Session) []*model.Session {
	var out []*model.Session
	for _, s := range sessions {
		if s.Name != UntitledName {
			continue
		}
		if s.Status != model.StatusActive && s.Status != model.StatusSuspended {
			continue
		}
		if s.ClaudeSessionID == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/autorename/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/autorename/
git commit -m "feat(autorename): candidate filtering for untitled sessions

Co-authored-by: Isaac"
```

---

### Task 3: Title sanitization and claude output parsing

Pure functions: clean the model's title text; parse `claude -p --output-format json` stdout.

**Files:**
- Create: `internal/autorename/title.go`
- Create: `internal/autorename/title_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/autorename/title_test.go`:

```go
package autorename

import "testing"

func TestSanitizeTitle(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Fix Login Bug", "Fix Login Bug"},
		{"  Fix Login Bug  \n", "Fix Login Bug"},
		{"\"Fix Login Bug\"", "Fix Login Bug"},
		{"'Fix   Login\tBug'", "Fix Login Bug"},
		{"Fix Login Bug\nExtra explanation line", "Fix Login Bug"},
		{"", ""},
		{"   \n  ", ""},
		// 70 x's: capped at 60 runes
		{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
			"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"[:60]},
	}
	for _, c := range cases {
		if got := SanitizeTitle(c.in); got != c.want {
			t.Errorf("SanitizeTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseClaudeOutput(t *testing.T) {
	out := []byte(`{"type":"result","subtype":"success","is_error":false,"result":"Fix Login Bug","session_id":"fork-123"}`)
	title, forkID, err := parseClaudeOutput(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if title != "Fix Login Bug" {
		t.Errorf("title = %q", title)
	}
	if forkID != "fork-123" {
		t.Errorf("forkID = %q", forkID)
	}
}

func TestParseClaudeOutputError(t *testing.T) {
	cases := [][]byte{
		[]byte(`not json`),
		[]byte(`{"is_error":true,"result":"something failed","session_id":"fork-1"}`),
		[]byte(`{"is_error":false,"result":"","session_id":"fork-2"}`), // empty title
	}
	for _, out := range cases {
		if _, _, err := parseClaudeOutput(out); err == nil {
			t.Errorf("parseClaudeOutput(%s): expected error, got nil", out)
		}
	}
}

func TestParseClaudeOutputErrorStillReturnsForkID(t *testing.T) {
	out := []byte(`{"is_error":true,"result":"boom","session_id":"fork-9"}`)
	_, forkID, err := parseClaudeOutput(out)
	if err == nil {
		t.Fatal("expected error")
	}
	if forkID != "fork-9" {
		t.Errorf("forkID = %q, want fork-9 (needed for cleanup even on error)", forkID)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/autorename/ -run 'TestSanitizeTitle|TestParseClaudeOutput' -v`
Expected: FAIL — undefined functions.

- [ ] **Step 3: Write minimal implementation**

Create `internal/autorename/title.go`:

```go
package autorename

import (
	"encoding/json"
	"fmt"
	"strings"
)

const maxTitleRunes = 60

// SanitizeTitle normalizes a model-produced title: first line only,
// surrounding quotes stripped, whitespace collapsed, length capped.
// Returns "" if nothing usable remains.
func SanitizeTitle(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.Trim(s, "\"'`“”‘’")
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxTitleRunes {
		s = strings.TrimSpace(string(r[:maxTitleRunes]))
	}
	return s
}

// claudeResult is the subset of `claude -p --output-format json` we consume.
type claudeResult struct {
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	IsError   bool   `json:"is_error"`
}

// parseClaudeOutput extracts the sanitized title and the forked session ID
// from headless claude stdout. The fork ID is returned even on error so the
// caller can always clean up the temporary forked JSONL.
func parseClaudeOutput(out []byte) (title, forkSessionID string, err error) {
	var r claudeResult
	if err := json.Unmarshal(out, &r); err != nil {
		return "", "", fmt.Errorf("parse claude output: %w", err)
	}
	if r.IsError {
		return "", r.SessionID, fmt.Errorf("claude returned is_error")
	}
	title = SanitizeTitle(r.Result)
	if title == "" {
		return "", r.SessionID, fmt.Errorf("empty title after sanitization")
	}
	return title, r.SessionID, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/autorename/ -v`
Expected: PASS (all).

- [ ] **Step 5: Commit**

```bash
git add internal/autorename/title.go internal/autorename/title_test.go
git commit -m "feat(autorename): title sanitization and claude JSON output parsing

Co-authored-by: Isaac"
```

---

### Task 4: Claude title generator (headless exec)

The impure edge: shells out to `claude`. Kept thin — all logic it wraps is already unit-tested in Task 3; this task is verified manually in Task 7.

**Files:**
- Create: `internal/autorename/generator.go`

- [ ] **Step 1: Write the implementation**

Create `internal/autorename/generator.go`:

```go
package autorename

import (
	"context"
	"os"
	"os/exec"
	"time"

	"jarvis/internal/model"
	"jarvis/internal/session"
)

// titlePrompt asks for the title and nothing else; the full conversation
// context comes from --resume, not from this prompt.
const titlePrompt = "Based on the entire conversation so far, output ONLY a 3-8 word title-case task name for this session. No explanation, no quotes, no trailing punctuation."

// Generator produces a display name for a session from its conversation.
type Generator interface {
	Title(sess *model.Session) (string, error)
}

// ClaudeGenerator infers a title by resuming the session's full context in
// a one-shot headless claude call. --fork-session keeps the rename exchange
// out of the original transcript; the forked JSONL is deleted afterwards.
// The headless call is granted no tools — it can only print text.
type ClaudeGenerator struct {
	Timeout time.Duration
}

func (g ClaudeGenerator) Title(sess *model.Session) (string, error) {
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude", "-p",
		"--resume", sess.ClaudeSessionID,
		"--fork-session",
		"--output-format", "json",
		titlePrompt)
	cmd.Dir = sess.LaunchDir

	out, err := cmd.Output()
	if err != nil {
		return "", err
	}

	title, forkID, perr := parseClaudeOutput(out)
	if forkID != "" && forkID != sess.ClaudeSessionID {
		// Best-effort cleanup of the temporary forked transcript.
		os.Remove(session.SessionJSONLPath(forkID, sess.LaunchDir))
	}
	return title, perr
}
```

- [ ] **Step 2: Build**

Run: `go build ./...`
Expected: OK.

- [ ] **Step 3: Commit**

```bash
git add internal/autorename/generator.go
git commit -m "feat(autorename): headless claude title generator

Co-authored-by: Isaac"
```

---

### Task 5: `Run` orchestration

Sequential scan-and-rename loop with a pluggable generator.

**Files:**
- Modify: `internal/autorename/autorename.go`
- Modify: `internal/autorename/autorename_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/autorename/autorename_test.go` (add imports `"os"`, `"path/filepath"`, and `"jarvis/internal/store"`, `"jarvis/internal/paths"`):

```go
type stubGen struct {
	title string
	err   error
	calls []string
}

func (s *stubGen) Title(sess *model.Session) (string, error) {
	s.calls = append(s.calls, sess.ID)
	return s.title, s.err
}

// writeTranscript creates a fake Claude JSONL under the (fake) HOME for the
// given launch dir + claude session id, with one real user message.
func writeTranscript(t *testing.T, home, launchDir, csid string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", paths.EncodeCWD(launchDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","message":{"role":"user","content":"please fix the login bug in the auth service"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, csid+".jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunRenamesUntitledSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("JARVIS_HOME", filepath.Join(home, ".jarvis"))
	launchDir := t.TempDir() // not a git repo, so ProjectDirs has one candidate

	mustSave := func(s *model.Session) {
		t.Helper()
		if err := store.SaveSession(s); err != nil {
			t.Fatal(err)
		}
	}

	// Candidate with a transcript: should be renamed.
	mustSave(&model.Session{ID: "a", Name: UntitledName, Status: model.StatusActive,
		LaunchDir: launchDir, ClaudeSessionID: "csid-a"})
	writeTranscript(t, home, launchDir, "csid-a")

	// Candidate whose transcript is missing: skipped, stays untitled.
	mustSave(&model.Session{ID: "b", Name: UntitledName, Status: model.StatusActive,
		LaunchDir: launchDir, ClaudeSessionID: "csid-missing"})

	// Already named: untouched.
	mustSave(&model.Session{ID: "c", Name: "Keep Me", Status: model.StatusActive,
		LaunchDir: launchDir, ClaudeSessionID: "csid-c"})

	gen := &stubGen{title: "Fix Login Bug"}
	var notified []string
	Run(gen, func(id, name string) { notified = append(notified, id+"="+name) })

	if len(gen.calls) != 1 || gen.calls[0] != "a" {
		t.Fatalf("generator calls = %v, want [a]", gen.calls)
	}
	a, _ := store.GetSession("a")
	if a.Name != "Fix Login Bug" {
		t.Errorf("session a name = %q, want %q", a.Name, "Fix Login Bug")
	}
	b, _ := store.GetSession("b")
	if b.Name != UntitledName {
		t.Errorf("session b name = %q, want untouched", b.Name)
	}
	c, _ := store.GetSession("c")
	if c.Name != "Keep Me" {
		t.Errorf("session c name = %q, want untouched", c.Name)
	}
	if len(notified) != 1 || notified[0] != "a=Fix Login Bug" {
		t.Errorf("notified = %v", notified)
	}
}

func TestRunSkipsOnGeneratorError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("JARVIS_HOME", filepath.Join(home, ".jarvis"))
	launchDir := t.TempDir()

	sess := &model.Session{ID: "a", Name: UntitledName, Status: model.StatusActive,
		LaunchDir: launchDir, ClaudeSessionID: "csid-a"}
	if err := store.SaveSession(sess); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, home, launchDir, "csid-a")

	gen := &stubGen{err: os.ErrDeadlineExceeded}
	notifyCalled := false
	Run(gen, func(id, name string) { notifyCalled = true })

	got, _ := store.GetSession("a")
	if got.Name != UntitledName {
		t.Errorf("name = %q, want untouched on generator error", got.Name)
	}
	if notifyCalled {
		t.Error("notify must not fire on failure")
	}
}
```

Note: `os.UserHomeDir()` reads `$HOME` on Linux, so `t.Setenv("HOME", ...)` redirects both `paths.ProjectDirs` and the store's default — and `JARVIS_HOME` pins the store explicitly.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/autorename/ -run TestRun -v`
Expected: FAIL — `undefined: Run`.

- [ ] **Step 3: Write minimal implementation**

In `internal/autorename/autorename.go`, extend imports to:

```go
import (
	"os"

	"jarvis/internal/model"
	"jarvis/internal/searchindex"
	"jarvis/internal/session"
	"jarvis/internal/store"
)
```

Append:

```go
// hasRealUserMessage reports whether the session's transcript contains at
// least one human-typed message (system reminders, hook output and other
// synthetic records don't count). A session with no real content yet can't
// be named meaningfully — it stays untitled until the next scan.
func hasRealUserMessage(sess *model.Session) bool {
	path := session.SessionJSONLPath(sess.ClaudeSessionID, sess.LaunchDir)
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	ps, _ := searchindex.ParseTranscript(f)
	return ps.InitialPrompt != ""
}

// Run scans for untitled sessions and renames each one whose transcript has
// real content. Sequential on purpose: one headless claude process at a time.
// Failures skip the session silently — this is a best-effort background
// enhancement and must never surface errors into the TUI.
// notify (optional) fires after each successful rename.
func Run(gen Generator, notify func(sessionID, newName string)) {
	sessions, err := store.ListSessions(&store.SessionFilter{
		StatusIn: []model.SessionStatus{model.StatusActive, model.StatusSuspended},
	})
	if err != nil {
		return
	}
	for _, sess := range FindCandidates(sessions) {
		if !hasRealUserMessage(sess) {
			continue
		}
		title, err := gen.Title(sess)
		if err != nil {
			continue
		}
		if _, err := store.RenameSession(sess.ID, title); err != nil {
			continue
		}
		if notify != nil {
			notify(sess.ID, title)
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/autorename/ -v -count=1`
Expected: PASS (all tests in the package).

- [ ] **Step 5: Commit**

```bash
git add internal/autorename/
git commit -m "feat(autorename): Run orchestration with transcript content gate

Co-authored-by: Isaac"
```

---

### Task 6: TUI wiring — start scan on dashboard init, refresh per rename

Bubble Tea channel-subscription pattern: one goroutine runs the scan and
pushes an event per rename; a re-armed wait command turns events into
`refreshItems` so titles appear live.

**Files:**
- Modify: `internal/tui/dashboard.go`

- [ ] **Step 1: Add the channel field and message type**

In `internal/tui/dashboard.go`, next to the existing message types (lines 46-49), add:

```go
type autoRenamedMsg struct{}          // one background auto-rename completed
```

In the `Dashboard` struct, add a field (near `pendingCursorID`):

```go
	// autoRenameEvents streams one event per background auto-rename so the
	// list refreshes as titles arrive. Closed when the scan finishes.
	autoRenameEvents chan struct{}
```

In `NewDashboard`'s returned struct literal, add:

```go
		autoRenameEvents: make(chan struct{}, 16),
```

- [ ] **Step 2: Add the two commands**

Add imports `"os/exec"` and `"jarvis/internal/autorename"` to dashboard.go, then append:

```go
// runAutoRename executes the whole background scan in one goroutine,
// emitting an event per successful rename. Skipped entirely when the
// claude CLI is unavailable.
func (d Dashboard) runAutoRename() tea.Cmd {
	ch := d.autoRenameEvents
	return func() tea.Msg {
		defer close(ch)
		if _, err := exec.LookPath("claude"); err != nil {
			return nil
		}
		autorename.Run(autorename.ClaudeGenerator{}, func(id, name string) {
			// Non-blocking: if the TUI already quit (e.g. user attached to a
			// session) nobody drains the channel; the scan must still finish
			// and persist renames. A dropped event only skips one interim
			// refresh — every event triggers a full rebuild anyway.
			select {
			case ch <- struct{}{}:
			default:
			}
		})
		return nil
	}
}

// waitAutoRename blocks for the next auto-rename event; Update re-arms it.
// Returns nil once the channel closes (scan finished).
func (d Dashboard) waitAutoRename() tea.Cmd {
	ch := d.autoRenameEvents
	return func() tea.Msg {
		if _, ok := <-ch; ok {
			return autoRenamedMsg{}
		}
		return nil
	}
}
```

- [ ] **Step 3: Wire into Init and Update**

Change `Init` (line 202-204) to:

```go
func (d Dashboard) Init() tea.Cmd {
	return tea.Batch(d.refreshItems(), d.syncIndex(), d.runAutoRename(), d.waitAutoRename())
}
```

In `Update`, add a case next to `indexSyncedMsg`:

```go
	case autoRenamedMsg:
		return d, tea.Batch(d.refreshItems(), d.waitAutoRename())
```

Also update the comment above `Init` (lines 199-201): the dashboard still
doesn't poll, but background auto-rename completions now trigger refreshes.

- [ ] **Step 4: Build and test**

Run: `go build ./... && go test ./... -count=1`
Expected: build OK, all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/dashboard.go
git commit -m "feat(tui): auto-rename untitled sessions in background on startup

Co-authored-by: Isaac"
```

---

### Task 7: Manual end-to-end verification

No code. Verifies the real `claude -p --resume --fork-session` behavior that unit tests mock.

- [ ] **Step 1: Build the binary**

Run from the worktree root: `make build` (or `go build -o bin/jarvis ./cmd/jarvis`)

- [ ] **Step 2: Create a throwaway untitled session with content**

```bash
./bin/jarvis chat
```

Type a couple of real prompts (e.g. "explain what the internal/paths package does"), let Claude answer, then detach with `Ctrl-\`.

- [ ] **Step 3: Record the session's transcript state**

```bash
./bin/jarvis ls   # note the untitled session's ID
# find its ClaudeSessionID and JSONL, note line count:
grep claude_session_id ~/.jarvis/sessions/<ID>/session.yaml
wc -l ~/.claude/projects/<encoded-launch-dir>/<claude-session-id>.jsonl
```

- [ ] **Step 4: Start the TUI and observe**

Run `./bin/jarvis`. Expected within ~a minute: the `(untitled chat)` entry changes to a descriptive 3-8 word title without any interaction.

- [ ] **Step 5: Verify no side effects**

```bash
# Original transcript unchanged (same line count as step 3):
wc -l ~/.claude/projects/<encoded-launch-dir>/<claude-session-id>.jsonl
# No leftover forked JSONL (no new *.jsonl newer than the original):
ls -t ~/.claude/projects/<encoded-launch-dir>/*.jsonl | head -3
```

Also attach to the session and confirm the conversation shows no rename exchange.

- [ ] **Step 6: Clean up the throwaway session**

```bash
./bin/jarvis rm <ID>
```

- [ ] **Step 7: Final commit if any fixes were needed, then push**

```bash
git push -u origin feat/auto-rename-untitled
```
