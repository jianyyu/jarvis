# Multi-agent jarvis: process-level agent selection (Claude / Isaac)

Date: 2026-07-16
Status: approved

## Goal

Let the user choose which LLM agent CLI a jarvis session drives, by launching
jarvis under a different binary name:

- `jarvis`  → Claude Code (`claude`)
- `ijarvis` → Isaac (`isaac`), a Claude Code wrapper

Isaac shares Claude Code's entire CLI contract (`--settings` hook injection,
`--resume <uuid>`, `--append-system-prompt`, `-p --output-format json
--fork-session`) and reads/writes the same session transcript. The only
difference from jarvis's perspective is the executable name.

Codex is explicitly out of scope for now.

## Design principle: agent is a property of the process, not the session

The agent is resolved **once** from `os.Args[0]` when the process starts.
Everything that process does — creating sessions, chats, and resuming *any*
existing session — uses that agent's executable. There is **no** per-session
agent field.

Consequence (intended): a session created under `jarvis` and later resumed
under `ijarvis` relaunches with `isaac`. This is safe because the two share
the transcript and CLI contract.

## Components

### `internal/agent` (new package)

```go
type Agent struct {
    Name string // "claude" | "isaac"
    Exec string // executable name on PATH
}

func FromArgv0(arg0 string) Agent // basename(arg0): "ijarvis" → isaac, else → claude
func Current() Agent              // FromArgv0(os.Args[0]), memoized
```

`claude` is the default for any unrecognized launch name (preserves existing
behavior).

### `session.Manager`

Carries an `Agent` set at construction time from `agent.Current()`.

- `Spawn(name, cwd)` — drop the `claudeArgs []string` parameter (every caller
  passes the same `[]string{"claude"}` literal today). Internally builds the
  command from `m.agent.Exec`.
- `Resume()` — replace the literal `"claude"` in the command with
  `m.agent.Exec`. All Claude flags (`--settings`, `--resume`,
  `--append-system-prompt`) are kept verbatim; they are valid for Isaac.

### `internal/autorename`

`ClaudeGenerator` currently hardcodes `exec.CommandContext(ctx, "claude", ...)`.
Add an `Exec string` field (populated from the process agent, defaulting to
`"claude"` when empty) and use it. Runs inside the TUI process, so it inherits
the same agent.

### Entry points

- `cmd/jarvis/main.go` — `NewManager` reads the process agent internally, so
  `new` / `chat` need no per-command plumbing.
- `internal/tui/dashboard.go` — the `exec.LookPath` availability check uses the
  agent exec so the "CLI unavailable" banner is correct for `ijarvis`. `[n]ew`
  and `[c]hat` flow through the same `Manager`.

### Call sites updated for the new `Spawn` signature

`cmd/jarvis/main.go` (new, chat), `internal/tui/commands.go` (createSession,
createChat). Watcher daemons (`internal/watch/*`) run under the separate
`jarvis-watch` binary and stay on Claude — untouched.

## Out of scope

- Codex support.
- Per-session agent persistence.
- Watcher daemons choosing an agent.
- `jarvis-watch` itself: its `~/.zshrc` auto-start is disabled separately (not a
  code change in this repo).

## Build / install

- `make build` is unchanged (`jarvis` + `jarvis-sidecar`).
- `make install` copies both binaries, then creates an `ijarvis` symlink to
  `jarvis` in the install dir (`ln -sf jarvis ijarvis`).

## Testing

- Unit test `agent.FromArgv0` for `jarvis`, `ijarvis`, absolute paths, and
  unknown names (→ claude).
- Existing `session` / `autorename` tests updated for the new signatures.
- Manual verification: build, symlink, launch `ijarvis`, confirm a new session
  spawns an `isaac` process (via the spawned command / process table).
