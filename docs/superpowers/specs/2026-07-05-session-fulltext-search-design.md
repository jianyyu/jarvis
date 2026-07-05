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

**Schema:** one row per session; the denoised content is split into **weighted columns**
(not lumped into a single `body`) so bm25 can rank a hit in the jarvis name / first prompt
above a hit buried in an assistant reply.
```sql
CREATE VIRTUAL TABLE sessions_fts USING fts5(
  jarvis_id UNINDEXED,   -- returned for attach; not tokenized/searched
  name,                  -- jarvis session name (the dashboard title) — highest weight
  initial_prompt,        -- first *real* human prompt (best "what was this about") — high weight
  user_text,             -- all real user messages, concatenated & denoised
  assistant_text,        -- all assistant text replies, denoised & per-reply truncated
  metadata,              -- name + ai-title + launch_dir (+ git branch if known)
  tokenize = 'trigram'
);

CREATE TABLE index_meta (
  jarvis_id      TEXT PRIMARY KEY,
  transcript_path TEXT,
  indexed_mtime  INTEGER   -- unix nanos of the transcript file at last index
);
```

Note the **jarvis session name is a dedicated, highest-weighted column** — so search matches
the dashboard title exactly as it does today, just now unified into the same FTS query as the
content. The Claude `ai-title` also lives in `metadata`.

**Tokenizer: `trigram`.** Enables substring matching across mixed Chinese/English (e.g.
searching `飞机`). Limitation: FTS5 trigram requires queries of **≥ 3 characters**. For
queries shorter than 3 characters we fall back to an in-memory substring scan over session
names (see §4.4). This fallback is name-only — short-query content search is out of scope.

### 4.2 Indexing — `Sync()`

Incremental, driven by file mtime:

1. List sessions from the store. For each with a non-empty `claude_session_id`, compute the
   transcript path via `internal/paths` (encode `LaunchDir`; also try `WorktreeDir` as a
   candidate directory, mirroring resume logic).
2. `stat` the transcript. If it does not exist → skip (session has no transcript yet).
3. If `mtime == index_meta.indexed_mtime` → up to date, skip.
4. Otherwise re-parse the transcript (streaming, line by line) into buckets, applying the
   denoising rules below, then map buckets → columns and `INSERT OR REPLACE` the row +
   update `index_meta`.
5. Delete `sessions_fts` / `index_meta` rows whose session no longer exists in the store.

**Parsing & denoising rules** (the core of `extract.go` — this is what keeps results clean;
adapted from a proven Claude-transcript indexer):

- **`extractContent`**: a record's `content` may be a string or a block array — take only
  `type: "text"` blocks; drop `tool_use`, `tool_result`, `thinking`, `image`.
- **Only `user` / `assistant` records** reach the buckets; `system`, `file-history-snapshot`,
  `attachment`, `queue-operation`, etc. are skipped.
- **`isSyntheticUserText`** — skip `user` records that aren't you typing: `tool_result`
  carriers, `<system-reminder>` blocks, hook `additionalContext`, `[Request interrupted…]`,
  local-command stdout/`<local-command-…>`, and other known injected prefixes. These must not
  count as `initial_prompt` or enter `user_text`. (Needs a jarvis-tuned prefix list.)
- **`firstPrompt`** — the first *real* human `user` message (post-synthetic-filter), truncated
  to ~200 chars → `initial_prompt` column.
- **user bucket** — remaining real user messages → `user_text`.
- **`detectToolNoise`** — skip assistant filler: very short (<50 runes) or containing a
  `[Tool: …]` marker. These don't enter `assistant_text`. (Prefix-based narration filtering
  was considered and rejected — long "Let me…" replies usually carry real content.)
- **assistant truncation** — for a kept reply >800 chars, store `first 500 + "…" + last 200`
  (not the whole thing) so one long reply can't dominate the index.
- **`<5` char** content is dropped outright.
- **`metadata`** column = `name + ai-title + launch_dir` (+ git branch if resolvable).

Streaming parse ⇒ memory stays flat regardless of transcript size (one line buffered at a
time), and the denoising means we index far less than even the 4.2% raw-text figure.

First run indexes everything once; subsequent runs touch only changed transcripts (active
sessions whose logs grew), so `Sync()` is cheap to call often.

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
SELECT jarvis_id, name,
       snippet(sessions_fts, -1, '\x02', '\x03', '…', 12) AS snip
FROM sessions_fts
WHERE sessions_fts MATCH ?
ORDER BY bm25(sessions_fts, 0.0, 12.0, 8.0, 2.0, 1.0, 3.0)
LIMIT 50;
```
- **bm25 needs one weight per column, in column order — including the `UNINDEXED`
  `jarvis_id` (weight `0.0`).** Order: `jarvis_id=0, name=12, initial_prompt=8, user_text=2,
  assistant_text=1, metadata=3`. So a hit in the jarvis session name outranks the first
  prompt, which outranks raw conversation body; metadata (ai-title etc.) sits in between.
- `snippet()`'s second arg is the column index; **`-1` auto-selects the best-matching
  column**, so the excerpt comes from wherever the match actually is (name, first prompt, or
  a reply) without us guessing. *(Plan step 0: verify `snippet(..., -1, ...)` behaves this way
  in the vendored FTS5 build; if not, fall back to snippeting `assistant_text`/`user_text` and
  picking the non-empty best in Go.)*
- `snippet()` wraps matches in sentinel markers (`\x02`/`\x03`) that the TUI restyles with
  lipgloss (bold/color) — avoids collision with real text.
- The query string is built from user input escaped for FTS5 (wrap tokens to avoid syntax
  errors from punctuation/quotes).
- `Age`/`Status`/`Name` finalized by looking up each returned `jarvis_id` in the store
  (also validates the row isn't stale). Rows whose session is gone are dropped. `Name` is the
  jarvis session name, falling back to the ai-title only if the name is empty or the
  `"(untitled chat)"` placeholder.

FTS5 lookups are sub-millisecond, so `Search()` runs **synchronously per keystroke** from the
TUI. If profiling later shows lag, wrap it in a debounced `tea.Cmd` (interface unchanged).

### 4.4 Short-query fallback

When `len([]rune(query)) < 3`: skip FTS, filter the already-loaded dashboard items by
substring over the session name in memory (today's behavior). Keeps 1–2 character queries
responsive and avoids trigram's minimum-length error.

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
| `search/index.db` missing/unopenable | Log; search silently falls back to the in-memory name substring filter. Never crashes the TUI. |
| FTS5 not available in driver build | Caught at plan step 0; would block the feature — resolve before building. |
| Transcript path can't be resolved (encoding mismatch) | Session simply isn't indexed; still matchable by name. |
| Session has no `claude_session_id` | Not indexed (no transcript); matchable by name. |
| Malformed / partially-written JSONL line | Skip that line; index the rest (active sessions are being appended to concurrently). |
| Query with FTS5-special characters | Escaped/quoted before MATCH; on parse error, fall back to in-memory filter for that keystroke. |
| Query < 3 chars | In-memory name substring fallback (§4.4). |
| Returned `jarvis_id` no longer in store | Dropped from results. |
| Concurrent write during Sync | SQLite single-writer; Sync runs in one goroutine. Reads (Search) use a separate connection. Use WAL mode. |

## 7. Testing

High-value logic is in `internal/searchindex` (pure-ish, DB-backed) and gets real coverage:

- **Parse/extract (`extract.go`) — the highest-value tests, since denoising is the whole
  point:** given sample JSONL bytes covering each rule → assert
  - synthetic `user` records (`<system-reminder>`, tool_result carriers, `[Request
    interrupted]`, local-command stdout) are excluded from `initial_prompt` and `user_text`;
  - `initial_prompt` = the first *real* human message, truncated to ~200 chars;
  - assistant tool-noise (`"Let me read…"`, `[Tool: …]`, <50 chars) excluded from
    `assistant_text`;
  - an assistant reply >800 chars stored as `first 500 + "…" + last 200`;
  - only `text` blocks kept (images/tool_use dropped);
  - `metadata` = name + ai-title + launch_dir.
- **Sync incrementality:** index a temp transcript; re-`Sync()` with unchanged mtime → no
  reparse (assert via a parse counter/spy); bump mtime → reparsed; delete session → row
  removed.
- **Search:** seed rows; assert MATCH returns expected `jarvis_id`s; bm25 ordering (a hit in
  `name` ranks above a hit only in `assistant_text`); `snippet()` markers present around the
  match; Chinese query (`飞机`) matches Chinese `user_text` via trigram; 2-char query routes
  to the fallback.
- **FTS5 + snippet(-1) availability (plan step 0):** a test that creates the virtual table
  and runs `snippet(..., -1, ...)` — fails loudly if the driver lacks FTS5 or `-1` doesn't
  auto-select the matching column.
- **TUI:** no automated Bubble Tea tests (consistent with the current codebase). The attach
  path is already covered by existing integration tests since we reuse `attachMsg`. Manual
  verification of the spotlight rendering + mouse.

## 8. File / unit layout

- `internal/searchindex/db.go` — open/migrate the DB (WAL, schema), path via `store.JarvisHome()`.
- `internal/searchindex/extract.go` — streaming JSONL → buckets (`initial_prompt`, `user_text`,
  `assistant_text`, `metadata`) with the denoising rules (`isSyntheticUserText`,
  `detectToolNoise`, truncation); pure, the most heavily tested unit.
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
