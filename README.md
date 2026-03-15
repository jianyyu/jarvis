# Jarvis

A task context manager for [Claude Code](https://docs.anthropic.com/en/docs/claude-code). Each task owns a git worktree and one or more Claude Code sessions. Jarvis gets you back into the right context in seconds.

## Why

When you juggle multiple tasks — a bug fix, a feature, an incident — each needs its own branch, worktree, and Claude Code session. Jarvis manages all of that so you don't have to.

- **One task = one worktree** — fully isolated working directories
- **Session tracking** — resume Claude Code exactly where you left off
- **Auto-enrichment** — branches and references are captured automatically via Claude Code hooks
- **Zero interference** — Claude Code runs normally, Jarvis just manages the context around it

## Install

```bash
pip install .
```

Requires Python 3.10+ and [Claude Code](https://docs.anthropic.com/en/docs/claude-code).

If you have [git-stack](https://github.com/search?q=git-stack) installed, Jarvis will use it for stacked branches. Otherwise, it falls back to standard git branching.

## Quick start

```bash
# Launch Jarvis from inside a git repo
jarvis
```

```
  JARVIS — 3 active tasks

  ❯ ● 3P closed-source app design              1d ago
    ● Clean room incident investigation        2h ago
    ● Fix flaky estore test                    3d ago

  >
```

## Commands

| Command | Action |
|---------|--------|
| `/new` | Create a new task with worktree + branch |
| `/chat` | Create a lightweight task (no worktree, main repo) |
| `/cc` | Launch/resume Claude Code for selected task |
| `/done` | Mark selected task done + cleanup worktree |
| `/info` | Show task details |
| `/refs` | View auto-detected references |
| `/note` | Open notes in `$EDITOR` |
| `/ls` | Toggle showing done tasks |
| `/q` | Quit |

Arrow keys to navigate, Enter to open the selected task.

## How it works

1. **`/new "fix the bug"`** — creates a git worktree, a branch, and a task entry
2. **`/cc`** — launches Claude Code in the worktree with task context injected
3. **Hooks** — Claude Code hooks automatically capture session IDs, branches, and URLs you paste
4. **`/cc` again** — resumes the previous Claude Code session in the same worktree
5. **`/done`** — marks the task complete and cleans up the worktree

Tasks are stored as YAML files in `~/.jarvis/tasks/`.

## License

MIT
