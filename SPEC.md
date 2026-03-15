# Jarvis — Task Context Manager for Claude Code

## One-liner

Jarvis is a persistent interactive shell that manages developer workstreams. Each task owns a git worktree and one or more Claude Code sessions. Jarvis gets you back into the right context in seconds.

## Core Concepts

### Task = Worktree

- One task = one git worktree = one isolated working directory
- No two tasks share a directory
- Stacked branches within the same worktree are part of the same task
- Tasks start as a blank slate and enrich over time

### Jarvis is the Lobby

- Jarvis is a persistent interactive dialog (not fire-and-forget CLI commands)
- You launch `jarvis`, see your tasks, pick one or create one, then `/cc` to drop into Claude Code
- When you exit Claude Code, you're back in Jarvis
- Jarvis never interferes with the Claude Code experience

## UX Flow

### Launch

```
$ jarvis

  JARVIS — 3 active tasks

  ● 3P closed-source app design              1d ago
  ● Clean room incident investigation        2h ago
  ● Fix flaky estore test                    3d ago

  >
```

### Create a Task

```
  > /new
  What are you working on? > investigate the kafka schema issue

  Created task: "investigate the kafka schema issue"
  Worktree: .claude/worktrees/investigate-kafka-schema-issue

  >
```

- The first message is the only required input
- Worktree name is derived from the message (lowercase, hyphenated, truncated)
- No other metadata required at creation time

### Open Claude Code

```
  > /cc
  Launching Claude Code with context...

  [Claude Code session — full normal experience]
  [You exit Claude Code]

  Back in JARVIS.
  >
```

- First `/cc` on a new task: launches `claude` in the worktree with `--append-system-prompt` containing the task name and any references
- Subsequent `/cc`: launches `claude --resume <session-id>` in the worktree
- The Claude Code session is 100% normal — no wrapper, no proxy

### Select a Task

Interactive picker with arrow keys (like Claude Code's `/resume` picker):

```
  JARVIS — 3 active tasks                          /: search

  ❯ ● 3P closed-source app design              1d ago
    ● Clean room incident investigation        2h ago
    ● Fix flaky estore test                    3d ago

  ↑↓ navigate  enter select  /new  /cc  /done  /q
```

- Arrow keys to scroll up/down
- `/` to fuzzy search/filter by name
- `enter` to select the highlighted task
- Selected task is remembered — `/cc` and `/done` operate on it

### Task Info

```
  > /info

  ╭─ investigate the kafka schema issue ───────────────╮
  │ Status:    ● active                                 │
  │ Branches:  stack/kafka-schema-fix                   │
  │ Worktree:  .claude/worktrees/investigate-kafka-...  │
  │ Created:   2026-03-08 10:00                         │
  │ Sessions:  1 (latest: 5m ago)                       │
  │                                                     │
  │ References:                                         │
  │  • Slack: oncall alert thread                       │
  ╰─────────────────────────────────────────────────────╯
```

### Mark Done

```
  > /done
  Mark "investigate the kafka schema issue" as done?
  Branch not merged. Remove worktree anyway? (y/n) y
  Task marked done. Worktree removed.
```

- Worktree is removed on done (after confirmation if branch not merged)
- Task metadata (yaml, refs, notes) is kept permanently

### Commands

| Command | Action |
|---------|--------|
| `/new` | Create a new task with worktree + branch |
| `/chat` | Create a lightweight task (no worktree, main repo) |
| `/cc` | Launch/resume Claude Code for selected task |
| `/done` | Mark selected task done + cleanup worktree |
| `/refs` | View auto-detected references for selected task |
| `/info` | Show task details |
| `/note` | Open notes in $EDITOR |
| `/ls` | List tasks (including done) |
| `/q` | Quit Jarvis |
| `<number>` | Select a task by index |

### Claude Code Slash Commands

| Command | Action |
|---------|--------|
| `/jarvis-init "title"` | Upgrade chat task: set title, create worktree + branch |

## Auto-Enrichment

Task metadata is enriched automatically without user input:

| Data | How | When |
|------|-----|------|
| Task name | First message during `/new` | At creation |
| Worktree name | Derived from task name | At creation |
| Claude session ID | Captured when `/cc` launches Claude | On `/cc` |
| Branch names | `PostToolUse` hook on Bash (detects `git stack create`, `git checkout -b`, etc.) | Real-time during Claude session |
| References/URLs | `UserPromptSubmit` hook detects URLs in user messages and auto-adds them | Real-time during Claude session |

## Data Model

### Storage Layout

```
~/.jarvis/
  config.yaml                    # global config (repo path, preferences)
  tasks/
    <task-id>/
      task.yaml                  # metadata
      references.yaml            # labeled links
      notes.md                   # free-form scratchpad
```

### task.yaml

```yaml
id: "a1b2c3d4"
name: "investigate the kafka schema issue"
status: active                   # active | done
cwd: "/home/user/universe/.claude/worktrees/investigate-kafka-schema-issue"
branches:
  - stack/kafka-schema-fix
  - stack/kafka-schema-test
claude_sessions:
  - id: "298e5fe3-b33c-4896-9a85-7c65625407df"
    started_at: "2026-03-08T10:00:00Z"
created_at: "2026-03-08T10:00:00Z"
updated_at: "2026-03-08T12:00:00Z"
tags: []
```

### references.yaml

```yaml
- label: "oncall alert thread"
  url: "https://slack.com/archives/C0123/p456"
  type: slack
  added_at: "2026-03-08T10:05:00Z"
- label: "ES-1234"
  url: "https://jira.com/browse/ES-1234"
  type: jira
  added_at: "2026-03-08T10:05:00Z"
```

## Claude Code Integration

### Launching

- New session: `claude --append-system-prompt <context>` in the task's worktree directory
- Resume: `claude --resume <session-id>` in the task's worktree directory
- Session ID captured from Claude Code's output or JSONL after exit

### Hooks

Jarvis registers Claude Code hooks for auto-enrichment:

```json
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "jarvis enrich --event post-tool-use"
          }
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "jarvis enrich --event user-prompt"
          }
        ]
      }
    ]
  }
}
```

**PostToolUse (Bash)**: Inspects `tool_input` for git branch commands (`git stack create`, `git checkout -b`, `git switch -c`) and updates task branches.

**UserPromptSubmit**: Scans `prompt` for URLs (Slack, Jira, Confluence, GitHub PRs, Google Docs) and auto-adds them as references. Detects URL type from domain and generates a label automatically. Deduplicates against existing references.

### Context Injection

On first `/cc`, the `--append-system-prompt` includes:

```
You are working on: "investigate the kafka schema issue"
References:
- Slack: oncall alert thread (https://slack.com/archives/C0123/p456)
- Jira: ES-1234 (https://jira.com/browse/ES-1234)
```

On subsequent `/cc` (resume), no injection needed — the session has full history.

## Architecture

```
                              Jarvis CLI Architecture
                              ======================

    ┌─────────────────────────────────────────────────────────────────┐
    │                        Terminal (user)                          │
    │                                                                 │
    │   $ jarvis                    $ jarvis-task init "title"        │
    │   ┌─────────┐                 ┌──────────────┐                  │
    │   │ Jarvis  │                 │ jarvis-task   │                  │
    │   │ (lobby) │                 │ (from Claude) │                  │
    │   └────┬────┘                 └──────┬───────┘                  │
    └────────┼────────────────────────────-┼──────────────────────────┘
             │                              │
             │  /new, /chat, /done          │  init, set-title
             │                              │
    ┌────────▼──────────────────────────────▼──────────────────────────┐
    │                        Shared Internals                          │
    │                                                                  │
    │  ┌──────────┐  ┌───────────┐  ┌──────────┐  ┌───────────────┐   │
    │  │ store.py │  │worktree.py│  │launcher.py│  │enrichment.py  │   │
    │  │          │  │           │  │           │  │               │   │
    │  │ CRUD     │  │ create    │  │ launch    │  │ SessionStart  │   │
    │  │ tasks    │  │ remove    │  │ resume    │  │ PostToolUse   │   │
    │  │ on YAML  │  │ checkout  │  │ migrate   │  │ hooks         │   │
    │  └────┬─────┘  └─────┬────┘  └─────┬─────┘  └───────┬───────┘   │
    └───────┼──────────────┼────────────-─┼────────────────┼───────────┘
            │              │              │                │
            ▼              ▼              ▼                ▼
    ┌──────────────┐ ┌──────────┐ ┌─────────────┐ ┌──────────────────┐
    │ ~/.jarvis/   │ │ git      │ │ claude      │ │ .claude/         │
    │ tasks/       │ │ worktree │ │ (Claude Code│ │ settings.local   │
    │   <id>/      │ │ git stack│ │  process)   │ │ .json (hooks)    │
    │   task.yaml  │ │          │ │             │ │                  │
    └──────────────┘ └──────────┘ └─────────────┘ └──────────────────┘


    Task Lifecycle — Two Paths
    ==========================

    Path A: /new (know what you're doing)
    ─────────────────────────────────────

    /new "fix the bug"
         │
         ├─► create worktree + stack branch
         ├─► create task (cwd=worktree, branches=[stack/fix-bug])
         ├─► install hooks in worktree
         └─► launch Claude Code in worktree
                │
                └─► SessionStart hook captures session ID
                └─► work, commit, push...
                └─► exit → back to Jarvis


    Path B: /chat → /jarvis-init (figure it out first)
    ──────────────────────────────────────────────────

    /chat
         │
         ├─► create task (cwd=main repo, no branches)
         ├─► install hooks in main repo
         └─► launch Claude Code in main repo
                │
                ├─► SessionStart hook captures session ID
                ├─► chat, research, explore...
                │
                ├─► /jarvis-init "fix the bug"
                │      │
                │      ├─► create worktree + stack branch
                │      ├─► checkout files in worktree
                │      ├─► move session file to new project dir
                │      ├─► install hooks in worktree
                │      └─► update task (cwd=worktree, branches=[stack/fix-bug])
                │
                └─► exit → back to Jarvis
                         │
                         └─► resume finds session in worktree project dir


    Environment & Session Tracking
    ==============================

    Jarvis                     Claude Code
    ──────                     ──────────
    set JARVIS_TASK_ID ──────► env var (per-process, isolated)
                                  │
    SessionStart hook  ◄──────── fires on session start
    (captures session_id)         │
                                  │
    PostToolUse hook   ◄──────── fires on Bash tool use
    (captures branches)           │
                                  │
                               jarvis-task init "title"
                               reads $JARVIS_TASK_ID
                               updates task via store.py
```

## Tech Stack

- Python 3.10+
- `prompt_toolkit` — interactive dialog / input
- `rich` — display (tables, panels, colors)
- `pyyaml` — task/config storage
- No database, no network, no external services

## Project Structure

```
experimental/jianyu.zhou/jarvis/
  pyproject.toml
  jarvis/
    __init__.py
    app.py              # Main interactive loop (the "lobby")
    commands.py         # Slash command parsing
    store.py            # Task CRUD on filesystem
    models.py           # Task, Reference dataclasses
    launcher.py         # Claude Code launch/resume/session migration
    enrichment.py       # Hook handlers (SessionStart, PostToolUse)
    display.py          # Rich rendering (task list, info panels)
    config.py           # ~/.jarvis/config.yaml management
    worktree.py         # Git worktree lifecycle (create, remove, checkout)
    task_cli.py         # jarvis-task CLI (called from within Claude Code)
```

## V2 Roadmap (not V1)

- Semantic search over tasks (fuzzy / embedding-based)
- Auto-create tasks from PagerDuty / Slack alerts
- `/refresh` — re-fetch linked references and inject into Claude
- Task templates (oncall, feature, bug)
- Task dependencies
- Multiple repo support
