# Jarvis

Multi-session task manager for Claude Code. Run multiple Claude Code sessions in parallel with a TUI dashboard, PTY sidecars for attach/detach, and folder-based organization.

## Install

Download the latest release from [GitHub Releases](https://github.com/jianyyu/jarvis/releases), or build from source:

```bash
make build
# Binaries: ./jarvis, ./jarvis-sidecar
```

Copy them somewhere on your `$PATH`:

```bash
cp jarvis jarvis-sidecar ~/.local/bin/
```

## Usage

```bash
jarvis              # Open the TUI dashboard
jarvis new "task"   # Create a new session and attach
jarvis chat         # Quick unnamed session
jarvis attach NAME  # Reattach to a session
jarvis ls           # List sessions
jarvis status NAME  # Detailed session info
jarvis done NAME    # Mark session as done
jarvis rm NAME      # Delete a session
```

### Dashboard keybindings

| Key       | Action                  |
|-----------|-------------------------|
| `j/k`     | Navigate up/down        |
| `Enter`   | Attach / toggle folder  |
| `n`       | New session             |
| `/`       | Search                  |
| `d`       | Mark done               |
| `x`       | Delete                  |
| `q`       | Quit                    |

### Detach

Press `Ctrl-\` to detach from a running session without stopping it.

## Build from source

Requires Go 1.24+.

```bash
make build      # Build both binaries
make test       # Run tests
make clean      # Remove build artifacts
```

## Architecture

- **jarvis** — CLI + bubbletea TUI dashboard
- **jarvis-sidecar** — PTY daemon per session, communicates via Unix socket
- Sessions persist in `~/.jarvis/sessions/`
- Folders configured in `~/.jarvis/config.yaml`

## License

MIT
