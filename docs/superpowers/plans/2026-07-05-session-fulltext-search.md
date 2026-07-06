# Session Full-Text Search (SQLite FTS5) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add full-text search over past Claude Code conversation content to the jarvis dashboard, so a session can be found by what was discussed in it, not just its name.

**Architecture:** A new `internal/searchindex` package owns a SQLite FTS5 index at `~/.jarvis/search/index.db` (one weighted-column row per session, `trigram` tokenizer). A denoising streaming parser extracts only real conversational text from each transcript JSONL. An incremental mtime-based `Sync()` runs in the background on dashboard start. The TUI's existing search mode delegates to `Search()` for queries ≥3 chars (falling back to the current in-memory name filter for shorter ones), renders spotlight-style results with highlighted snippets, and Enter reuses the existing `attachMsg` attach/resume path.

**Tech Stack:** Go 1.24, `modernc.org/sqlite` (pure-Go, cgo-free, FTS5), Bubble Tea, existing `internal/{store,paths,ui,model,session}` packages.

**Reference spec:** `docs/superpowers/specs/2026-07-05-session-fulltext-search-design.md`

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/searchindex/extract.go` (new) | Pure streaming JSONL parser + denoising (`ParseTranscript`, `isSyntheticUserText`, `isToolNoise`, truncation). No DB, no I/O beyond the reader. |
| `internal/searchindex/extract_test.go` (new) | Heaviest test coverage — every denoising rule. |
| `internal/searchindex/db.go` (new) | Open/migrate the SQLite DB (WAL, FTS5 schema, `index_meta`). |
| `internal/searchindex/fts5_smoke_test.go` (new) | Go/no-go: FTS5 available + `snippet(-1)` works in the vendored driver. |
| `internal/searchindex/index.go` (new) | `Index` type: `Sync()` incremental indexing, upsert/delete by rowid. |
| `internal/searchindex/index_test.go` (new) | Sync incrementality, delete. |
| `internal/searchindex/search.go` (new) | `Search()`: query escaping, bm25/snippet query, result resolution via store. |
| `internal/searchindex/search_test.go` (new) | Ranking, trigram CJK, snippet markers. |
| `internal/tui/search.go` (new) | `fullTextItems()` — bridges `Result` → `ListItem`. |
| `internal/tui/dashboard.go` (modify) | Open index in `NewDashboard`, background `Sync` in `Init`, full-text branch in `filteredItems`, mouse handling in `Update`. |
| `internal/tui/view.go` (modify) | Spotlight snippet rendering with marker highlighting. |
| `internal/tui/styles.go` (modify) | Snippet highlight style. |
| `cmd/jarvis/main.go` (modify) | `tea.WithMouseCellMotion()`. |
| `go.mod` / `go.sum` (modify) | Add `modernc.org/sqlite`. |

**Sentinel markers:** snippet matches are wrapped with ASCII `STX` (`\x02`) / `ETX` (`\x03`), referenced in code as `char(2)`/`char(3)` (SQL) and `"\x02"`/`"\x03"` (Go). Defined once as constants in `search.go`.

---

## Task 0: Add dependency and prove FTS5 + snippet(-1) work

This is the go/no-go gate for the whole design. If the vendored driver lacks FTS5 or `snippet(-1)`, stop and revisit the driver choice before building anything else.

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/searchindex/fts5_smoke_test.go`

- [ ] **Step 1: Add the dependency**

Run:
```bash
go get modernc.org/sqlite@latest
```
Expected: `go.mod` gains a `modernc.org/sqlite` require line; `go.sum` updated.

- [ ] **Step 2: Write the smoke test**

Create `internal/searchindex/fts5_smoke_test.go`:
```go
package searchindex

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestFTS5AndSnippetAvailable is the go/no-go gate: the whole design depends on
// FTS5 being compiled into the driver and snippet(-1) auto-selecting the
// best-matching column.
func TestFTS5AndSnippetAvailable(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE VIRTUAL TABLE t USING fts5(a, b, tokenize='trigram')`)
	if err != nil {
		t.Fatalf("FTS5 not available in this driver build: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO t(a, b) VALUES ('hello world', 'goodbye planet')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var snip string
	err = db.QueryRow(
		`SELECT snippet(t, -1, '[', ']', '…', 8) FROM t WHERE t MATCH '"planet"'`,
	).Scan(&snip)
	if err != nil {
		t.Fatalf("snippet(-1) query failed: %v", err)
	}
	// -1 must pick column b (where "planet" is), not column a.
	if !strings.Contains(snip, "[planet]") {
		t.Fatalf("snippet(-1) did not highlight the match in the right column: %q", snip)
	}
}
```

- [ ] **Step 3: Run the smoke test**

Run:
```bash
go test ./internal/searchindex/ -run TestFTS5AndSnippetAvailable -v
```
Expected: PASS. If it FAILS on the CREATE VIRTUAL TABLE line, the driver lacks FTS5 — stop and switch to a build that includes it (e.g. `mattn/go-sqlite3` with `-tags sqlite_fts5`, accepting cgo) before continuing.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum internal/searchindex/fts5_smoke_test.go
git commit -m "feat(searchindex): add modernc.org/sqlite; verify FTS5 + snippet(-1)"
```

---

## Task 1: Denoising transcript parser (`extract.go`)

The core of search quality. Pure function over an `io.Reader` — no DB, fully testable.

**Files:**
- Create: `internal/searchindex/extract.go`
- Test: `internal/searchindex/extract_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/searchindex/extract_test.go`:
```go
package searchindex

import (
	"strings"
	"testing"
)

func TestParseTranscript_RealPromptAndReply(t *testing.T) {
	jsonl := strings.Join([]string{
		`{"type":"ai-title","aiTitle":"Socket deadlock fix"}`,
		`{"type":"user","message":{"role":"user","content":"the socket hangs on detach"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"The daemon never closes the attached client connection, so readPTY blocks forever on the write."}]}}`,
	}, "\n")

	ps, err := ParseTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ps.AITitle != "Socket deadlock fix" {
		t.Errorf("AITitle = %q", ps.AITitle)
	}
	if ps.InitialPrompt != "the socket hangs on detach" {
		t.Errorf("InitialPrompt = %q", ps.InitialPrompt)
	}
	if !strings.Contains(ps.UserText, "the socket hangs on detach") {
		t.Errorf("UserText missing prompt: %q", ps.UserText)
	}
	if !strings.Contains(ps.AssistantText, "readPTY blocks forever") {
		t.Errorf("AssistantText missing reply: %q", ps.AssistantText)
	}
	if strings.Contains(ps.AssistantText, "hmm") {
		t.Errorf("AssistantText should not include thinking blocks: %q", ps.AssistantText)
	}
}

func TestParseTranscript_SkipsSyntheticUser(t *testing.T) {
	jsonl := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"<system-reminder>be nice</system-reminder>"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"file contents"}]}}`,
		`{"type":"user","message":{"role":"user","content":"[Request interrupted by user]"}}`,
		`{"type":"user","message":{"role":"user","content":"actually find the marketplace session"}}`,
	}, "\n")

	ps, err := ParseTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// The first *real* prompt must be the 4th record, not the synthetic ones.
	if ps.InitialPrompt != "actually find the marketplace session" {
		t.Errorf("InitialPrompt = %q, want the real prompt", ps.InitialPrompt)
	}
	if strings.Contains(ps.UserText, "system-reminder") ||
		strings.Contains(ps.UserText, "file contents") ||
		strings.Contains(ps.UserText, "Request interrupted") {
		t.Errorf("UserText leaked synthetic content: %q", ps.UserText)
	}
}

func TestParseTranscript_SkipsToolNoise(t *testing.T) {
	longReply := "This is a substantive assistant reply that is comfortably longer than the fifty character noise threshold."
	jsonl := strings.Join([]string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Let me read that."}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"[Tool: Bash] running a command that is otherwise long enough to pass the length gate easily."}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + longReply + `"}]}}`,
	}, "\n")

	ps, err := ParseTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if strings.Contains(ps.AssistantText, "Let me read") {
		t.Errorf("short filler not filtered: %q", ps.AssistantText)
	}
	if strings.Contains(ps.AssistantText, "[Tool:") {
		t.Errorf("tool marker not filtered: %q", ps.AssistantText)
	}
	if !strings.Contains(ps.AssistantText, "substantive assistant reply") {
		t.Errorf("real reply dropped: %q", ps.AssistantText)
	}
}

func TestParseTranscript_TruncatesLongReply(t *testing.T) {
	body := strings.Repeat("A", 500) + strings.Repeat("B", 400) + strings.Repeat("C", 200) // 1100 chars
	jsonl := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + body + `"}]}}`

	ps, err := ParseTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strings.Contains(ps.AssistantText, "…") {
		t.Errorf("expected ellipsis in truncated reply: %q", ps.AssistantText)
	}
	if strings.Count(ps.AssistantText, "B") == 400 {
		t.Errorf("middle should have been dropped by truncation")
	}
	// Kept head (A) and tail (C), dropped the middle (B).
	if !strings.HasPrefix(ps.AssistantText, strings.Repeat("A", 500)) {
		t.Errorf("head not preserved")
	}
	if !strings.HasSuffix(strings.TrimRight(ps.AssistantText, "\n"), strings.Repeat("C", 200)) {
		t.Errorf("tail not preserved")
	}
}

func TestParseTranscript_InitialPromptTruncated(t *testing.T) {
	prompt := strings.Repeat("x", 500)
	jsonl := `{"type":"user","message":{"role":"user","content":"` + prompt + `"}}`
	ps, err := ParseTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len([]rune(ps.InitialPrompt)) > 200 {
		t.Errorf("InitialPrompt not truncated to 200 runes: got %d", len([]rune(ps.InitialPrompt)))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
go test ./internal/searchindex/ -run TestParseTranscript -v
```
Expected: FAIL / build error — `ParseTranscript` and `ParsedSession` are undefined.

- [ ] **Step 3: Implement `extract.go`**

Create `internal/searchindex/extract.go`:
```go
package searchindex

import (
	"bufio"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

// ParsedSession is the denoised, searchable content extracted from one
// Claude Code transcript JSONL file.
type ParsedSession struct {
	AITitle       string // latest ai-title record
	InitialPrompt string // first real human prompt, truncated to 200 runes
	UserText      string // all real user messages, newline-joined
	AssistantText string // all assistant text replies, denoised + truncated
}

// transcriptRecord is the subset of a JSONL line we care about.
type transcriptRecord struct {
	Type    string `json:"type"`
	AITitle string `json:"aiTitle"`
	Message *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

const (
	maxInitialPromptRunes = 200
	replyTruncateAbove    = 800
	replyHeadRunes        = 500
	replyTailRunes        = 200
	toolNoiseMinLen       = 50
	maxColumnRunes        = 200_000 // per-column safety cap
)

// syntheticUserPrefixes mark `user`-role records that are not the human typing:
// system reminders, hook context, slash-command wrappers, command output, and
// interruption notices. Content that starts with any of these is excluded.
var syntheticUserPrefixes = []string{
	"<system-reminder>",
	"<local-command-stdout>",
	"<local-command-stderr>",
	"<command-name>",
	"<command-message>",
	"<user-prompt-submit-hook>",
	"[Request interrupted",
	"Caveat:",
	"This session is being continued",
}

// ParseTranscript streams a Claude Code JSONL transcript and returns its
// denoised searchable buckets. Malformed lines are skipped.
func ParseTranscript(r io.Reader) (ParsedSession, error) {
	var ps ParsedSession
	var users, assistants []string

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // transcript lines can be large

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec transcriptRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // skip malformed / partial line
		}

		switch rec.Type {
		case "ai-title":
			if rec.AITitle != "" {
				ps.AITitle = rec.AITitle
			}
		case "user":
			text := realUserText(rec)
			if text == "" {
				continue
			}
			if ps.InitialPrompt == "" {
				ps.InitialPrompt = truncateRunes(text, maxInitialPromptRunes)
			}
			users = append(users, text)
		case "assistant":
			for _, blk := range textBlocks(rec) {
				if isToolNoise(blk) {
					continue
				}
				assistants = append(assistants, truncateReply(blk))
			}
		}
	}
	if err := sc.Err(); err != nil {
		return ps, err
	}

	ps.UserText = capRunes(strings.Join(users, "\n"), maxColumnRunes)
	ps.AssistantText = capRunes(strings.Join(assistants, "\n"), maxColumnRunes)
	return ps, nil
}

// realUserText returns the human-typed text of a user record, or "" if the
// record is synthetic (array content = tool_result carrier, or a synthetic
// prefix).
func realUserText(rec transcriptRecord) string {
	if rec.Message == nil {
		return ""
	}
	// Real prompts have string content; tool_result carriers have array content.
	var s string
	if err := json.Unmarshal(rec.Message.Content, &s); err != nil {
		return "" // array content → synthetic
	}
	s = strings.TrimSpace(s)
	if len([]rune(s)) < 5 {
		return ""
	}
	if isSyntheticUserText(s) {
		return ""
	}
	return s
}

// textBlocks returns the text of every `type:"text"` block in an assistant
// record (dropping thinking / tool_use / image).
func textBlocks(rec transcriptRecord) []string {
	if rec.Message == nil {
		return nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(rec.Message.Content, &blocks); err != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			out = append(out, strings.TrimSpace(b.Text))
		}
	}
	return out
}

func isSyntheticUserText(s string) bool {
	t := strings.TrimSpace(s)
	for _, p := range syntheticUserPrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

var toolMarkerRe = regexp.MustCompile(`\[Tool:`)

func isToolNoise(s string) bool {
	t := strings.TrimSpace(s)
	if len([]rune(t)) < toolNoiseMinLen {
		return true
	}
	if toolMarkerRe.MatchString(t) {
		return true
	}
	return false
}

func truncateReply(s string) string {
	r := []rune(s)
	if len(r) <= replyTruncateAbove {
		return s
	}
	return string(r[:replyHeadRunes]) + "…" + string(r[len(r)-replyTailRunes:])
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func capRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
go test ./internal/searchindex/ -run TestParseTranscript -v
```
Expected: PASS (all five).

- [ ] **Step 5: Commit**

```bash
git add internal/searchindex/extract.go internal/searchindex/extract_test.go
git commit -m "feat(searchindex): denoising transcript parser"
```

---

## Task 2: Database open + schema (`db.go`)

**Files:**
- Create: `internal/searchindex/db.go`
- Test: covered indirectly by Task 3/4; add a focused open test here.

- [ ] **Step 1: Write the failing test**

Create `internal/searchindex/db_test.go`:
```go
package searchindex

import (
	"path/filepath"
	"testing"
)

func TestOpenCreatesSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	idx, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()

	// Both tables must exist and be queryable.
	if _, err := idx.db.Exec(`INSERT INTO sessions_fts(jarvis_id, name) VALUES ('x', 'hello')`); err != nil {
		t.Fatalf("sessions_fts not usable: %v", err)
	}
	if _, err := idx.db.Exec(`INSERT INTO index_meta(jarvis_id, rowid_ref, transcript_path, indexed_mtime, ai_title) VALUES ('x', 1, '/p', 0, '')`); err != nil {
		t.Fatalf("index_meta not usable: %v", err)
	}

	// Reopening the same path must not error (idempotent migration).
	idx2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	idx2.Close()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:
```bash
go test ./internal/searchindex/ -run TestOpenCreatesSchema -v
```
Expected: FAIL — `Open` / `Index` undefined.

- [ ] **Step 3: Implement `db.go`**

Create `internal/searchindex/db.go`:
```go
package searchindex

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"jarvis/internal/store"

	_ "modernc.org/sqlite"
)

// Index is a handle to the SQLite FTS5 search index.
type Index struct {
	db *sql.DB
}

// DefaultPath returns the standard index location under JARVIS_HOME.
func DefaultPath() string {
	return filepath.Join(store.JarvisHome(), "search", "index.db")
}

// Open opens (creating if needed) the index database at path and ensures the
// schema exists. WAL journaling and a per-connection busy_timeout are applied
// via the DSN so every pooled connection gets them at connect time — this lets
// background Sync writes coexist with Search reads without instant SQLITE_BUSY
// failures.
func Open(path string) (*Index, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir index dir: %w", err)
	}
	dsn := path + "?_pragma=busy_timeout(3000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	idx := &Index{db: db}
	if err := idx.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return idx, nil
}

func (i *Index) migrate() error {
	stmts := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS sessions_fts USING fts5(
			jarvis_id UNINDEXED,
			name,
			initial_prompt,
			user_text,
			assistant_text,
			metadata,
			tokenize='trigram'
		)`,
		`CREATE TABLE IF NOT EXISTS index_meta(
			jarvis_id       TEXT PRIMARY KEY,
			rowid_ref       INTEGER NOT NULL,
			transcript_path TEXT,
			indexed_mtime   INTEGER,
			ai_title        TEXT
		)`,
	}
	for _, s := range stmts {
		if _, err := i.db.Exec(s); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

// Close releases the database handle.
func (i *Index) Close() error {
	if i == nil || i.db == nil {
		return nil
	}
	return i.db.Close()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run:
```bash
go test ./internal/searchindex/ -run TestOpenCreatesSchema -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/searchindex/db.go internal/searchindex/db_test.go
git commit -m "feat(searchindex): sqlite FTS5 schema + open"
```

---

## Task 3: Incremental indexing (`index.go`)

**Files:**
- Create: `internal/searchindex/index.go`
- Test: `internal/searchindex/index_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/searchindex/index_test.go`:
```go
package searchindex

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"jarvis/internal/model"
	"jarvis/internal/store"
)

// seedSession writes a session.yaml under a temp JARVIS_HOME and a transcript
// JSONL under a fake ~/.claude/projects dir, returning the transcript path.
func seedSession(t *testing.T, id, name, launchDir, claudeID, jsonl string) string {
	t.Helper()
	s := &model.Session{
		ID: id, Type: "session", Name: name, Status: model.StatusActive,
		LaunchDir: launchDir, ClaudeSessionID: claudeID,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := store.SaveSession(s); err != nil {
		t.Fatalf("save session: %v", err)
	}
	// Transcript lives at <home>/.claude/projects/<encode(launchDir)>/<claudeID>.jsonl
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".claude", "projects", encodeForTest(launchDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir projects: %v", err)
	}
	p := filepath.Join(dir, claudeID+".jsonl")
	if err := os.WriteFile(p, []byte(jsonl), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	t.Cleanup(func() { os.Remove(p) })
	return p
}

func encodeForTest(cwd string) string {
	// mirror paths.EncodeCWD
	out := make([]rune, 0, len(cwd))
	for _, r := range cwd {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			out = append(out, r)
		} else {
			out = append(out, '-')
		}
	}
	return string(out)
}

func TestSyncIndexesAndIsIncremental(t *testing.T) {
	t.Setenv("JARVIS_HOME", t.TempDir())
	launch := t.TempDir()
	seedSession(t, "sess1", "Deadlock fix", launch, "claude-1",
		`{"type":"user","message":{"role":"user","content":"the socket hangs on detach forever"}}`)

	idx, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()

	n, err := idx.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if n != 1 {
		t.Fatalf("first sync indexed %d sessions, want 1", n)
	}

	// Second sync with no file change must reindex nothing.
	n, err = idx.Sync()
	if err != nil {
		t.Fatalf("sync 2: %v", err)
	}
	if n != 0 {
		t.Fatalf("second sync reindexed %d, want 0 (incremental)", n)
	}
}

func TestSyncRemovesDeletedSession(t *testing.T) {
	t.Setenv("JARVIS_HOME", t.TempDir())
	launch := t.TempDir()
	seedSession(t, "sess1", "Deadlock fix", launch, "claude-1",
		`{"type":"user","message":{"role":"user","content":"the socket hangs on detach forever"}}`)

	idx, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()
	if _, err := idx.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if err := store.DeleteSession("sess1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := idx.Sync(); err != nil {
		t.Fatalf("sync 2: %v", err)
	}

	var count int
	idx.db.QueryRow(`SELECT count(*) FROM index_meta`).Scan(&count)
	if count != 0 {
		t.Fatalf("index_meta still has %d rows after session delete", count)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
go test ./internal/searchindex/ -run TestSync -v
```
Expected: FAIL — `Sync` undefined.

- [ ] **Step 3: Implement `index.go`**

Create `internal/searchindex/index.go`:
```go
package searchindex

import (
	"database/sql"
	"os"
	"strings"

	"jarvis/internal/paths"
	"jarvis/internal/store"
)

// Sync brings the index up to date with the session store. It (re)indexes any
// session whose transcript is new or whose mtime changed, and removes rows for
// sessions that no longer exist. Returns the number of sessions (re)indexed.
func (i *Index) Sync() (int, error) {
	sessions, err := store.ListSessions(nil)
	if err != nil {
		return 0, err
	}

	live := make(map[string]bool, len(sessions))
	reindexed := 0

	for _, s := range sessions {
		live[s.ID] = true
		if s.ClaudeSessionID == "" {
			continue // no transcript to index
		}
		path, mtime, ok := transcriptPathFor(s.LaunchDir, s.WorktreeDir, s.ClaudeSessionID)
		if !ok {
			continue // transcript not found yet
		}

		var storedMtime int64
		err := i.db.QueryRow(`SELECT indexed_mtime FROM index_meta WHERE jarvis_id=?`, s.ID).Scan(&storedMtime)
		if err == nil && storedMtime == mtime {
			continue // up to date
		}

		f, err := os.Open(path)
		if err != nil {
			continue
		}
		ps, perr := ParseTranscript(f)
		f.Close()
		if perr != nil {
			continue
		}

		metadata := strings.Join([]string{s.Name, ps.AITitle, s.LaunchDir}, " ")
		if err := i.upsert(s.ID, s.Name, ps, metadata, path, mtime); err != nil {
			return reindexed, err
		}
		reindexed++
	}

	// Remove rows whose session is gone.
	if err := i.pruneDeleted(live); err != nil {
		return reindexed, err
	}
	return reindexed, nil
}

// upsert replaces a session's FTS row and meta row inside a transaction,
// reusing the FTS rowid so index_meta stays valid.
func (i *Index) upsert(jarvisID, name string, ps ParsedSession, metadata, path string, mtime int64) error {
	tx, err := i.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var oldRowid int64
	err = tx.QueryRow(`SELECT rowid_ref FROM index_meta WHERE jarvis_id=?`, jarvisID).Scan(&oldRowid)
	if err == nil {
		if _, err := tx.Exec(`DELETE FROM sessions_fts WHERE rowid=?`, oldRowid); err != nil {
			return err
		}
	} else if err != sql.ErrNoRows {
		return err
	}

	res, err := tx.Exec(
		`INSERT INTO sessions_fts(jarvis_id, name, initial_prompt, user_text, assistant_text, metadata)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		jarvisID, name, ps.InitialPrompt, ps.UserText, ps.AssistantText, metadata,
	)
	if err != nil {
		return err
	}
	newRowid, err := res.LastInsertId()
	if err != nil {
		return err
	}

	if _, err := tx.Exec(
		`INSERT INTO index_meta(jarvis_id, rowid_ref, transcript_path, indexed_mtime, ai_title)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(jarvis_id) DO UPDATE SET
		   rowid_ref=excluded.rowid_ref,
		   transcript_path=excluded.transcript_path,
		   indexed_mtime=excluded.indexed_mtime,
		   ai_title=excluded.ai_title`,
		jarvisID, newRowid, path, mtime, ps.AITitle,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (i *Index) pruneDeleted(live map[string]bool) error {
	rows, err := i.db.Query(`SELECT jarvis_id, rowid_ref FROM index_meta`)
	if err != nil {
		return err
	}
	type row struct {
		id    string
		rowid int64
	}
	var stale []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.rowid); err != nil {
			rows.Close()
			return err
		}
		if !live[r.id] {
			stale = append(stale, r)
		}
	}
	rows.Close()

	for _, r := range stale {
		if _, err := i.db.Exec(`DELETE FROM sessions_fts WHERE rowid=?`, r.rowid); err != nil {
			return err
		}
		if _, err := i.db.Exec(`DELETE FROM index_meta WHERE jarvis_id=?`, r.id); err != nil {
			return err
		}
	}
	return nil
}

// transcriptPathFor locates a session's transcript JSONL and returns its path
// and mtime (unix nanos). It tries the launch dir and, if set, the worktree
// dir, mirroring resume logic.
func transcriptPathFor(launchDir, worktreeDir, claudeID string) (string, int64, bool) {
	var candidates []string
	for _, dir := range paths.ProjectDirs(launchDir) {
		candidates = append(candidates, dir)
	}
	if worktreeDir != "" && worktreeDir != launchDir {
		for _, dir := range paths.ProjectDirs(worktreeDir) {
			candidates = append(candidates, dir)
		}
	}
	for _, dir := range candidates {
		p := dir + string(os.PathSeparator) + claudeID + ".jsonl"
		if fi, err := os.Stat(p); err == nil {
			return p, fi.ModTime().UnixNano(), true
		}
	}
	return "", 0, false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
go test ./internal/searchindex/ -run TestSync -v
```
Expected: PASS (both).

> Note: `paths.ProjectDirs` shells out to `git`. In tests the temp launch dir isn't a git repo, so it returns only the encoded-CWD candidate — which is exactly where `seedSession` writes the transcript. Good.

- [ ] **Step 5: Commit**

```bash
git add internal/searchindex/index.go internal/searchindex/index_test.go
git commit -m "feat(searchindex): incremental mtime-based Sync"
```

---

## Task 4: Search query + result resolution (`search.go`)

**Files:**
- Create: `internal/searchindex/search.go`
- Test: `internal/searchindex/search_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/searchindex/search_test.go`:
```go
package searchindex

import (
	"path/filepath"
	"strings"
	"testing"
)

func newSeededIndex(t *testing.T) *Index {
	t.Helper()
	t.Setenv("JARVIS_HOME", t.TempDir())
	launch := t.TempDir()

	seedSession(t, "sess-name", "PTY deadlock", launch, "c-name",
		`{"type":"user","message":{"role":"user","content":"just some unrelated chatter about lunch plans"}}`)
	seedSession(t, "sess-body", "random title", launch, "c-body",
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"The marketplace socket timeout happens because the retry backoff is too aggressive here."}]}}`)
	seedSession(t, "sess-cjk", "中文会话", launch, "c-cjk",
		`{"type":"user","message":{"role":"user","content":"我想查一下六月的飞机票价格和时间"}}`)

	idx, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := idx.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

func TestSearchMatchesBody(t *testing.T) {
	idx := newSeededIndex(t)
	res, err := idx.Search("marketplace socket")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) == 0 || res[0].JarvisID != "sess-body" {
		t.Fatalf("expected sess-body first, got %+v", res)
	}
	if !strings.Contains(res[0].Snippet, "\x02") {
		t.Errorf("snippet missing highlight markers: %q", res[0].Snippet)
	}
}

func TestSearchNameOutranksBody(t *testing.T) {
	idx := newSeededIndex(t)
	// "deadlock" appears only in sess-name's *name*. It must come first.
	res, err := idx.Search("deadlock")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) == 0 || res[0].JarvisID != "sess-name" {
		t.Fatalf("expected sess-name first for a name hit, got %+v", res)
	}
	if res[0].Name != "PTY deadlock" {
		t.Errorf("Name = %q", res[0].Name)
	}
}

func TestSearchChineseTrigram(t *testing.T) {
	idx := newSeededIndex(t)
	res, err := idx.Search("飞机票") // 3 chars — satisfies trigram minimum
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) == 0 || res[0].JarvisID != "sess-cjk" {
		t.Fatalf("expected sess-cjk for Chinese query, got %+v", res)
	}
}

func TestSearchEscapesSpecialChars(t *testing.T) {
	idx := newSeededIndex(t)
	// A query full of FTS5 syntax characters must not error.
	if _, err := idx.Search(`"marketplace" AND (socket*`); err != nil {
		t.Fatalf("special-char query errored: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
go test ./internal/searchindex/ -run TestSearch -v
```
Expected: FAIL — `Search` / `Result` undefined.

- [ ] **Step 3: Implement `search.go`**

Create `internal/searchindex/search.go`:
```go
package searchindex

import (
	"strings"

	"jarvis/internal/model"
	"jarvis/internal/store"
	"jarvis/internal/ui"
)

// Highlight markers wrapped around matched terms by snippet(). The TUI restyles
// them; they are ASCII control chars that never appear in real text.
const (
	MarkOpen  = "\x02"
	MarkClose = "\x03"
)

const untitledPlaceholder = "(untitled chat)"

// Result is one search hit, resolved to a jarvis session.
type Result struct {
	JarvisID string
	Name     string
	Snippet  string
	Age      string
	Status   model.SessionStatus
}

// Search runs a full-text query and returns matching sessions, best first.
func (i *Index) Search(query string) ([]Result, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	match := escapeFTSQuery(q)

	rows, err := i.db.Query(
		`SELECT f.jarvis_id, m.ai_title,
		        snippet(sessions_fts, -1, ?, ?, '…', 64)
		 FROM sessions_fts f
		 JOIN index_meta m ON m.rowid_ref = f.rowid
		 WHERE sessions_fts MATCH ?
		 ORDER BY bm25(sessions_fts, 0.0, 12.0, 8.0, 2.0, 1.0, 3.0)
		 LIMIT 50`,
		MarkOpen, MarkClose, match,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Result
	for rows.Next() {
		var id, aiTitle, snip string
		if err := rows.Scan(&id, &aiTitle, &snip); err != nil {
			return nil, err
		}
		sess, err := store.GetSession(id)
		if err != nil {
			continue // stale row; session gone
		}
		name := sess.Name
		if name == "" || name == untitledPlaceholder {
			if aiTitle != "" {
				name = aiTitle
			}
		}
		results = append(results, Result{
			JarvisID: id,
			Name:     name,
			Snippet:  snip,
			Age:      ui.FormatAge(sess.UpdatedAt),
			Status:   sess.Status,
		})
	}
	return results, rows.Err()
}

// escapeFTSQuery turns arbitrary user input into a safe FTS5 MATCH expression by
// wrapping it as a single quoted phrase (doubling embedded quotes). With the
// trigram tokenizer a quoted phrase matches any substring, which is the
// substring-search behavior we want.
func escapeFTSQuery(q string) string {
	return `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
go test ./internal/searchindex/ -v
```
Expected: PASS (all searchindex tests, including Task 0-3).

- [ ] **Step 5: Commit**

```bash
git add internal/searchindex/search.go internal/searchindex/search_test.go
git commit -m "feat(searchindex): full-text Search with bm25 ranking + snippets"
```

---

## Task 5: Wire full-text search into the TUI

No automated Bubble Tea tests exist in this codebase; this task is implementation + manual verification. The attach path is unchanged (reuses `attachMsg`), so it stays covered by existing integration tests.

**Files:**
- Create: `internal/tui/search.go`
- Modify: `internal/tui/dashboard.go`, `internal/tui/view.go`, `internal/tui/styles.go`, `cmd/jarvis/main.go`

- [ ] **Step 1: Add the highlight style**

In `internal/tui/styles.go`, add a style for snippet matches (place near the other style definitions, matching the file's existing lipgloss style pattern):
```go
// snippetHighlightStyle colours the matched term inside a search snippet.
var snippetHighlightStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11"))
```

- [ ] **Step 2: Add the Result→ListItem bridge and index field**

Create `internal/tui/search.go`:
```go
package tui

import (
	"strings"

	"jarvis/internal/searchindex"
)

// fullTextItems runs the FTS query and maps hits to ListItems. The snippet is
// carried in Detail; markers are restyled at render time.
func (d Dashboard) fullTextItems() []ListItem {
	if d.idx == nil {
		return nil
	}
	results, err := d.idx.Search(d.searchQuery)
	if err != nil {
		return nil
	}
	items := make([]ListItem, 0, len(results))
	for _, r := range results {
		items = append(items, ListItem{
			Type:   ItemSession,
			ID:     r.JarvisID,
			Name:   r.Name,
			Status: r.Status,
			State:  "",
			Detail: r.Snippet,
			Age:    r.Age,
		})
	}
	return items
}

// styleSnippet replaces the FTS highlight markers with a lipgloss style and
// collapses newlines so the snippet renders on one line.
func styleSnippet(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, searchindex.MarkOpen, "\x00OPEN\x00")
	s = strings.ReplaceAll(s, searchindex.MarkClose, "\x00CLOSE\x00")
	// Restyle each OPEN..CLOSE span.
	var b strings.Builder
	for {
		start := strings.Index(s, "\x00OPEN\x00")
		if start < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:start])
		rest := s[start+len("\x00OPEN\x00"):]
		end := strings.Index(rest, "\x00CLOSE\x00")
		if end < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(snippetHighlightStyle.Render(rest[:end]))
		s = rest[end+len("\x00CLOSE\x00"):]
	}
	return b.String()
}
```

- [ ] **Step 3: Add the index to the Dashboard model and open it**

In `internal/tui/dashboard.go`:

3a. Add the import:
```go
	"jarvis/internal/searchindex"
```

3b. Add a field to the `Dashboard` struct (near `mgr *session.Manager`):
```go
	idx *searchindex.Index // full-text search index (nil if unavailable)
```

3c. In `NewDashboard`, after `session.RecoverAllSessions()`, open the index (failure is non-fatal — search degrades to name-only):
```go
	idx, err := searchindex.Open(searchindex.DefaultPath())
	if err != nil {
		idx = nil
	}
```
and set `idx: idx,` in the returned `Dashboard{...}` literal.

- [ ] **Step 4: Kick off a background Sync on start**

In `internal/tui/dashboard.go`, change `Init` to also run a background sync command:
```go
func (d Dashboard) Init() tea.Cmd {
	return tea.Batch(d.refreshItems(), d.syncIndex())
}

func (d Dashboard) syncIndex() tea.Cmd {
	return func() tea.Msg {
		if d.idx != nil {
			_, _ = d.idx.Sync()
		}
		return indexSyncedMsg{}
	}
}
```
Add the message type near the other message types:
```go
type indexSyncedMsg struct{}
```
And handle it in `Update` (so a completed sync refreshes results if a query is active) by adding a case:
```go
	case indexSyncedMsg:
		if d.mode == ModeSearch || d.searchQuery != "" {
			// results may have improved; no state change needed — View re-runs the query
		}
		return d, nil
```

- [ ] **Step 5: Use full-text results in `filteredItems`**

In `internal/tui/dashboard.go`, replace the search branch at the top of `filteredItems` (currently the `if d.searchQuery != ""` block that does name substring matching) with:
```go
	if d.searchQuery != "" {
		// Full-text search for queries long enough for the trigram tokenizer.
		if len([]rune(d.searchQuery)) >= 3 {
			if items := d.fullTextItems(); items != nil {
				return items
			}
		}
		// Fallback: in-memory name substring (short queries or no index).
		query := strings.ToLower(d.searchQuery)
		var result []ListItem
		for _, item := range d.items {
			if strings.Contains(strings.ToLower(item.Name), query) {
				result = append(result, item)
			}
		}
		return result
	}
```

- [ ] **Step 6: Render the snippet line in search mode**

In `internal/tui/view.go`, in the item-list loop, render a second indented snippet line for search-mode session rows that have a `Detail`. After the line that writes an item (`b.WriteString(... line ... "\n")`), add:
```go
		if d.mode == ModeSearch && item.IsSession() && item.Detail != "" {
			b.WriteString("    " + dimStyle.Render("↳ ") + styleSnippet(item.Detail) + "\n")
		}
```
(Adjust the surrounding structure so this runs for both the selected and non-selected branches — simplest is to compute `line` and write the cursor prefix, then unconditionally append the snippet line.)

- [ ] **Step 7: Enable the mouse**

In `cmd/jarvis/main.go`, in `runDashboard`, change:
```go
		p := tea.NewProgram(dashboard, tea.WithAltScreen())
```
to:
```go
		p := tea.NewProgram(dashboard, tea.WithAltScreen(), tea.WithMouseCellMotion())
```

- [ ] **Step 8: Handle mouse clicks to select a row**

In `internal/tui/dashboard.go` `Update`, add a case for mouse messages that maps a left-click Y coordinate to a visible row and moves the cursor there:
```go
	case tea.MouseMsg:
		if msg.Type == tea.MouseLeft {
			// Row 0 of the list starts after the 2-line header.
			row := msg.Y - 2 + d.scrollOffset
			visible := d.filteredItems()
			if row >= 0 && row < len(visible) {
				d.cursor = row
				d.adjustScroll()
			}
		}
		return d, nil
```
> Note: in search mode each result occupies two screen lines (row + snippet). If click precision matters there, refine the Y→row mapping later; for v1 the keyboard is the primary path and this gives approximate click-to-select.

- [ ] **Step 9: Build and vet**

Run:
```bash
go build ./... && go vet ./...
```
Expected: no errors.

- [ ] **Step 10: Run the full test suite**

Run:
```bash
go test ./... 2>&1 | tail -30
```
Expected: all packages PASS (existing tests + new `internal/searchindex` tests).

- [ ] **Step 11: Manual verification**

Run:
```bash
go build -o /tmp/jarvis ./cmd/jarvis && go build -o /tmp/jarvis-sidecar ./cmd/sidecar
/tmp/jarvis
```
Then in the dashboard:
1. Press `/` and type a word you know appears **inside** a past conversation (not in any session name), ≥3 chars.
2. Confirm matching sessions appear with a highlighted snippet line under each.
3. Confirm `↑/↓` move the selection and a mouse click selects a row.
4. Press Enter on a result and confirm it attaches (and resumes if the session was suspended).
5. Type a 1–2 char query and confirm it falls back to name matching without error.
6. Try a Chinese query of ≥3 characters if you have Chinese conversations.

- [ ] **Step 12: Commit**

```bash
git add internal/tui/search.go internal/tui/dashboard.go internal/tui/view.go internal/tui/styles.go cmd/jarvis/main.go
git commit -m "feat(tui): full-text session search with spotlight snippets"
```

---

## Task 6: Update ARCHITECTURE.md

**Files:**
- Modify: `docs/ARCHITECTURE.md`

- [ ] **Step 1: Add a package-map entry and a short section**

Add `internal/searchindex` to the package map table in `docs/ARCHITECTURE.md` (§3), and add a short subsection describing the FTS5 index (schema, incremental Sync on dashboard start, trigram tokenizer, snippet-based results, `<3`-char name fallback). Also note the new `search/index.db` file in the Persistence Model table (§7) and the `tea.WithMouseCellMotion()` addition.

- [ ] **Step 2: Commit**

```bash
git add docs/ARCHITECTURE.md
git commit -m "docs: document searchindex package in ARCHITECTURE.md"
```

---

## Self-Review Notes (author checklist — completed)

- **Spec coverage:** schema/columns (Task 2), trigram + `<3` fallback (Task 4 + Task 5 step 5), denoising rules incl. synthetic/tool-noise/truncation (Task 1), incremental mtime Sync + prune (Task 3), bm25 weights incl. UNINDEXED `jarvis_id=0` (Task 4), `snippet(-1)` (Task 0 gate + Task 4), background Sync on start (Task 5 step 4), spotlight rendering + Enter reuse of `attachMsg` (Task 5 steps 5-8), mouse (Task 5 steps 7-8), error/degradation to name-only (Task 5 steps 3,5), rollout/ARCHITECTURE (Task 6). All spec sections map to a task.
- **Type consistency:** `Index`, `ParsedSession{AITitle,InitialPrompt,UserText,AssistantText}`, `Result{JarvisID,Name,Snippet,Age,Status}`, `Open/DefaultPath/Sync/Search/Close`, `MarkOpen/MarkClose`, `index_meta.rowid_ref` are used consistently across tasks 1-5. `ParseTranscript(io.Reader)` signature matches all call sites.
- **Placeholder scan:** no TBD/TODO; every code step contains full code; every command has an expected result.
