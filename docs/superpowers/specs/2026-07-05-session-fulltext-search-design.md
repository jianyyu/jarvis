# Session Full-Text Search (SQLite FTS5) — Design

> Date: 2026-07-05 · Feature: search the dashboard by conversation content, not just session name

---

## 1. Problem

The dashboard's `/` search (`internal/tui/dashboard.go` `filteredItems()`) is a substring
match over `item.Name` only. With many sessions, the name is often unhelpful — a session
called `(untitled chat)` might contain exactly the conversation you're looking for. There is
no way to find a session by *what was discussed in it*.

We want the experience of a spotlight-style search: type a query, get a live list of matching
sessions, each shown with a **content snippet with the matched term highlighted** and a
timestamp, navigate with the keyboard/mouse, and press Enter to enter (attach to) that session.

Reference experience: the Claude desktop app's conversation search panel — titled results,
each with a highlighted excerpt from the matching message and a relative time on the right.

## 2. Goals / Non-Goals

**Goals**
- Full-text search over past Claude Code conversation content, live as you type.
- Spotlight-style results: session name/title + highlighted snippet + relative age.
- Works for both English and Chinese queries.
- Bounded, predictable memory and CPU regardless of total transcript size.
- Enter attaches the selected session via the existing attach/resume path.

**Non-Goals (explicit YAGNI for v1)**
- Semantic / LLM-based search (we scrapped the earlier Claude-chat design — literal
  full-text is simpler, deterministic, and fast).
- Searching sessions that never recorded a `claude_session_id` (they have no transcript;
  they remain matchable by name).
- Per-message result rows / multiple hits per session (v1 returns one row per session).
- Ranking tuning beyond bm25 column weights.

## 3. Key facts that shape the design

- Session metadata lives at `~/.jarvis/sessions/<jarvis_id>/session.yaml` with fields
  `id`, `name`, `status`, `launch_dir`, `claude_session_id`.
- Claude transcripts live at
  `~/.claude/projects/<encoded-launch-dir>/<claude_session_id>.jsonl`; the **filename stem
  is the `claude_session_id`**. The `internal/paths` package already encodes launch dirs.
- Transcripts are large, but the conversational text is a tiny fraction. Measured on a real
  1.2 MB transcript: `user`/`assistant` `text` blocks were **50 KB — 4.2%** of the file.
  The other ~96% is `tool_use` / `tool_result` / `thinking` / `file-history-snapshot` /
  `attachment` records. Indexing only the conversational text keeps the index small.
- The JSONL also contains `ai-title` records (Claude's auto-generated conversation title) —
  cheap to index and excellent for matching.

## 4. Architecture

### 4.1 New package `internal/searchindex`

Owns the SQLite database. No TUI knowledge; unit-testable in isolation.

**Driver:** `modernc.org/sqlite` — pure-Go, cgo-free, with FTS5 compiled in. This preserves
jarvis's static pure-Go build and cross-compiled releases. (`mattn/go-sqlite3` would require
cgo + a C toolchain and the `sqlite_fts5` build tag.)
> Plan step 0: verify the vendored `modernc.org/sqlite` build has FTS5 enabled (create the
> virtual table in a throwaway test DB). If not, revisit the driver choice.

**Storage:** `~/.jarvis/search/index.db` (fits the existing `~/.jarvis/` layout, honors
`$JARVIS_HOME`).

**Schema:**
```sql
CREATE VIRTUAL TABLE sessions_fts USING fts5(
  jarvis_id UNINDEXED,   -- returned for attach; not tokenized/searched
  name,                  -- session display name
  title,                 -- Claude ai-title (may be empty)
  body,                  -- concatenated user/assistant `text` blocks only
  tokenize = 'trigram'
);

CREATE TABLE index_meta (
  jarvis_id      TEXT PRIMARY KEY,
  transcript_path TEXT,
  indexed_mtime  INTEGER   -- unix nanos of the transcript file at last index
);
```

**Tokenizer: `trigram`.** Enables substring matching across mixed Chinese/English (e.g.
searching `飞机`). Limitation: FTS5 trigram requires queries of **≥ 3 characters**. For
queries shorter than 3 characters we fall back to an in-memory substring scan over session
names + titles (see §4.4). This fallback is name/title only — short-query content search is
out of scope.

### 4.2 Indexing — `Sync()`

Incremental, driven by file mtime:

1. List sessions from the store. For each with a non-empty `claude_session_id`, compute the
   transcript path via `internal/paths` (encode `LaunchDir`; also try `WorktreeDir` as a
   candidate directory, mirroring resume logic).
2. `stat` the transcript. If it does not exist → skip (session has no transcript yet).
3. If `mtime == index_meta.indexed_mtime` → up to date, skip.
4. Otherwise re-parse the transcript (streaming, line by line):
   - Keep `user`/`assistant` records; from their `content`, collect `text` blocks only.
     Skip `tool_use`, `tool_result`, `thinking`, `image`, and non-conversational record
     types.
   - Capture the latest `ai-title` record as `title`.
   - Concatenate the text into `body`, capped at **128 KB** per session (worst-case guard;
     the 4.2% ratio means this is rarely hit).
   - `INSERT OR REPLACE` the `sessions_fts` row and update `index_meta`.
5. Delete `sessions_fts` / `index_meta` rows whose session no longer exists in the store.

Streaming parse ⇒ memory stays flat regardless of transcript size (one line buffered at a
time). First run indexes everything once; subsequent runs touch only changed transcripts
(active sessions whose logs grew), so `Sync()` is cheap to call often.

**When Sync runs:** on dashboard start, as a background `tea.Cmd`. It does not block the UI;
results simply improve once it completes. (Search works against whatever is already indexed.)

### 4.3 Searching — `Search(query) → []Result`

```go
type Result struct {
    JarvisID string
    Name     string   // session name; if name is empty or the "(untitled chat)" placeholder and a title exists, use title
    Snippet  string   // from FTS5 snippet(): matched excerpt with highlight markers
    Age      string   // ui.FormatAge(session.UpdatedAt)
    Status   model.SessionStatus
}
```

One query:
```sql
SELECT jarvis_id, name, title,
       snippet(sessions_fts, 3, '\x02', '\x03', '…', 12) AS snip
FROM sessions_fts
WHERE sessions_fts MATCH ?
ORDER BY bm25(sessions_fts, 0.0, 10.0, 5.0, 1.0)   -- jarvis_id, name, title, body
LIMIT 50;
```
- `snippet()`'s second argument is the column index; `3` targets `body` (where content
  matches live — name/title matches are short and surfaced via the row's title anyway).
- `snippet()` wraps matches in sentinel markers (`\x02`/`\x03`) that the TUI restyles with
  lipgloss (bold/color) — avoids collision with real text.
- **`bm25` needs one weight per column, in column order — including the `UNINDEXED`
  `jarvis_id` (weight `0.0`).** Omitting it would misalign every weight. Order:
  `jarvis_id=0, name=10, title=5, body=1`, so name > title > body.
- The query string is built from user input escaped for FTS5 (wrap tokens to avoid syntax
  errors from punctuation/quotes).
- `Age`/`Status`/`Name` finalized by looking up each returned `jarvis_id` in the store
  (also validates the row isn't stale). Rows whose session is gone are dropped.

FTS5 lookups are sub-millisecond, so `Search()` runs **synchronously per keystroke** from the
TUI. If profiling later shows lag, wrap it in a debounced `tea.Cmd` (interface unchanged).

### 4.4 Short-query fallback

When `len([]rune(query)) < 3`: skip FTS, filter the already-loaded dashboard items by
substring over name + title in memory (today's behavior, extended to title). Keeps 1–2
character queries responsive and avoids trigram's minimum-length error.

## 5. TUI integration (`internal/tui`)

- **Search mode reuses the existing `ModeSearch`.** No new mode. What changes is how results
  are produced and rendered while a query is active.
- `filteredItems()` (or a search-mode branch of it) delegates to `searchindex.Search()` when
  a query is present and ≥ 3 chars, else to the in-memory fallback. Empty query → the normal
  dashboard tree, exactly as today.
- **Rendering (spotlight style):** each result row shows `💬` + name/title + a second line
  with the highlighted snippet + right-aligned age. Snippet sentinel markers are converted to
  a highlight style via `styles.go`. Reuses `ui.FormatAge`.
- **Selection:** `↑/↓` (and `j/k`) move the cursor over results; mouse click selects the row
  under the cursor (`tea.MouseMsg` → Y coordinate → row). Enabling mouse requires
  `tea.WithMouseCellMotion()` in `cmd/jarvis/main.go` `runDashboard()`. (Tradeoff: terminal
  drag-to-select-text then needs Shift held.)
- **Enter attaches:** emits `attachMsg{sessionID: result.JarvisID}` — the *same* message the
  dashboard emits when Enter is pressed on a session row (`dashboard.go`). `Update` sets
  `attachSessionID` + `tea.Quit`; `runDashboard()` calls `mgr.Attach(id)`, which resumes the
  session if suspended. `SaveState()` is called first, matching the existing handler. So
  attaching from search is identical to attaching from the dashboard.

## 6. Error handling & edge cases

| Situation | Behavior |
|---|---|
| `search/index.db` missing/unopenable | Log; search silently falls back to name/title in-memory filter. Never crashes the TUI. |
| FTS5 not available in driver build | Caught at plan step 0; would block the feature — resolve before building. |
| Transcript path can't be resolved (encoding mismatch) | Session simply isn't indexed; still matchable by name. |
| Session has no `claude_session_id` | Not indexed (no transcript); matchable by name. |
| Malformed / partially-written JSONL line | Skip that line; index the rest (active sessions are being appended to concurrently). |
| Query with FTS5-special characters | Escaped/quoted before MATCH; on parse error, fall back to in-memory filter for that keystroke. |
| Query < 3 chars | In-memory name/title substring fallback (§4.4). |
| Returned `jarvis_id` no longer in store | Dropped from results. |
| Concurrent write during Sync | SQLite single-writer; Sync runs in one goroutine. Reads (Search) use a separate connection. Use WAL mode. |

## 7. Testing

High-value logic is in `internal/searchindex` (pure-ish, DB-backed) and gets real coverage:

- **Parse/extract:** given sample JSONL bytes (mixed record types, tool payloads, ai-title,
  malformed line) → assert `body` contains only user/assistant text, `title` captured, cap
  respected.
- **Sync incrementality:** index a temp transcript; re-`Sync()` with unchanged mtime → no
  reparse (assert via a parse counter/spy); bump mtime → reparsed; delete session → row
  removed.
- **Search:** seed rows; assert MATCH returns expected `jarvis_id`s, bm25 ordering
  (name hit ranks above body hit), `snippet()` markers present around the match; Chinese
  query (`飞机`) matches Chinese body via trigram; 2-char query routes to fallback.
- **FTS5 availability (plan step 0):** a test that creates the virtual table — fails loudly
  if the driver lacks FTS5.
- **TUI:** no automated Bubble Tea tests (consistent with the current codebase). The attach
  path is already covered by existing integration tests since we reuse `attachMsg`. Manual
  verification of the spotlight rendering + mouse.

## 8. File / unit layout

- `internal/searchindex/db.go` — open/migrate the DB (WAL, schema), path via `store.JarvisHome()`.
- `internal/searchindex/extract.go` — streaming JSONL → `(title, body)` extraction; pure, well-tested.
- `internal/searchindex/sync.go` — `Sync()` incremental indexing.
- `internal/searchindex/search.go` — `Search()`, query escaping, bm25/snippet, result resolution.
- `internal/tui/dashboard.go` — search-mode branch delegates to searchindex; short-query fallback.
- `internal/tui/view.go` — spotlight result rendering (snippet highlight, age).
- `internal/tui/styles.go` — snippet highlight style.
- `cmd/jarvis/main.go` — `tea.WithMouseCellMotion()`; kick off background `Sync()` on start.
- `go.mod` — add `modernc.org/sqlite`.

## 9. Rollout notes

- First launch after this ships does the initial full index in the background; search quality
  ramps up over the first few seconds. No migration of existing data required.
- The index DB is disposable — deleting `~/.jarvis/search/` triggers a clean rebuild.
