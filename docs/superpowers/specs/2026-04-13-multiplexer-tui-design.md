# Multiplexer TUI Design

**Date:** 2026-04-13
**Status:** Approved
**Goal:** Replace the dashboard + raw-PTY-attach cycle with an always-on multiplexer TUI that has a persistent sidebar and embedded terminal pane, eliminating the need for Ctrl+\ detach/reattach.

## Motivation

The current Jarvis UX requires exiting Bubble Tea to attach to a session (raw PTY passthrough), then pressing Ctrl+\ to detach back to the dashboard. This makes session switching slow and disorienting. Users want a tmux-like experience: a sidebar listing all sessions that's always visible, with instant switching between sessions in the main pane.

## Architecture

### High-Level

Bubble Tea runs continuously as the sole owner of the terminal. It never exits for attach/detach. Session output is rendered via a virtual terminal emulator (`charmbracelet/x/vt`) embedded in the main pane, rather than raw PTY passthrough.

```
jarvis TUI (Bubble Tea) -- always running
  +-- Sidebar: session tree (folders, status icons, navigation)
  +-- TermPane: VT emulator rendering active session's PTY output
  |     +-- Input forwarding: keystrokes -> sidecar socket -> PTY
  |     +-- Output streaming: PTY -> sidecar socket -> VT emulator -> View()
  +-- StatusBar: active session name, keybind hints
```

### Key Dependency

**`charmbracelet/x/vt`** — Go virtual terminal emulator from the Charm team. Parses ANSI escape sequences into an in-memory screen buffer. The `SafeEmulator` variant is thread-safe for concurrent write (output goroutine) and read (Bubble Tea render). Pre-release (v0.0.0 pseudo-version), but actively developed with 21+ real-world importers. Natural fit for the existing Charm stack (bubbletea, lipgloss, bubbles).

### What Stays Unchanged

- All sidecar code: `daemon.go`, `status.go`, `policy.go`, `ringbuf.go`
- Socket protocol: `protocol/messages.go`
- Session/folder storage: `store/`, `model/`
- Session manager: `manager.go` (`Spawn()`, `Resume()`, `GetStatus()`)
- CLI subcommands: `jarvis ls`, `jarvis status`, `jarvis done`, etc.
- Config and approval policies

## Component Model

```
MultiplexerModel (root tea.Model)
+-- Sidebar       -- session tree, folder navigation, status polling
+-- TermPane      -- VT emulator + sidecar connection for active session
+-- StatusBar     -- bottom bar: active session name, keybind hints, notifications
+-- FocusManager  -- tracks which component receives keystrokes
```

### Sidebar

Replaces the current `Dashboard` model. Reuses the existing `builder.go` logic (folder tree flattening, recent sessions, Done section) but renders into a fixed-width column. All current keybinds work when sidebar has focus:

- `n` — new session
- `f` — new folder
- `d` — mark done
- `/` — search
- `Enter` — attach to selected session (focus shifts to main pane)
- `j/k` or arrows — navigate
- `space` — expand/collapse folder

Styled like a file tree browser (similar to Cursor/VS Code's sidebar).

### TermPane

New component. Wraps `vt.SafeEmulator` and a sidecar socket connection.

On attach to a session:
1. Dial the sidecar Unix socket
2. Request ring buffer (`{Action: "get_buffer", Lines: 5000}`) for catch-up
3. Feed buffer into VT emulator via `em.Write()`
4. Send `{Action: "attach"}` to claim the attach slot
5. Start output streaming goroutine: sidecar sends `{Event: "output", Data: base64}`, decode, `em.Write()`
6. On each Bubble Tea render cycle, call `em.Render()` for the main pane content

On detach (switch session or focus sidebar):
1. Send `{Action: "detach"}` to sidecar
2. Close connection
3. Reset VT emulator (clear screen buffer). No need to preserve — the ring buffer provides catch-up on re-attach.

On session switch:
- Detach current, attach new. Instant — ring buffer provides catch-up context.

Input forwarding (when focused):
- `tea.KeyMsg` -> `em.SendKey()` -> read from emulator input pipe -> send to sidecar as `{Action: "send_input", Text: base64}`

Resize handling:
- `tea.WindowSizeMsg` -> recalculate pane dimensions -> `em.Resize(cols, rows)` -> send `{Action: "resize", Cols: c, Rows: r}` to sidecar

### StatusBar

Single row at the bottom. Shows:
```
session-name | state | Option+S: sidebar | Option+J/K: switch
```

### FocusManager

Tracks which component receives keystrokes. Two states:

**Sidebar focused:** Arrow keys/j/k navigate sessions. All dashboard keybinds active. Enter attaches and shifts focus to TermPane.

**TermPane focused:** All keystrokes forwarded to sidecar via VT emulator, except intercepted keys (see Keyboard Input section).

## Keyboard Input

### Focus Toggle

`Option+S` (`\x1b s`) toggles focus between sidebar and main pane. This works when the terminal is configured with "Option as Meta key" (standard for developer setups on Mac).

### Intercepted Keys (never forwarded to session)

| Key | Sequence | Action |
|---|---|---|
| Option+S | `\x1b s` | Toggle sidebar focus |
| Option+J | `\x1b j` | Select next session in sidebar (without leaving main pane) |
| Option+K | `\x1b k` | Select previous session in sidebar |
| Option+Enter | `\x1b \r` | Attach to selected session (switch main pane) |
| Option+N | `\x1b n` | New session (without leaving main pane) |

### Input Path

```
Physical keystroke
  -> Bubble Tea captures as tea.KeyMsg
  -> FocusManager routes to TermPane
  -> TermPane calls em.SendKey(keyMsg)
  -> VT emulator writes raw bytes to its input pipe
  -> Read from em input pipe, send to sidecar as send_input
```

All other keys pass through to Claude Code, including Ctrl+C, Ctrl+Z, Ctrl+D, escape, arrow keys, Tab, Enter, function keys.

### Configurability

The prefix key (Option/Alt) is configurable in `config.yaml` as a safety valve in case of conflicts:

```yaml
keybindings:
  sidebar_toggle: "alt+s"   # default
```

## Layout & Rendering

```
+----------------------+----------------------------------------+
| SESSIONS             | session output (VT emulator)           |
|                      |                                        |
| > Recent             | Claude's output renders here with      |
|   * auth-fix         | full ANSI color, cursor positioning,   |
|   ! api-refactor     | syntax highlighting, etc.              |
|                      |                                        |
| > Sprint-42          | Approval prompts, code diffs, and      |
|   ~ test-suite       | all interactive Claude features work   |
|   * db-migration     | because the VT emulator handles        |
|                      | the full terminal protocol.            |
| v Done               |                                        |
|   . cleanup-v2       |                                        |
+----------------------+----------------------------------------+
| * auth-fix | working | Option+S: sidebar | Option+J/K: switch |
+----------------------+----------------------------------------+
```

### Layout Rules

- **Sidebar:** Fixed width, configurable, default 24 chars. Separated by a vertical border.
- **Main pane:** Fills remaining width. VT emulator sized to `(termWidth - sidebarWidth - 1)` cols x `(termHeight - 1)` rows.
- **Status bar:** 1 row at bottom, always visible.
- **Focus indicator:** Sidebar border is highlighted (purple) when sidebar has focus, dim when main pane has focus.
- **Empty main pane:** Shows "Select a session from the sidebar or press 'n' to create one" when no session is attached.

### Rendering

```go
func (m *MultiplexerModel) View() string {
    sidebar := m.sidebar.View()
    main := m.termPane.View()       // em.Render() output
    status := m.statusBar.View()

    body := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, border, main)
    return lipgloss.JoinVertical(lipgloss.Left, body, status)
}
```

## Status Polling

Sidebar session status icons update via background polling:

- A `tea.Tick` fires every 2 seconds
- For each session, dial its sidecar socket with `{Action: "get_status"}`
- Update sidebar items with fresh state (working/idle/approval/exited)
- Dead sidecars (socket unreachable) show as "suspended"
- These are lightweight status-only connections that never send `attach` — the existing sidecar protocol already supports this

## Data Flow: Sidecar Integration

No sidecar protocol changes required. The existing protocol supports all needed operations:

```
On attach:
  1. conn = dial("unix", socketPath)
  2. send {Action: "get_buffer", Lines: 5000}
  3. feed response.Data into VT emulator (catch-up)
  4. send {Action: "attach"}
  5. start output streaming goroutine

Output goroutine (runs continuously):
  sidecar sends {Event: "output", Data: base64}
  -> decode -> em.Write(bytes)
  -> Bubble Tea re-renders on next tick

Input path (when TermPane focused):
  tea.KeyMsg -> em.SendKey() -> read bytes -> send {Action: "send_input", Text: base64}

On detach:
  send {Action: "detach"}
  close conn

On switch session:
  detach current -> attach new (instant)

Resize:
  tea.WindowSizeMsg -> em.Resize(cols, rows) -> send {Action: "resize", Cols: c, Rows: r}
```

## Migration

### Replaced Components

| Current | New |
|---|---|
| `internal/tui/dashboard.go` (full-screen) | Refactored into `Sidebar` component (reuses most logic) |
| `internal/session/attach.go` (raw PTY passthrough) | Replaced by `TermPane` (VT emulator rendering) |
| `cmd/jarvis/main.go` dashboard loop (exit/attach/restart) | Single `tea.NewProgram(NewMultiplexer())`, no restart cycle |

### Preserved Fallback

`jarvis attach <name>` CLI command retains the old raw PTY attach mode for debugging or when VT emulator rendering has issues. This is a safety valve, not the primary workflow.

## Testing

### Unit Tests

- **VT emulator rendering:** Feed known ANSI sequences, assert screen buffer matches expected output. Colors, cursor, scrollback, alternate screen.
- **Focus manager:** Assert Option+S toggles focus, Option+J/K changes sidebar selection, intercepted keys never reach sidecar.
- **Sidebar:** Reuse existing dashboard tests for folder expand/collapse, search, session ordering in narrower layout.
- **Input encoding:** Assert all key types (Ctrl+C, arrows, Tab, etc.) encode to correct bytes for sidecar.

### Integration Tests

- **Sidecar round-trip:** Spawn sidecar with `cat`, connect TermPane, send input, verify VT emulator shows echo.
- **Session switching:** Attach A, switch to B, verify B's output. Switch back, verify A restored from ring buffer.
- **Resize:** Change dimensions, verify VT emulator and sidecar PTY both resize, output reflows.

### Manual Testing

- Full Claude Code session through multiplexer: syntax highlighting, approval prompts, long output streaming, vim/nano (alternate screen).

### Performance

- High-throughput: pipe large file through sidecar -> VT emulator -> render. Target: no perceptible lag for normal Claude usage.

## File Structure (New/Modified)

```
internal/tui/
  multiplexer.go    -- MultiplexerModel (root tea.Model)
  sidebar.go        -- Sidebar component (refactored from dashboard.go)
  termpane.go       -- TermPane component (VT emulator + sidecar connection)
  statusbar.go      -- StatusBar component
  focus.go          -- FocusManager
  styles.go         -- updated styles for multiplexer layout

cmd/jarvis/main.go  -- simplified: single tea.NewProgram, no attach loop
```
