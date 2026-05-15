# Jarvis Architecture Guide

> Last updated: 2026-04-04 | Covers commit `837f8e2` (branch `refactor/readability-improvements`)

---

## 1. Overview

Jarvis is a session manager for Claude Code -- think **tmux, but for AI coding assistants**.

The problem it solves: Claude Code runs in a terminal and occupies that terminal for the duration of a conversation. If you want to run five Claude sessions in parallel -- one fixing a bug, one refactoring a module, one writing tests -- you need five terminal tabs, and if your SSH connection drops, they all die.

Jarvis decouples Claude Code sessions from your terminal. Each session runs inside a **sidecar daemon** (a separate process with its own PTY), and you **attach** and **detach** at will. When you detach, the sidecar keeps Claude running in the background. When you re-attach, the sidecar replays recent terminal output so you can see what happened while you were away. If the sidecar crashes or the machine reboots, Jarvis can **resume** the Claude conversation using Claude Code's built-in `--resume` flag.

The project is a Go monorepo producing two binaries:

- **`jarvis`** (`cmd/jarvis/main.go`) -- the CLI and TUI dashboard
- **`jarvis-sidecar`** (`cmd/sidecar/main.go`) -- the background daemon that owns the PTY

The TUI is built with [Bubble Tea](https://github.com/charmbracelet/bubbletea), sessions are stored as YAML files on disk, and all IPC between the CLI and sidecars happens over Unix domain sockets using newline-delimited JSON.

---

## 2. Architecture Diagram Description

This section describes the system architecture in enough detail to generate a visual diagram. All component names match the Go package and struct names used in the codebase.

### Components

**User terminal** -- the user's shell where `jarvis` runs.

**Jarvis CLI** (`cmd/jarvis/main.go`) -- the main binary. It has two personalities:
- **Dashboard mode** (default): launches a Bubble Tea full-screen TUI showing all sessions.
- **Command mode**: subcommands like `jarvis new`, `jarvis attach`, `jarvis ls`.

**Session Manager** (`internal/session/Manager`) -- orchestrates session lifecycle: spawn, attach, resume, status.

**TUI Dashboard** (`internal/tui/Dashboard`) -- the Bubble Tea model. Renders the session list, handles keyboard input, delegates actions to the Manager.

**Sidecar Daemon** (`internal/sidecar/Daemon`) -- one per session. Runs as an independent OS process. Owns the PTY, runs Claude Code inside it, listens on a Unix socket for commands.

**Claude Code process** -- the actual `claude` binary, running inside the sidecar's PTY.

**PTY** (`internal/sidecar/pty.go`) -- pseudo-terminal connecting Claude Code's stdin/stdout to the sidecar.

**Ring Buffer** (`internal/sidecar/RingBuffer`) -- circular buffer of the last 10,000 lines of PTY output, used for catch-up when a client attaches.

**Unix Socket** -- one per session, located at `~/.jarvis/sockets/<session-id>.sock`. Carries the JSON protocol between the CLI and the sidecar.

**Session Store** (`internal/store/`) -- YAML files on disk under `~/.jarvis/sessions/<id>/session.yaml`. Also stores folders under `~/.jarvis/folders/<id>.yaml`.

**Claude JSONL files** -- Claude Code's own conversation logs at `~/.claude/projects/<encoded-cwd>/<session-id>.jsonl`. Jarvis reads these to detect the Claude session ID and to derive status when the sidecar is dead.

### Relationships and Data Flow

```
User Terminal
    |
    v
Jarvis CLI (cmd/jarvis)
    |
    +---> TUI Dashboard (internal/tui)
    |         |
    |         +---> buildItemList() reads Session Store + pings sidecar sockets
    |         |
    |         +---> on Enter: returns attachMsg -> exits TUI
    |
    +---> Session Manager (internal/session)
              |
              +---> Spawn: forks jarvis-sidecar process, waits for socket
              |
              +---> Attach: connects to Unix socket, enters raw terminal mode
              |         |
              |         +---> sends: attach, send_input, resize, detach
              |         +---> receives: buffer, output, session_ended
              |
              +---> Resume: cleans stale socket, rebuilds claude --resume args,
              |             calls Spawn again
              |
              +---> GetStatus: pings socket or falls back to JSONL derivation

Sidecar Daemon (cmd/sidecar -> internal/sidecar)
    |
    +---> PTY master fd <----> Claude Code process
    |
    +---> Ring Buffer (10,000 lines)
    |
    +---> Unix Socket listener
    |         |
    |         +---> accepts connections from Jarvis CLI
    |         +---> handles: ping, get_status, attach, detach, send_input, resize, get_buffer
    |
    +---> State detection (internal/sidecar/status.go)
    |         reads PTY output, matches regex patterns for approval prompts / idle
    |
    +---> Periodic state persistence (every 5s -> Session Store)
    |
    +---> Claude session ID detection (polls for new .jsonl files)

Session Store (~/.jarvis/)
    +---> sessions/<id>/session.yaml
    +---> sessions/<id>/sidecar.log
    +---> folders/<id>.yaml
    +---> sockets/<id>.sock
    +---> config.yaml
    +---> dashboard_state.yaml
```

### Layout Instructions for Diagram Generation

- Place the User Terminal at the top.
- Below it, the Jarvis CLI with two branches: TUI Dashboard (left) and Session Manager (right).
- Below the Session Manager, show the Sidecar Daemon as a large box containing: PTY, Ring Buffer, Socket Listener, State Detector.
- Inside the Sidecar box, show Claude Code connected to the PTY.
- On the right side, show the Session Store (disk) with bidirectional arrows from both the Manager and the Sidecar.
- Show the Unix Socket as a connection between the Manager/CLI and the Sidecar's Socket Listener.
- At the bottom-right, show Claude JSONL files with a dashed arrow from the Session Manager (read-only fallback).

---

## 3. Package Map

Every Go package in the project, what it does, and its key files.

| Package | Path | Lines | Purpose | Key Files |
|---|---|---|---|---|
| **main (CLI)** | `cmd/jarvis/` | 396 | Entry point for the `jarvis` binary. Defines all cobra commands: `new`, `chat`, `attach`, `ls`, `status`, `done`, `rm`, `rename`, `init`. Contains the dashboard loop. | `main.go` -- all commands and `runDashboard()` |
| **main (sidecar)** | `cmd/sidecar/` | 49 | Entry point for the `jarvis-sidecar` binary. Parses flags, sets up logging, creates a `Daemon`, and calls `Run()`. | `main.go` |
| **config** | `internal/config/` | 56 | Loads `~/.jarvis/config.yaml`. Provides `RepoPath()` (git root detection) and `EffectiveWorktreeBaseDir()`. | `config.go` |
| **model** | `internal/model/` | 81 | Pure data types with no logic: `Session`, `Folder`, `SidecarInfo`, `ChildRef`, plus the `SessionStatus` and `SidecarState` enums. Also `NewID()` for generating 8-char hex IDs. | `session.go` -- structs and enums; `id.go` -- ID generation |
| **paths** | `internal/paths/` | 48 | Encodes CWD paths the way Claude Code does (`/home/user/repo` -> `-home-user-repo`) and returns candidate project directories for JSONL lookup. | `paths.go` |
| **protocol** | `internal/protocol/` | 113 | Defines the IPC wire format: `Request` and `Response` structs, plus the `Codec` type that reads/writes newline-delimited JSON over a `net.Conn`. | `messages.go` -- struct definitions; `codec.go` -- encoder/decoder |
| **session** | `internal/session/` | 736 | The brain of Jarvis. `Manager` handles spawn, attach, resume, and status. `attach.go` implements raw-mode PTY passthrough. `resume.go` handles sidecar health checks and session recovery. `jsonl.go` reads Claude JSONL files for session detection and status derivation. | `manager.go:49` -- `Spawn()`; `manager.go:161` -- `Attach()`; `manager.go:191` -- `Resume()`; `attach.go:22` -- `Attach()` (socket-level); `resume.go:56` -- `RecoverAllSessions()`; `jsonl.go:100` -- `DeriveStatusFromJSONL()` |
| **sidecar** | `internal/sidecar/` | 742 | The daemon that runs in the background. `Daemon.Run()` starts the PTY, socket listener, state detection, and persistence loops. `RingBuffer` stores recent output. `status.go` uses regex to detect approval prompts and idle states. `pty.go` wraps `creack/pty`. | `daemon.go:72` -- `Run()`; `daemon.go:174` -- `readPTY()`; `daemon.go:232` -- `handleConnection()`; `ringbuf.go` -- circular buffer; `status.go:25` -- `DetectState()`; `pty.go:13` -- `StartProcessWithPTY()` |
| **store** | `internal/store/` | 310 | Filesystem persistence. `SaveSession`/`GetSession`/`ListSessions` for sessions, same pattern for folders. `WriteAtomic` does temp-file-then-rename. `JarvisHome()` resolves `$JARVIS_HOME` or defaults to `~/.jarvis`. | `session.go:16` -- `JarvisHome()`; `session.go:31` -- `SaveSession()`; `atomic.go:9` -- `WriteAtomic()` |
| **tui** | `internal/tui/` | 1312 | The Bubble Tea dashboard. `Dashboard` is the model with Init/Update/View. `builder.go` flattens the folder/session tree into a list. `commands.go` has async business logic. `view.go` renders everything. `styles.go` defines Lipgloss colors. | `dashboard.go:90` -- `NewDashboard()`; `dashboard.go:193` -- `Update()`; `builder.go:31` -- `buildItemList()`; `view.go:20` -- `View()` |
| **ui** | `internal/ui/` | 53 | Shared formatting helpers: `FormatAge()` (relative timestamps), `Truncate()`, `StatusIcon()`. Used by both the CLI and the TUI. | `format.go` |
| **worktree** | `internal/worktree/` | 118 | Git worktree and branch creation for `jarvis init`. `Slugify()` converts titles to branch-safe names. `CreateBranchAndWorktree()` tries `git stack` first, falls back to plain `git worktree add`. | `worktree.go:28` -- `Slugify()`; `worktree.go:83` -- `CreateBranchAndWorktree()` |
| **testdata** | `testdata/` | 74 | `mock_claude.go` -- a fake Claude Code that reads stdin, echoes input, simulates approval prompts, and can crash on command. Built and used by integration tests. | `mock_claude.go` |
| **integration** | `./` | 809 | `integration_test.go` -- builds real binaries, exercises full lifecycle over Unix sockets. | `integration_test.go:36` -- `TestMain()` builds binaries |

**Total: ~4,970 lines of Go.**

---

## 4. Data Flow Walkthrough

### 4.1 Creating a New Session (`jarvis new "fix bug"`)

1. **CLI entry** (`cmd/jarvis/main.go:68`): The `newCmd` cobra handler joins args into `name = "fix bug"`, loads config, determines `cwd` from `cfg.RepoPath()` or `os.Getwd()`.

2. **Manager.Spawn** (`internal/session/manager.go:49`):
   - Generates an 8-char hex ID via `model.NewID()` (`internal/model/id.go:9`).
   - Creates a `model.Session` struct with `Status: active`, `LaunchDir: cwd` (and empty `WorktreeDir` until `jarvis init`).
   - Calls `store.SaveSession()` to write `~/.jarvis/sessions/<id>/session.yaml`.
   - Calls `m.spawnSidecar(sess, "claude")`.

3. **Manager.spawnSidecar** (`internal/session/manager.go:85`):
   - Finds the `jarvis-sidecar` binary via `findSidecarBinary()` (checks next to executable, then PATH).
   - Gets terminal size from `os.Stdout`.
   - Launches `jarvis-sidecar --session-id <id> --cwd <cwd> --claude-cmd claude --cols 80 --rows 24` as a detached process (`Setsid: true`).
   - Releases the child process handle (`cmd.Process.Release()`).
   - Polls `sidecar.SocketPath(id)` up to 50 times (5 seconds) using `PingSidecar()`.
   - Once the socket responds to ping, updates `sess.Sidecar` with PID, socket path, start time.
   - Saves the session again.

4. **Sidecar daemon starts** (`cmd/sidecar/main.go:13` -> `internal/sidecar/daemon.go:72`):
   - `Daemon.Run()` creates the socket directory, removes any stale socket file.
   - Takes a **pre-snapshot** of existing JSONL files in Claude's project directories (to detect the new one later).
   - Calls `StartProcessWithPTY("claude", cwd, env, cols, rows)` (`internal/sidecar/pty.go:13`), which uses `creack/pty` to create a pseudo-terminal and start the `claude` process inside it.
   - Spawns five goroutines: `readPTY`, `acceptConnections`, `persistStateLoop`, `idleDetectionLoop`, and `detectClaudeSessionID`.
   - Blocks on `cmd.Wait()` (waits for Claude to exit).

5. **Back in the CLI** (`cmd/jarvis/main.go:89`): After `Spawn` returns, calls `mgr.Attach(sess.ID)` which connects to the socket and enters raw PTY passthrough (see next section).

### 4.2 Attaching to a Session (pressing Enter in the dashboard)

1. **Dashboard key handler** (`internal/tui/dashboard.go:285`): When the user presses Enter on a session, the dashboard creates an `attachMsg{sessionID: item.ID}`, saves viewport state to disk, and returns `tea.Quit`.

2. **Dashboard loop** (`cmd/jarvis/main.go:33`): The `runDashboard()` loop receives the quit, extracts `d.AttachSessionID()`, and calls `mgr.Attach(sessionID)`.

3. **Manager.Attach** (`internal/session/manager.go:161`):
   - Loads the session from disk.
   - Bumps `UpdatedAt` to now (so recently-attached sessions sort to the top in the dashboard).
   - Computes the socket path via `sidecar.SocketPath(sess.ID)`.
   - Calls `PingSidecar(socketPath)` (`internal/session/resume.go:16`) -- connects, sends `{"action":"ping"}`, expects `{"event":"pong"}`.
   - If the sidecar is dead, calls `m.Resume(sess)` to restart it (see section 4.4).
   - Calls `Attach(socketPath)` (`internal/session/attach.go:22`).

4. **Attach (socket level)** (`internal/session/attach.go:22`):
   - Connects to the Unix socket with a 5-second timeout.
   - Sends a `resize` request with the current terminal dimensions.
   - Sends an `attach` request.
   - Puts the terminal in **raw mode** via `term.MakeRaw(stdin)`.
   - Ignores `SIGQUIT` (because Ctrl-\ sends SIGQUIT, and we use it for detach).
   - Starts a goroutine to forward `SIGWINCH` to the sidecar as resize requests.
   - Starts a goroutine reading from the socket: decodes base64 `output`/`buffer` events and writes them to stdout. On `session_ended`, cancels the context.
   - Creates an **os.Pipe** (the pipe trick -- see Gotchas) and starts two goroutines: one copies `os.Stdin` -> pipe, the other reads pipe -> sidecar. The second goroutine scans every byte for the detach character `0x1c` (Ctrl-\).

5. **Sidecar side** (`internal/sidecar/daemon.go:264`): The `handleConnection` handler receives the `attach` action:
   - Closes any previously attached client.
   - Sets this connection as the attached client.
   - Sends the entire ring buffer content as a `buffer` event (base64-encoded). This is the **catch-up replay**.
   - From then on, `readPTY()` (`daemon.go:174`) forwards every chunk of PTY output to this client as `output` events.

6. **Blocked on ctx.Done()**: The attach function blocks until either the user detaches (Ctrl-\), the session ends, or the connection breaks.

### 4.3 Detaching (Ctrl-\) and Returning to the Dashboard

1. **Detach detection** (`internal/session/attach.go:135`): The stdin reader goroutine finds byte `0x1c` in the input stream. It sends `{"action":"detach"}` to the sidecar and calls `cancel()` on the context.

2. **Sidecar side** (`daemon.go:289`): The `detach` handler clears the attached client reference and sends back a `{"event":"status","state":"detached"}` response. The sidecar continues running -- Claude Code is unaffected.

3. **Cleanup** (`attach.go:149`):
   - Restores the terminal from raw mode (`term.Restore`).
   - Closes the pipe (both read and write ends) to unblock the stdin reader goroutine.
   - Waits up to 500ms for the stdin goroutine to exit.
   - Stops signal handlers, closes the socket connection.
   - Returns nil.

4. **Back in the dashboard loop** (`cmd/jarvis/main.go:59`): After `mgr.Attach` returns, prints "Detached. Returning to dashboard...", sleeps 100ms (to let the terminal settle after raw-mode restoration), and loops back to create a new `tea.NewProgram(dashboard)`.

5. The new dashboard instance calls `session.RecoverAllSessions()` (`internal/session/resume.go:56`) to mark any newly-dead sidecars as suspended, then rebuilds the item list.

### 4.4 Resuming After a Crash/Reboot

1. **Trigger**: The user tries to attach to a session (Enter in dashboard, or `jarvis attach <name>`). `Manager.Attach` pings the socket and gets no response.

2. **Manager.Resume** (`internal/session/manager.go:191`):
   - Removes the stale socket file.
   - Uses **`LaunchDir`** for the sidecar `--cwd` (where `claude --resume` finds JSONL) and **`WorkspaceDir()`** (`WorktreeDir` if set, else `LaunchDir`) for the user-facing workspace. No temporary field swapping.
   - Checks the stored `ClaudeSessionID`. If it exists, validates it by calling `SessionIsValid()` against both `LaunchDir` and `WorkspaceDir()` (JSONL may resolve under either). If invalid, logs a warning and starts fresh.
   - If valid, builds: `claude --resume <session-id>`. If `LaunchDir != WorkspaceDir()`, appends `--append-system-prompt 'Your working directory is <worktree-path>'`.
   - If no valid session ID, falls back to plain `claude` (no resume).
   - Calls `spawnSidecar`, which always passes `sess.LaunchDir` to the sidecar.

3. **RecoverAllSessions** (`internal/session/resume.go:56`): Called at startup and when returning to the dashboard. Scans all sessions with `Status: active`, pings each sidecar, and marks dead ones as `Status: suspended`. For dead sidecars, tries to derive the last state from the JSONL file using `DeriveStatusFromSession`.

---

## 5. Key Design Decisions

### Why a separate sidecar process (not goroutines)

The fundamental requirement is that Claude Code sessions survive the jarvis CLI exiting. If the user quits the dashboard, closes their terminal, or their SSH drops, the Claude sessions must keep running. Goroutines die with the process; separate processes don't. The sidecar is launched with `Setsid: true` so it gets its own session and won't receive the parent's SIGHUP.

### Why Unix sockets (not TCP, not pipes)

- **Not TCP**: TCP would require port allocation, risk port conflicts, and expose sessions to network access. Unix sockets are local-only and their access is controlled by filesystem permissions (`0600`).
- **Not pipes**: Pipes are unidirectional and die when either end closes. The sidecar needs to accept connections from *multiple* clients over time (dashboard polling status, then attach, then detach, then attach again). A socket listener handles this naturally.
- Unix sockets also give clean lifecycle: the socket file appears when the sidecar starts and disappears when it exits, making liveness checks trivial.

### Why base64 encoding for PTY output

PTY output contains arbitrary bytes -- ANSI escape sequences, binary data, null bytes, control characters. The IPC protocol is newline-delimited JSON. If raw bytes were embedded in JSON strings, any embedded newline or invalid UTF-8 would break the framing. Base64 encoding is a clean, lossless way to tunnel arbitrary bytes through a JSON text field. The overhead is ~33%, which is negligible for terminal output rates.

### Why atomic writes for YAML

Both the jarvis CLI and the sidecar daemon write to the same `session.yaml` file. If either writes a partial file (e.g., gets killed mid-write), the other will read corrupt YAML and crash. `WriteAtomic` (`internal/store/atomic.go:9`) writes to a temp file in the same directory, then does an `os.Rename`, which is atomic on all Unix filesystems. This guarantees readers always see a complete file.

### Why two-level state (SessionStatus vs SidecarState)

`SessionStatus` is the **lifecycle** state of a session: `queued`, `active`, `suspended`, `done`, `archived`. It persists across sidecar restarts and is set explicitly by user actions or recovery logic.

`SidecarState` is the **real-time operational** state of a running sidecar: `working`, `waiting_for_approval`, `idle`, `exited`. It changes every few seconds based on PTY output analysis.

These are orthogonal. A session can be `active` (lifecycle) while the sidecar state is `idle` (Claude is waiting for input). Or `suspended` (lifecycle, sidecar dead) with a last-known state of `waiting_for_approval`. Conflating them into one enum would make the state machine much harder to reason about.

### Why a ring buffer for catch-up

When you attach to a session, you want to see what happened recently -- not a blank screen. The ring buffer (`internal/sidecar/ringbuf.go`) stores the last 10,000 lines of PTY output. On attach, the sidecar sends the entire buffer as a single `buffer` event. This is much simpler than alternatives like:

- Logging all output to a file and seeking (requires managing a growing file)
- Asking Claude to repeat itself (not possible)
- Using tmux's scrollback (adds a dependency)

The ring buffer is fixed-size, thread-safe, and has zero disk I/O.

### Why JSONL derivation as a fallback

When the sidecar is dead (crashed, rebooted), Jarvis can't query it for status. But Claude Code writes a JSONL file with every conversation turn. `DeriveStatusFromJSONL` (`internal/session/jsonl.go:100`) reads the tail of this file to infer what Claude was doing when it died: if the last assistant message had `stop_reason: tool_use` with no corresponding user response, Claude was waiting for approval. If `stop_reason: end_turn`, it was idle. This gives the dashboard something meaningful to display for suspended sessions.

---

## 6. State Machines

### SessionStatus (Lifecycle)

Defined in `internal/model/session.go:7`.

```
                     +-----------+
        Spawn()      |           |
     +-------------->|  active   |<--------+
     |               |           |         |
     |               +-----+-----+         |
     |                     |               |
     |          sidecar dies / crash        |  Resume()
     |          RecoverAllSessions()        |  (user attaches to
     |                     |               |   suspended session)
     |                     v               |
     |               +-----------+         |
     |               |           |---------+
     |               | suspended |
     |               |           |
     |               +-----+-----+
     |                     |
     |             user runs "done"
     |                     |
     |                     v
     |               +-----------+
     |               |           |
     |               |   done    |
     |               |           |
     |               +-----------+
     |                     |
     |             (future: archive)
     |                     v
     |               +-----------+
     |               |           |
     |               | archived  |
     |               |           |
     |               +-----------+
```

**Transitions:**

| From | To | Trigger | Code Location |
|---|---|---|---|
| (none) | `active` | `Manager.Spawn()` creates a new session | `manager.go:60` |
| `active` | `suspended` | Sidecar process exits (clean or crash) | `daemon.go:142`; `resume.go:83` |
| `suspended` | `active` | `Manager.Resume()` restarts the sidecar | `manager.go:245` |
| `active`/`suspended` | `done` | User runs `jarvis done <name>` or presses `d` in dashboard | `cmd/jarvis/main.go:243`; `tui/commands.go:127` |
| (any) | `archived` | (Not yet implemented; the enum value exists) | `model/session.go:12` |

Note: `queued` is defined but currently unused. It's reserved for a future queue system where sessions wait for a slot.

### SidecarState (Runtime)

Defined in `internal/model/session.go:16`.

```
                start
                  |
                  v
            +-----------+
            |           |<-------- new PTY output arrives
  +-------->|  working  |         (timeSinceLastOutput < 5s)
  |         |           |
  |         +-----+-----+
  |               |
  |     +---------+---------+
  |     |                   |
  |     v                   v
  | +-----------+    +-------------------+
  | |           |    |                   |
  | |   idle    |    | waiting_for_      |
  | |           |    | approval          |
  | +-----------+    +-------------------+
  |     |                   |
  |     +------- PTY output resumes ----+
  |                                      |
  +--------------------------------------+
                  |
        Claude process exits
                  |
                  v
            +-----------+
            |           |
            |  exited   | (terminal state)
            |           |
            +-----------+
```

**Transitions:**

| From | To | Trigger | Code Location |
|---|---|---|---|
| (start) | `working` | PTY process started | `daemon.go:95` |
| `working` | `idle` | No PTY output for 5 seconds, OR `>` prompt pattern detected | `daemon.go:400`; `status.go:33` |
| `working` | `waiting_for_approval` | Approval pattern matched (`Allow Bash? (y/n)`, etc.) | `status.go:27` |
| `idle` | `working` | New PTY output arrives with `timeSinceLastOutput < 5s` | `status.go:41` |
| `waiting_for_approval` | `working` | New PTY output arrives (user approved or denied) | `status.go:41` |
| (any) | `exited` | `cmd.Wait()` returns (Claude process exited) | `daemon.go:136` |

The state detection happens in `DetectState()` (`internal/sidecar/status.go:25`), called from `readPTY()` on every chunk of output. It uses regex patterns for approval prompts and a 5-second idle timeout.

---

## 7. Persistence Model

Every file that Jarvis writes to disk:

| File | Format | Written By | When | Purpose |
|---|---|---|---|---|
| `~/.jarvis/sessions/<id>/session.yaml` | YAML | `store.SaveSession()` | On create, attach, detach, state change, resume, every 5s by sidecar | Canonical session record: ID, name, status, `launch_dir`, optional `worktree_dir`, Claude session ID, sidecar info, timestamps (legacy `cwd` / `original_cwd` are migrated on load) |
| `~/.jarvis/sessions/<id>/sidecar.log` | Plain text | `log.SetOutput()` in `cmd/sidecar/main.go:29` | Continuously while sidecar runs | Debug log from the sidecar daemon (startup, session ID detection, errors) |
| `~/.jarvis/folders/<id>.yaml` | YAML | `store.SaveFolder()` | On folder create, rename, add/remove child, mark done | Folder metadata and children list |
| `~/.jarvis/sockets/<id>.sock` | Unix socket | `net.Listen("unix", ...)` in `daemon.go:108` | Created on sidecar start, removed on clean exit | IPC endpoint between CLI and sidecar |
| `~/.jarvis/config.yaml` | YAML | User (manual) | User-edited | Optional config: `worktree_base_dir` |
| `~/.jarvis/dashboard_state.yaml` | YAML | `Dashboard.SaveState()` in `tui/dashboard.go:146` | On quit, on attach (before exiting TUI) | Persists expand/collapse state, cursor position, scroll offset across restarts |
| `~/.claude/projects/<encoded-cwd>/<session-id>.jsonl` | JSONL | Claude Code itself (NOT Jarvis) | During Claude conversations | Jarvis reads this (never writes) to detect Claude session IDs and derive status |

The `JARVIS_HOME` environment variable overrides the `~/.jarvis` base path. This is heavily used in tests to isolate test data.

All YAML files are written atomically via `store.WriteAtomic()` (`internal/store/atomic.go:9`): write to a temp file in the same directory, then `os.Rename`.

---

## 8. Gotchas and Sharp Edges

### LaunchDir vs WorktreeDir

`internal/model/session.go`, `internal/session/manager.go`

`LaunchDir` is set at spawn and is where the sidecar runs Claude (`--cwd`); Claude's JSONL lives under `~/.claude/projects/<encoded-launch-dir>/`. `WorktreeDir` is optional: after `jarvis init`, it holds the git worktree path where the user edits, while `LaunchDir` stays on the original repo path. `Resume` always starts the sidecar in `LaunchDir` and, when `WorktreeDir` differs, appends `--append-system-prompt` so Claude `cd`s to the worktree. Old session files used `cwd` + `original_cwd`; `store.GetSession` migrates them into `launch_dir` + `worktree_dir` via `NormalizePathFields`.

### The JSONL detection race condition and pre-snapshot

`internal/sidecar/daemon.go:83-86, 349-388`

Claude Code creates its JSONL file only after the first user interaction, not immediately on startup. The sidecar detects it by polling for **new** `.jsonl` files in the project directories. But if we take the snapshot *after* starting Claude, there's a race: Claude might create the file between `StartProcessWithPTY` and `snapshotProjectFiles`, and we'd think it was pre-existing.

The fix: `snapshotProjectFiles()` is called **before** `StartProcessWithPTY()` (line 83-86). Any `.jsonl` file that appears after the snapshot must be the new one. The detection goroutine polls every 2 seconds for up to 120 seconds.

### The pipe trick in attach.go for goroutine cleanup

`internal/session/attach.go:96-117`

The stdin reader goroutine (`os.Stdin.Read`) blocks forever and cannot be interrupted -- there's no way to cancel a blocking read on stdin in Go. After detach, if this goroutine is still alive, it competes with Bubble Tea for stdin when the dashboard restarts.

The solution: instead of reading stdin directly, the code creates an `os.Pipe()`. One goroutine copies `stdin -> pipeW`, another reads `pipeR -> sidecar`. On detach, closing `pipeW` and `pipeR` causes the second goroutine to get an EOF and exit cleanly. The first goroutine (reading stdin) is still blocked, but it writes to a closed pipe, which causes it to exit too. Without this, users would see corrupted input after returning to the dashboard.

### SocketPath using JarvisHome

`internal/sidecar/daemon.go:67`

`SocketPath()` uses `store.JarvisHome()` to compute the socket directory. This means the socket path depends on the `JARVIS_HOME` environment variable, which is set when the sidecar starts. If `JARVIS_HOME` changes between sidecar start and CLI usage, the CLI won't find the socket. In practice this only matters in tests (which set `JARVIS_HOME` per-test). The recent bugfix (commit `de173c2`) ensured consistent `JARVIS_HOME` usage.

### The `__done__` virtual folder in the TUI

The flat item list contains one virtual item that isn't a real session or folder:

- `__done__` (ID: `"__done__"`): A virtual folder at the bottom that contains all done sessions and done folders. It's collapsed by default. The delete and mark-done handlers have special guards to avoid operating on it (`item.ID != "__done__"`).

If you add new virtual items, you need to update the action guards.

### Stable order

The dashboard sorts active groups (folders + unfiled sessions) and folder children by `CreatedAt` (newest first), not `UpdatedAt`. Once a session/folder is created, its position is fixed; activity does not reorder the list. Done sessions are hoisted to the `__done__` virtual folder regardless of which folder they were created in, so an active folder's children list shrinks when a child is marked done. Search still iterates the full flat list, so done items remain searchable.

---

## 9. Code Pointers

Quick-reference table: "If you want to change X, look at Y."

| Task | Where to Look |
|---|---|
| Add a new CLI command | `cmd/jarvis/main.go` -- add a `cobra.Command` and register in `init()` at line 392 |
| Change how sessions are stored | `internal/store/session.go` and `internal/model/session.go` |
| Add a new field to the Session struct | `internal/model/session.go:31` (struct), `internal/store/session.go` (auto via YAML tags) |
| Change the IPC protocol | `internal/protocol/messages.go` (add fields to Request/Response), then handle in `daemon.go:232` |
| Add a new sidecar command | `internal/sidecar/daemon.go:250` -- add a case in the `switch req.Action` block |
| Change how state is detected from PTY output | `internal/sidecar/status.go` -- modify `DetectState()` and its regex patterns |
| Change the idle timeout | `internal/sidecar/status.go:22` -- `const idleTimeout = 5 * time.Second` |
| Change the ring buffer size | `internal/sidecar/daemon.go:57` -- `NewRingBuffer(10000)` |
| Change the state persistence interval | `internal/sidecar/daemon.go:408` -- `time.NewTicker(5 * time.Second)` |
| Change how the dashboard looks | `internal/tui/view.go` (layout), `internal/tui/styles.go` (colors) |
| Change dashboard keyboard shortcuts | `internal/tui/dashboard.go:248` -- `handleDashboardKey()` |
| Change how the item list is built | `internal/tui/builder.go:31` -- `buildItemList()` |
| Change the dashboard refresh interval | `internal/tui/dashboard.go:179` -- `5*time.Second` in `tickEvery()` |
| Change how resume works | `internal/session/manager.go:191` -- `Resume()` |
| Change how Claude session IDs are detected | `internal/sidecar/daemon.go:349` -- `detectClaudeSessionID()` |
| Change how JSONL status derivation works | `internal/session/jsonl.go:100` -- `DeriveStatusFromJSONL()` |
| Change worktree/branch creation | `internal/worktree/worktree.go:83` -- `CreateBranchAndWorktree()` |
| Change the slug generation for branch names | `internal/worktree/worktree.go:28` -- `Slugify()` |
| Add a new integration test | `integration_test.go` -- follow the pattern of existing `Test*` functions; use `testEnv(t)` for isolation |
| Change how `JARVIS_HOME` is resolved | `internal/store/session.go:16` -- `JarvisHome()` |

---

## 10. Testing

### Unit Tests

Run all unit tests:

```bash
go test ./...
```

Unit tests exist for:

- **`internal/protocol/`** (`codec_test.go`, 60 lines): Tests JSON codec round-trip over `net.Pipe`, including request/response and input forwarding.
- **`internal/sidecar/`** (`ringbuf_test.go`, 77 lines): Tests ring buffer basics, wraparound, partial lines, empty state, and `Bytes()` output.
- **`internal/sidecar/`** (`daemon_test.go`, 102 lines): Tests daemon ping/pong, status query, and input echo using a real bash command as a mock.
- **`internal/store/`** (`session_test.go`, 117 lines): Tests session CRUD, listing with sort order, filtering by status, and deletion.

### Integration Tests

The integration tests (`integration_test.go`, 809 lines) are the most important tests. They build **real binaries** and exercise the full system:

```bash
go test -v -count=1 -timeout 120s ./...
```

`TestMain` (`integration_test.go:36`) builds three binaries into a temp directory:
1. `jarvis-sidecar` (the real daemon)
2. `claude` (the mock, from `testdata/mock_claude.go`)
3. `jarvis` (the real CLI)

Each test uses `testEnv(t)` which sets `JARVIS_HOME` to a temp dir and puts the test binaries first on `PATH`.

**What the integration tests cover:**

| Test | What It Verifies |
|---|---|
| `TestSidecarLifecycle` | Full flow: spawn, ping, status check, attach, send input, receive output, process exit with `session_ended` event |
| `TestSidecarApprovalDetection` | Sidecar detects `Allow Bash? (y/n)` pattern and transitions to `waiting_for_approval`; answering clears the state |
| `TestSidecarCrashAndRecovery` | Kill sidecar, run `RecoverAllSessions()`, verify session marked as `suspended` with `Sidecar: nil` |
| `TestMultipleSessions` | 3 concurrent sidecars, each independently pingable, each echoes unique input |
| `TestSessionStatePersistence` | After 6 seconds, verify `LastKnownState` and `LastActivityAt` are written to disk by the persist loop |
| `TestGetBuffer` | `get_buffer` command returns mock_claude's startup banner |
| `TestFindSessionByName` | Exact match, prefix match, case-insensitive match, and not-found error |
| `TestCLILs` | Runs `jarvis ls` as a subprocess, checks output contains session names |
| `TestCLIDoneAndRm` | `jarvis done` marks session done; `jarvis rm` deletes it from disk |
| `TestCLIStatus` | `jarvis status <name>` prints correct session name, ID, and launch directory |
| `TestSidecarProcessExitMarksSessionSuspended` | When mock_claude exits cleanly, session.yaml is updated to `suspended` with `LastKnownState: exited` |
| `TestSidecarCrashExitCode` | `mock_claude` `crash` command (exit 1) produces non-zero exit code in `session_ended` event and persists `exited` state |

### What's NOT Tested

- The TUI dashboard (Bubble Tea rendering and key handling) -- no automated tests
- The `jarvis init` / worktree creation flow
- The resume flow with `claude --resume` (would require a real Claude binary)
- Edge cases around `LaunchDir` / `WorktreeDir` migration from legacy YAML
- The JSONL status derivation (tested indirectly via `RecoverAllSessions`)
- Folder operations (create, nest, delete recursively)
