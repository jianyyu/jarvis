# Auto-Rename Untitled Sessions — Design

**Date:** 2026-07-12
**Status:** Approved for implementation
**Branch:** `feat/auto-rename-untitled`

## Problem

Sessions created via `jarvis chat` are named `(untitled chat)` and stay that way
unless the user manually runs the `/jarvis-rename` skill inside the session.
The dashboard fills up with indistinguishable untitled entries.

## Goal

When the TUI dashboard starts, automatically rename every untitled session in
the background, using the session's **full Claude Code conversation context**
to infer an accurate title — without attaching to the session or polluting its
transcript.

## Non-Goals

- Renaming on a timer or on session-idle hooks (may come later; startup scan only).
- Renaming sessions the user has already named (`jarvis new` sessions).
- Any TUI-blocking behavior or user-visible errors from this feature.

## Approach

### Trigger & candidate detection

On dashboard startup, a background goroutine scans all sessions in the store
and selects candidates matching **all** of:

- `Name == "(untitled chat)"` (the only untitled source, `cmd/jarvis/main.go`).
- `Status` is active or suspended (skip done/archived).
- A Claude transcript is locatable: prefer `ClaudeSessionID`; if empty, fall
  back to `session.FindLatestSession(LaunchDir)`.
- The transcript contains at least one real user message (checked with the
  existing `searchindex.ParseTranscript`, which already filters system
  reminders and tool noise).

Sessions failing the last two checks are skipped silently and retried on the
next TUI startup. No extra state tracking is needed — an untitled session
remains a candidate until it is successfully renamed.

### Title generation (approach A: title-only prompt)

For each candidate, **sequentially** (one claude process at a time):

```bash
cd <sess.LaunchDir> && claude -p \
  --resume <claudeSessionID> \
  --fork-session \
  --output-format json \
  "Based on the entire conversation so far, output ONLY a 3-8 word title-case task name for this session. No explanation, no quotes."
```

Key properties:

- `--resume` loads the **entire** conversation context — identical to what the
  in-session Claude sees, satisfying the accuracy requirement.
- `-p` (print mode) answers once and exits; no PTY, no attach, no injection.
- `--fork-session` copies the context into a **new** session ID so the rename
  Q&A never appears in the original session's transcript.
- `--output-format json` yields `{"result": "<title>", "session_id": "<fork-id>", ...}`;
  the forked session's temporary JSONL is deleted using `session_id`.
- No `--model` override — use the user's default model for skill-equivalent quality.
- No tool permissions are granted to the headless call; it only outputs text.
- Per-call timeout: 120s. On timeout/failure/empty result: skip, log, never
  write a bad name.
- Title sanitization: strip quotes/newlines, collapse whitespace, cap length.

The decision to use a title-only prompt (approach A) instead of literally
invoking `/jarvis-rename` headlessly (approach B) was deliberate: A needs zero
tool permissions and has a smaller failure surface; the rename itself is done
by trusted Go code.

### Persisting & TUI refresh

- Extract the rename store-write from `renameCmd` in `cmd/jarvis/main.go` into
  a shared function (store/session layer): set `Name`, bump `UpdatedAt`,
  `SaveSession`.
- After each successful rename, the goroutine sends a `tea.Msg` to the
  dashboard, which reuses the existing `refreshItems` path so titles appear
  live without restarting.

## Code organization

- New package `internal/autorename`:
  - `FindCandidates(sessions []model.Session) []model.Session`
  - `GenerateTitle(sess) (string, error)` — wraps the claude -p invocation
    behind an interface so tests can mock it.
  - `Run(notify func(sessionID, newName string))` — orchestrates the scan loop.
- `internal/tui`: a `tea.Cmd` started from dashboard init that runs
  `autorename.Run` and translates notifications into refresh messages.
- Shared rename helper extracted from `cmd/jarvis/main.go`.

## Error handling

`claude` missing from PATH, auth expiry, corrupt JSONL, store write failures:
all skipped silently with a log line. This is a best-effort enhancement; it
must never block or visually disrupt the dashboard.

## Testing

- Unit tests: candidate filtering (name/status/transcript conditions), title
  sanitization, orchestration loop with a mocked title generator.
- One manual end-to-end verification of the real
  `claude -p --resume --fork-session` behavior (context loaded, original JSONL
  untouched, forked JSONL cleaned up).

## Constraints

- All implementation happens on `feat/auto-rename-untitled` in its own
  worktree; nothing lands directly on main.
