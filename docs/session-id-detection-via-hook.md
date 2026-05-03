# Session-ID Detection via Claude Code SessionStart Hook

## Problem

`claude_session_id` in `session.yaml` is sometimes wrong or missing. Two
real-world failure modes have been confirmed:

1. **Detection times out** with `could not detect Claude session ID within
   120s`, leaving `claude_session_id` empty. Conversation continues normally
   and the JSONL file is created — jarvis just never sees it. After a
   sidecar restart, jarvis launches fresh `claude` again (no `--resume`),
   Claude creates a new JSONL, and that one *does* get detected and stored.
   The original transcript is now orphaned. (Reproduced on session
   `ea1adaeb`: `a531e075-…jsonl` was the real conversation, but jarvis
   eventually pinned `4a480e14-…jsonl` — an empty 92-line continuation —
   because the original detection failed.)

2. **Stored ID becomes invalid.** If the JSONL no longer resolves under
   `launch_dir`/`workspace_dir` (worktree moved, file deleted, etc.),
   `manager.Resume` clears `ClaudeSessionID` and starts fresh, again
   orphaning the prior transcript.

Both come from the same root cause: jarvis discovers the Claude session ID
indirectly by snapshotting the project directory before `claude` starts and
polling for newly appearing `*.jsonl` files. The approach is racy,
filename-based, and silent on failure.

## Root cause: snapshot+poll race

Today, in `internal/sidecar/daemon.go`:

```go
// Run() — line ~98
preSnapshot = d.snapshotProjectFiles()         // (1) ReadDir of project dirs
master, cmd, err := StartProcessWithPTY(...)   // (2) fork claude
...
go d.detectClaudeSessionID(preSnapshot)        // (3) poll every 2s for 120s
                                               //     for a *.jsonl not in (1)
```

Failure modes the design admits:

- Claude opens its JSONL with `O_CREAT` between (1) and (3) returning. If
  the file appears in `preSnapshot`, detection ignores it forever and logs
  "could not detect" 120s later.
- If Claude writes to a temp file then renames, the rename event can be
  missed.
- Multiple jarvis sessions launched from the same `cwd` share the project
  dir; whichever sidecar polls first claims the file regardless of which
  Claude actually owns it.
- "Detection failed" produces no signal at attach time — the sidecar keeps
  running, the user keeps chatting, and only the next reboot reveals the
  problem (when jarvis launches fresh and pins a different file).

## Fix: receive the session id from Claude itself, via a SessionStart hook

Claude Code fires a `SessionStart` hook on every session start (cold launch,
resume, fork, post-compact). The hook payload is JSON on stdin and includes
`session_id`. By configuring this hook to call back into jarvis, we get the
canonical id pushed to us, deterministically, exactly once per session.

The hook script knows which jarvis session it belongs to via the
`JARVIS_SESSION_ID` environment variable that `manager.spawnSidecar` already
exports to the sidecar (and which Claude inherits through the PTY).

### Flow

```
claude (child of sidecar)
  └─ SessionStart fires
       └─ runs:  jarvis hook-relay SessionStart
            ├─ stdin: { "session_id": "<claude-uuid>", ... }
            ├─ env:   JARVIS_SESSION_ID=<jarvis-uuid>
            └─ opens ~/.jarvis/sockets/<jarvis-uuid>.sock
                 └─ sends Request{ Action: "set_session_id",
                                   SessionID: "<claude-uuid>" }
                      └─ sidecar updates session.yaml
```

No filesystem watching. No path coupling beyond what jarvis already owns.

### Why this also fixes the resume-fork drift

Each `claude --resume <id>` mints a brand-new JSONL whose first line is a
`last-prompt` referencing the prior transcript via `leafUuid`. Today jarvis
keeps the original id forever, so `--resume` always loads the *original*
transcript and never sees content written into intermediate forks. Because
SessionStart fires on every resume with the *new* forked id, jarvis will
naturally update its pointer to the latest fork — turning a long-standing
"Claude forgot last session" complaint into a non-issue.

## Implementation plan

### 1. Per-session `claude-settings.json` writer

`spawnSidecar` (or a helper called from it) writes a settings file under
`~/.jarvis/sessions/<id>/claude-settings.json` containing both the existing
`Notification` hook and the new `SessionStart` hook:

```json
{
  "hooks": {
    "SessionStart": [
      { "matcher": "", "hooks": [
        { "type": "command",
          "command": "/path/to/jarvis hook-relay SessionStart" }
      ]}
    ],
    "Notification": [
      { "matcher": "", "hooks": [
        { "type": "command",
          "command": "/path/to/jarvis hook-relay Notification" }
      ]}
    ]
  }
}
```

Pass to Claude via `--settings <path>`. Resolve `/path/to/jarvis` from
`os.Executable()` so dev builds and prod builds both work without
hard-coding `~/.local/bin/jarvis`.

### 2. `jarvis hook-relay` cobra subcommand

New file `cmd/jarvis/hook_relay.go`. Argument: the hook event name
(`SessionStart`, `Notification`, ...). Reads stdin JSON, reads
`JARVIS_SESSION_ID` from env, opens
`~/.jarvis/sockets/$JARVIS_SESSION_ID.sock`, and sends one Request through
the existing `protocol.Codec`.

For `SessionStart`:

```go
type sessionStartPayload struct {
    SessionID string `json:"session_id"`
    Source    string `json:"source"` // startup | resume | clear | compact
}
```

Send `Request{ Action: "set_session_id", SessionID: payload.SessionID }`.

For `Notification` (preserve current behavior): forward as today.

`hook-relay` exits 0 always — a hook failure must not block Claude. Errors
go to stderr.

### 3. Protocol extension

`internal/protocol/protocol.go`:

```go
type Request struct {
    ...
    SessionID string `json:"session_id,omitempty"` // for set_session_id
}
```

Add `Action: "set_session_id"` to the doc-comment.

### 4. Sidecar handler

`internal/sidecar/daemon.go`'s `acceptConnections` switch:

```go
case "set_session_id":
    if req.SessionID == "" { /* ignore */ break }
    if s, err := store.GetSession(d.cfg.SessionID); err == nil {
        if s.ClaudeSessionID != req.SessionID {
            s.ClaudeSessionID = req.SessionID
            s.UpdatedAt = time.Now()
            store.SaveSession(s)
            log.Printf("sidecar: set Claude session ID via hook: %s",
                       req.SessionID)
        }
    }
    codec.Send(protocol.Response{Event: "ok"})
```

### 5. Remove the polling path

In `Run()`:

- Delete the `preSnapshot = d.snapshotProjectFiles()` call.
- Delete the `go d.detectClaudeSessionID(preSnapshot)` branch.
- Keep `if d.cfg.ClaudeSessionID != "" { log.Printf("...already known...") }`
  just as a one-liner.
- Delete `snapshotProjectFiles`, `detectClaudeSessionID`, and the related
  helpers. `paths.ProjectDirs` stays — it's used elsewhere
  (`SessionIsValid`, `DeriveStatusFromJSONL`).

### 6. Backwards compatibility

- Existing sessions with a stored `claude_session_id` keep working — the
  `--resume` path is unchanged.
- Existing sessions whose stored id is stale will get the *correct* id
  pushed in by the next SessionStart hook, fixing themselves.
- The Notification hook today already calls `jarvis hook-relay Notification`
  in the binary — settings written by old jarvis will keep working because
  we're not removing that branch.

### 7. Watchdog (optional, recommended)

Keep a 60-second timer started in `Run()`: if no `set_session_id` arrives
within 60s and `cfg.ClaudeSessionID == ""`, log a single warning. This
preserves a regression signal without re-introducing the polling logic.

## Edge cases

- **Hook payload missing `session_id`.** Log and exit 0. The session will
  proceed without an id; same as today's "could not detect" but loud.
- **Hook fires multiple times** (resume → SessionStart again). Handler is
  idempotent: only persist if the new id differs from stored.
- **Race against attach.** If a client is already attached when the hook
  fires, no special handling — `acceptConnections` is concurrent.
- **Sidecar dead by the time the hook runs** (extremely unlikely — Claude
  is the sidecar's child). Hook just fails to dial the socket and logs.
- **Claude `/clear`**: produces a SessionStart with new id. Treat the same
  way as resume — update.
- **Multiple sidecars sharing project dir**: no longer a concern, because
  each Claude reports *its own* id back to its own sidecar via env-var-keyed
  socket path. Today's polling-based ambiguity disappears.

## Testing

- Unit-test `hook-relay` against an in-memory protocol pair (stdin →
  socket round-trip).
- Unit-test the sidecar handler: send `set_session_id`, assert
  `session.yaml` is updated; send a duplicate, assert no rewrite.
- Manual: spawn a new session, observe `session.yaml.claude_session_id`
  populated within ~1s (vs current "after first user message" / 120s
  worst case). Detach, reattach, force a `--resume`-induced fork, and
  verify the stored id moves to the new forked file.

## File-by-file summary

| File | Change |
|---|---|
| `cmd/jarvis/hook_relay.go` (new) | `hook-relay <event>` subcommand |
| `cmd/jarvis/main.go` | register `hookRelayCmd` |
| `internal/protocol/protocol.go` | add `SessionID` field; document `set_session_id` |
| `internal/session/manager.go` | write per-session settings file; pass `--settings` to claude |
| `internal/sidecar/daemon.go` | handle `set_session_id`; remove `snapshotProjectFiles` + `detectClaudeSessionID` + their callsites |

## Out of scope

- Migrating existing pre-fix sessions whose JSONL is currently orphaned.
  (One-off manual `session.yaml` edit, as already done for `ea1adaeb`.)
- Any UI for showing the current Claude id in the dashboard. The status
  command already prints it.
- Any change to `Resume()`'s `SessionIsValid` invalidation logic — that
  path is fine when the stored id is correct, which the hook guarantees.
