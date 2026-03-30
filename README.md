<h1 align="center">Jarvis</h1>

<p align="center">
  <strong>Multi-session manager for Claude Code</strong><br>
  Run 10+ AI coding sessions in parallel. Attach, detach, and switch between them like tmux.
</p>

<p align="center">
  <a href="https://github.com/jianyyu/jarvis/releases">Releases</a> &middot;
  <a href="#install">Install</a> &middot;
  <a href="PRD.md">Design Doc</a>
</p>

---

## The Problem

Claude Code runs one session at a time. If you're juggling a bug fix, a feature branch, a code review, and an investigation, you're constantly losing context switching between them.

## The Solution

Jarvis gives you a **TUI dashboard** to manage all your Claude Code sessions. Each session runs in its own PTY with a background sidecar daemon — so Claude keeps working even when you're not watching. Detach and reattach anytime, just like tmux or screen.

```
JARVIS -- 14 sessions

  ▶ clean-room              8/37 done
  ▶ marketplace-3p-app      6/13 done
  ▶ marketplace-ops         8/11 done
  ▶ jarvis                  8/10 done
  ● Investigate Marketplace Chinanorth3 PagerDuty Incident    active    1d
  ● Fix clean room list API docs                              active    2d
  ▶ Done                    99/99 done

  [enter] attach  [n]ew  [c]hat  [f]older  [d]one  [/]search  [q]uit
```

### Key Features

- **Parallel sessions** -- Run multiple Claude Code instances simultaneously, each in an isolated PTY
- **Attach/detach** -- `Ctrl-\` to detach; sessions keep running in the background
- **TUI dashboard** -- Navigate, search, and organize sessions with keyboard shortcuts
- **Folder organization** -- Group sessions by project, team, or workflow
- **Auto-resume** -- Reattach picks up exactly where Claude left off (`--resume`)
- **Persistent state** -- Sessions, folders, and dashboard view survive restarts

## Install

Download prebuilt binaries from [GitHub Releases](https://github.com/jianyyu/jarvis/releases), or build from source:

```bash
git clone https://github.com/jianyyu/jarvis.git
cd jarvis
make build
cp jarvis jarvis-sidecar ~/.local/bin/
```

Requires Go 1.24+ and [Claude Code](https://docs.anthropic.com/en/docs/claude-code) installed.

## Usage

```bash
jarvis              # Open the TUI dashboard
jarvis new "task"   # Create a new session and attach
jarvis chat         # Quick unnamed session
jarvis attach NAME  # Reattach to a session
jarvis ls           # List all sessions
jarvis done NAME    # Mark session as done
jarvis rename NAME  # Rename current session
jarvis init TITLE   # Rename + create git worktree/branch
```

### Dashboard Keybindings

| Key | Action |
|---|---|
| `j` / `k` | Navigate up/down |
| `Enter` | Attach to session / toggle folder |
| `n` | New named session |
| `c` | Quick chat session |
| `f` | Create folder |
| `r` | Rename |
| `d` | Mark done |
| `x` | Delete |
| `/` | Search |
| `Ctrl-\` | Detach (while attached) |
| `q` | Quit |

### Claude Code Integration

Jarvis sets `JARVIS_SESSION_ID` in each session's environment. Use slash commands from within Claude Code:

- `/jarvis-rename` -- Rename the current session
- `/jarvis-init` -- Initialize with title, git worktree, and branch

## Architecture

```
jarvis (TUI)  ──unix socket──  jarvis-sidecar (PTY daemon)  ──pty──  claude
     │                              │
     │                         background process
     │                         survives detach
     │
  ~/.jarvis/
  ├── sessions/    # session state (YAML)
  ├── folders/     # folder hierarchy
  └── config.yaml  # settings
```

- **jarvis** -- CLI + [Bubble Tea](https://github.com/charmbracelet/bubbletea) TUI dashboard
- **jarvis-sidecar** -- One per session. PTY daemon that keeps Claude Code alive, communicates status via Unix socket
- **Sessions persist** across restarts. Sidecar auto-respawns with `claude --resume` on reattach

## Why Not Just Use tmux?

| | tmux | Jarvis |
|---|---|---|
| Session organization | Flat list | Folders + search |
| Status visibility | Must attach to check | Dashboard shows live state |
| Claude resume | Manual `--resume` flag | Automatic |
| Session lifecycle | Manual cleanup | Mark done, archive |
| Per-session state | None | CWD, Claude session ID, metadata |

## License

MIT
