# Jarvis Commander — Product Requirements Document

## Overview

Jarvis evolves from a **task lobby** (one Claude Code session at a time, no interaction) to a **multi-session commander** (concurrent background sessions with bidirectional communication and external system integration).

### Problem

Today, Jarvis can only open one Claude Code session at a time via `subprocess.run()`. This means:

- **No multi-tasking** — juggling tasks requires multiple terminal windows
- **No background execution** — exiting Jarvis kills the Claude Code session
- **No interaction** — Jarvis cannot observe what Claude Code is doing or intervene (e.g., auto-approve a permission prompt)
- **No oversight** — no dashboard showing what each session is doing
- **No reactivity** — external events (PR review requests, Slack messages, incidents) require manual attention and manual session creation

### Vision

Jarvis becomes a **personal AI chief of staff** — a CLI-native commander that manages multiple Claude Code sessions, reacts to external signals, and prepares responses for human review.

- Run multiple Claude Code sessions concurrently in the background
- Detach from Jarvis and have sessions continue running; reattach at any time
- Auto-handle routine decisions (permission approvals, simple choices)
- **React to external systems** — GitHub PRs, Slack messages, PagerDuty incidents automatically trigger sessions
- **Prepare but never send** — Jarvis drafts responses and actions but always requires human approval before any external-facing action

### Core Principle: Prepare, Never Act Externally

Jarvis must **never** take external-facing actions on behalf of the user without explicit approval:

- Never comment on GitHub PRs directly
- Never send Slack messages directly
- Never approve/merge PRs directly
- Never create Jira tickets directly

Jarvis **prepares** — the human **sends**. This is non-negotiable. The user's professional reputation depends on the quality and tone of outgoing communication. Jarvis is a staff that drafts; the user is the executive who signs.

---

## Architecture

### Current (V1)

```
User terminal ── Jarvis (lobby) ── subprocess.run("claude") ── blocked until exit
```

Jarvis hands off the terminal entirely. No communication, no concurrency.

### Proposed (V2) — Full System Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                        EXTERNAL SYSTEMS                             │
│  GitHub API  ·  Slack API  ·  PagerDuty API  ·  Jira API            │
└──────┬──────────────┬──────────────┬──────────────┬─────────────────┘
       │              │              │              │
       ▼              ▼              ▼              ▼
┌─────────────────────────────────────────────────────────────────────┐
│                     WATCHERS (background threads)                    │
│                                                                     │
│  github_watcher ·  slack_watcher  ·  pd_watcher  ·  jira_watcher    │
│                                                                     │
│  Poll external systems → emit events → check context registry       │
│  → route to existing session OR create new session via rules        │
└──────────────────────────────┬──────────────────────────────────────┘
                               │ events
                               ▼
┌─────────────────────────────────────────────────────────────────────┐
│                        JARVIS CORE PROCESS                          │
│                                                                     │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │ TUI Layer                                                     │  │
│  │ ┌─────────────────────────────────────────────────────────┐   │  │
│  │ │ Dashboard: folder tree + session statuses + ideas       │   │  │
│  │ │ ▼ Auth System Rewrite                       2/4 done   │   │  │
│  │ │   ● implement-middleware    working  writing migration  │   │  │
│  │ │ ▼ Oncall                                                │   │  │
│  │ │   ⚠ fix-bug-123            blocked  Allow Bash: rm?    │   │  │
│  │ │ ● review-PR-1234           working  reading diff        │   │  │
│  │ └─────────────────────────────────────────────────────────┘   │  │
│  │ ┌─────────────────────────────────────────────────────────┐   │  │
│  │ │ Smart Bar: > /new, /attach, /approve, /search, chat... │   │  │
│  │ └─────────────────────────────────────────────────────────┘   │  │
│  │                                                               │  │
│  │ Attach Mode: raw PTY passthrough to a single session          │  │
│  │ Outbox Mode: review / edit / send prepared drafts             │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                     │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │ Core Services                                                 │  │
│  │                                                               │  │
│  │  Session         Context          Folder          Outbox      │  │
│  │  Manager         Registry         Store           Store       │  │
│  │  ─────────       ─────────        ─────────       ─────────   │  │
│  │  spawn()         lookup()         create()        add_draft() │  │
│  │  attach()        register()       move()          send()      │  │
│  │  detach()        route_event()    archive()       discard()   │  │
│  │  kill()                                                       │  │
│  │  get_status()    Idea                                         │  │
│  │  send_input()    Store                                        │  │
│  │  resume()        ─────────                                    │  │
│  │                  capture()                                    │  │
│  │                  promote()                                    │  │
│  └──────────────────────┬────────────────────────────────────────┘  │
│                         │                                           │
└─────────────────────────┼───────────────────────────────────────────┘
                          │
          Session Manager manages sidecar lifecycle
                          │
      ┌───────────────────┼───────────────────┐
      │                   │                   │
      ▼                   ▼                   ▼
┌───────────┐       ┌───────────┐       ┌───────────┐
│ Sidecar 1 │       │ Sidecar 2 │       │ Sidecar 3 │     DAEMON
│           │       │           │       │           │     PROCESSES
│ PTY mstr ←┼─sock──┼─ Jarvis   │       │           │     (survive
│ PTY slave─┼─┐     │           │       │           │      jarvis
│ status    │ │     │           │       │           │      exit)
│ ring buf  │ │     │           │       │           │
└───────────┘ │     └─────┬─────┘       └─────┬─────┘
              │           │                   │
              ▼           ▼                   ▼
         ┌────────┐  ┌────────┐          ┌────────┐
         │ claude │  │ claude │          │ claude │     CLAUDE CODE
         │ code   │  │ code   │          │ code   │     SESSIONS
         │        │  │        │          │        │
         │ stdin  │  │ stdin  │          │ stdin  │
         │ stdout │  │ stdout │          │ stdout │
         │ stderr │  │ stderr │          │ stderr │
         └────────┘  └────────┘          └────────┘
              │           │                   │
              ▼           ▼                   ▼
         worktree 1  worktree 2          worktree 3    GIT WORKTREES
         branch A    branch B            branch C      (on disk)
```

### Two Levels of Persistence

The remote server can be **terminated** at any time. Only `~/` survives on the persisted volume. Next boot = new instance, fresh OS, all processes gone. This means two distinct resume paths:

```
┌─────────────────────────────────────────────────────────────────────┐
│                    WHAT SURVIVES INSTANCE TERMINATION                │
│                                                                     │
│  ✓ ~/.jarvis/sessions/*/session.yaml    (session metadata)          │
│  ✓ ~/.jarvis/folders/*.yaml             (folder structure)          │
│  ✓ ~/.jarvis/ideas/*.yaml               (idea backlog)             │
│  ✓ ~/.jarvis/context_registry.yaml      (external context map)     │
│  ✓ ~/.claude/projects/*/session.jsonl   (Claude Code history)      │
│  ✓ git worktrees + branches             (code)                     │
│                                                                     │
│  ✗ sidecar processes                    (dead)                     │
│  ✗ PTY pairs                            (dead)                     │
│  ✗ Unix sockets                         (dead)                     │
│  ✗ watcher threads                      (dead)                     │
└─────────────────────────────────────────────────────────────────────┘
```

**Resume path 1: Detach / reattach (same instance, sidecar alive)**

```
User detaches from session
  → Jarvis disconnects from sidecar Unix socket
  → Sidecar + Claude Code keep running in background
  → User quits jarvis entirely — sessions still alive
  → User relaunches jarvis
  → Session Manager pings /tmp/jarvis/<id>.sock → sidecar responds
  → Dashboard shows live status from sidecar
  → User attaches → reconnects to socket → raw PTY passthrough
```

**Resume path 2: Instance restart (sidecar dead, disk only)**

```
Instance terminated → new instance boots with ~/  mounted
  → User launches jarvis
  → Session Manager scans ~/.jarvis/sessions/
  → For each session: try ping socket → no response → sidecar dead
  → Read ~/.claude/projects/*/<session-id>.jsonl (last few events)
  → Derive last-known status from JSONL tail:
      last event = tool_use permission prompt  → "was: waiting for approval"
      last event = assistant text output       → "was: working"
      last event = session end                 → "done"
      last event = user message                → "was: waiting for claude"
  → Dashboard shows:
      ⏸ fix-bug-123          suspended   was: waiting for approval
      ⏸ implement-middleware  suspended   was: working
      ✓ review-PR-1234       done
  → User attaches to session
  → Session Manager spawns NEW sidecar → claude --resume <session-id>
  → Claude Code picks up from last state in the worktree
  → Session continues
```

### Session Status Model

```
                  ┌───────────────────────────────────┐
                  │         STATUS SOURCES             │
                  │                                    │
                  │  Sidecar alive? ──► live status    │
                  │    - PTY output monitoring         │
                  │    - Claude Code hooks             │
                  │    - Idle detection                │
                  │                                    │
                  │  Sidecar dead? ──► derived status  │
                  │    - Read session JSONL tail        │
                  │    - Infer last-known state         │
                  └───────────────────────────────────┘

Statuses:
  ● working              sidecar reports: Claude Code producing output
  ⚠ waiting_for_approval sidecar reports: permission prompt detected
  ◌ idle                 sidecar reports: no output for N seconds
  ✓ done                 session exited normally, or user marked done
  ⏸ suspended            sidecar dead, last-known status shown
  ◌ queued               created but never launched
```

### Sidecar Internals

```
┌─────────────────────────────────────────────────────────┐
│  Sidecar Daemon (one per session)                       │
│                                                         │
│  ┌─────────────────┐    ┌───────────────────────────┐   │
│  │ PTY Manager     │    │ Status Monitor            │   │
│  │                 │    │                           │   │
│  │ master_fd ──────┼────┼─► pattern matching:       │   │
│  │    ▲    │       │    │   "Allow Bash?" → ⚠       │   │
│  │    │    ▼       │    │   output flowing → ●      │   │
│  │ read  write     │    │   no output 30s → ◌       │   │
│  │    │    │       │    │   process exited → ✓      │   │
│  │    │    │       │    └───────────┬───────────────┘   │
│  │ slave_fd ───────┼──► Claude Code stdin/stdout/stderr │
│  └─────────────────┘               │                    │
│                                    │                    │
│  ┌─────────────────┐    ┌──────────▼────────────────┐   │
│  │ Ring Buffer     │    │ Socket Server             │   │
│  │                 │    │                           │   │
│  │ Last 10K lines  │    │ /tmp/jarvis/<id>.sock     │   │
│  │ of PTY output   │    │                           │   │
│  │ (for attach     │    │ ← get_status              │   │
│  │  catch-up)      │    │ ← attach / detach         │   │
│  │                 │    │ ← send_input              │   │
│  │                 │    │ ← resize                  │   │
│  │                 │    │ → status events            │   │
│  │                 │    │ → output stream            │   │
│  └─────────────────┘    └───────────────────────────┘   │
│                                                         │
│  ┌─────────────────────────────────────────────────┐    │
│  │ State Persistence                                │    │
│  │                                                  │    │
│  │ Periodically write to session.yaml:              │    │
│  │  - current status (working/waiting/idle)         │    │
│  │  - Claude Code session ID                        │    │
│  │  - last activity timestamp                       │    │
│  │                                                  │    │
│  │ On exit: write final status to session.yaml      │    │
│  │ → survives instance termination                  │    │
│  └──────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────┘
```

### IPC Protocol

Newline-delimited JSON over Unix domain sockets.

**Jarvis → Sidecar:**

```json
{"action": "ping"}
{"action": "get_status"}
{"action": "attach"}
{"action": "detach"}
{"action": "send_input", "text": "y\n"}
{"action": "resize", "cols": 120, "rows": 40}
{"action": "get_buffer", "lines": 100}
```

**Sidecar → Jarvis:**

```json
{"event": "pong"}
{"event": "status", "state": "working", "detail": "writing migration file..."}
{"event": "status", "state": "waiting_for_approval", "detail": "Allow Bash: rm -rf build/"}
{"event": "output", "data": "<raw PTY bytes, base64 encoded>"}
{"event": "session_started", "session_id": "abc-123"}
{"event": "session_ended", "exit_code": 0}
```

### Key Components

#### 1. Session Manager (`session_manager.py`)

Central coordinator for all session lifecycle operations.

```
spawn(name, folder_id, prompt, cwd)
  → create worktree → create session.yaml → fork sidecar → launch claude

attach(session_id)
  → if sidecar alive: connect to socket, enter PTY passthrough
  → if sidecar dead: spawn new sidecar with claude --resume <id>

detach()
  → disconnect from socket, return to dashboard

get_all_statuses()
  → for each session:
    → sidecar alive? ping socket → live status
    → sidecar dead? read session.yaml + JSONL → derived status

resume_after_restart()
  → scan ~/.jarvis/sessions/
  → for each non-archived session:
    → check socket → dead
    → read JSONL tail → derive last-known status
    → mark as "suspended" with last-known detail
```

#### 2. Context Registry (`context_registry.py`)

Maps external contexts to sessions. Prevents duplicate sessions for the same context.

```yaml
# ~/.jarvis/context_registry.yaml
"github:databricks-eng/universe#1234": "session-abc"
"slack:C0123/p456": "session-def"
"pagerduty:INC-789": "session-ghi"
```

When a new event arrives:

1. Check registry → existing session? Route to it (inject new context).
2. Check routing rules in config → matching folder? Create session there.
3. No match → create unfiled session at root.

#### 3. Watchers (`watchers/`)

Background pollers that monitor external systems.


| Watcher          | Source              | Trigger                 | Action                                             |
| ---------------- | ------------------- | ----------------------- | -------------------------------------------------- |
| `github_watcher` | GitHub API / MCP    | Review request assigned | Create session: review PR, prepare comments        |
| `slack_watcher`  | Slack API / MCP     | DM or mention           | Create or route: analyze context, prepare response |
| `pd_watcher`     | PagerDuty API / MCP | Incident assigned       | Create session: investigate, prepare response      |
| `jira_watcher`   | Jira API / MCP      | Ticket assigned/updated | Create or route: analyze, prepare response         |


Watchers run as threads inside the jarvis process. They die when jarvis exits, restart when jarvis launches. This is fine — they're stateless pollers. The context registry (on disk) prevents duplicate session creation across restarts.

#### 4. Outbox (`outbox.py`)

Holds prepared responses pending human review. Nothing leaves Jarvis without explicit approval.

```
  OUTBOX — 2 drafts pending

  1. [GitHub] PR #1234: universe/auth-refactor
     Session: review-PR-1234 prepared 3 review comments
     [v]iew  [e]dit  [s]end  [d]iscard

  2. [Slack] @alice in #oncall: "can you check the kafka lag?"
     Session: investigate-kafka prepared a response
     [v]iew  [e]dit  [s]end  [d]iscard
```

### Full Lifecycle Diagram

```
USER ACTIONS                     JARVIS                          SYSTEM
────────────                     ──────                          ──────

$ jarvis ──────────────────► Boot
                              │
                              ├─ Scan ~/.jarvis/sessions/
                              ├─ Ping each sidecar socket
                              │   alive → get live status
                              │   dead  → read JSONL → derive status
                              ├─ Start watcher threads
                              ├─ Render dashboard
                              │
> /folder "Oncall" ────────► Create folder
                              │  write ~/.jarvis/folders/f1.yaml
                              │
> /new "fix auth bug" ─────► Spawn session
                              │  create worktree + branch
                              │  write session.yaml
                              │  fork sidecar daemon
                              │  sidecar: openpty() + exec(claude)
                              │  dashboard: ● fix-auth-bug  working
                              │
[arrow keys] Enter ────────► Attach
                              │  connect to sidecar socket
                              │  set terminal raw mode
                              │  raw PTY passthrough ◄──────────── Claude Code I/O
                              │
Ctrl-D ────────────────────► Detach
                              │  disconnect socket
                              │  restore terminal
                              │  back to dashboard
                              │  session keeps running ──────────► sidecar + claude alive
                              │
> /q ──────────────────────► Quit jarvis
                              │  stop watchers
                              │  disconnect all sockets
                              │  jarvis process exits
                              │  sidecars keep running ─────────► background work continues
                              │
                                                                 Instance terminated!
                                                                 All processes die.
                                                                 ~/  persisted.
$ jarvis ──────────────────► Boot (new instance)
                              │
                              ├─ Scan sessions → all suspended
                              ├─ Read JSONL → derive statuses
                              ├─ Dashboard:
                              │   ⏸ fix-auth-bug  suspended  was: working
                              │
[Enter] on fix-auth-bug ───► Resume
                              │  spawn NEW sidecar
                              │  claude --resume <session-id>
                              │  Claude Code picks up where it left off
                              │  ● fix-auth-bug  working
                              │
                                                    ◄──────────── GitHub: PR review request
                              ├─ github_watcher detects event
                              ├─ context registry: no match
                              ├─ routing rule: github → "Universe PRs"
                              ├─ spawn session in folder
                              │  dashboard: ● review-PR-567  working
                              │
                                                    ◄──────────── session finishes review
                              ├─ session writes to outbox
                              │  dashboard: ✓ review-PR-567  done  ←review
                              │
> /outbox ─────────────────► Review drafts
                              │  show prepared PR comments
                              │
> send ────────────────────► Send
                              │  post comments via GitHub API
                              │  mark draft as sent
```

---

## User Experience

### Dashboard + Smart Bar (Default View)

The primary UI is a **dashboard with a smart chat bar** at the bottom. Think of it like Spotlight/Alfred — but instead of just search, you can talk to it.

```
  JARVIS ─ 3 sessions · 2 drafts pending                12:34 PM

  ▼ Auth System Rewrite                                  2/5 done
    ● Implement middleware              working    writing migration...    2m
    ◌ Migrate endpoints                 queued
  ▼ Oncall
    ⚠ fix-bug-123                       blocked    Allow Bash: rm -rf?   30s
    ✓ review-PR-1234                    done       3 comments   ←review

  💡 2 ideas · 📤 2 drafts pending

  > _                                                    [enter] send
```

The `>` bar at the bottom is the **smart bar**. It accepts:

**Slash commands** (structured, instant):

```
  > /new fix auth token expiry bug
  > /idea rate limiting for public API
  > /approve fix-bug-123
  > /attach fix-bug-123
  > /outbox
  > /ideas
  > /done
```

**Natural language** (interpreted by Claude, richer):

```
  > fix the auth token bug alice mentioned on slack

    Jarvis: Creating task "Fix auth token expiry bug" under "Auth System Rewrite"
            → Pulling context from Slack thread #oncall/p789
            → Spawning session with context bundle
            [created ✓]

  > tell kafka-schema-fix to rebase onto main

    Jarvis: Sent instruction to session "kafka-schema-fix"
            → "Rebase onto main and resolve conflicts"
            [sent ✓]

  > what's fix-bug-123 trying to do?

    Jarvis: It wants to run `rm -rf build/ && npm run build`.
            This is a clean rebuild of the project. Approve? (y/n)

  > y

    Jarvis: Approved. [continuing ✓]
```

**The smart bar is lightweight** — it's not a full Claude Code session. It's a thin Claude call that has access to:

- Current task tree and session statuses
- Idea backlog
- Outbox state
- Ability to: create tasks, spawn sessions, send instructions to sessions, approve/deny, manage ideas

Responses appear **inline** below the bar, then the dashboard refreshes. The conversation doesn't accumulate — each interaction is mostly stateless (though jarvis can keep a short rolling context for follow-ups like "y" after a question).

**Context handoff** — when the smart bar dispatches work to a new session, it prepares a context bundle:

```
Context bundle for "Fix auth token expiry bug":
  - Task description: "Auth tokens expire prematurely after refresh..."
  - Source: Slack thread from alice (URL + key messages extracted)
  - Relevant code: src/auth/token.py (identified by smart bar)
  - Parent project context: "Auth System Rewrite"
  - Instructions: "Fix the bug, write tests, open a PR. Do not force push."
```

This becomes the `--append-system-prompt` for the worker session. The worker starts with full context, not a cold start.

**Key design choices:**

- **Dashboard is always visible** — the smart bar overlays, doesn't replace
- **Slash commands for speed** — when you know what you want, no AI latency
- **Natural language for everything else** — ambiguous requests, multi-step, context-gathering
- **Inline responses** — short, actionable, then back to dashboard
- **⚠ blocked floats to top** — needs your attention
- **←review marker** — session finished, output in outbox waiting for you

### Session Creation: Two Paths (carried from V1)

**Path A: Know what you're doing** — `/new "fix the auth bug"`
```
  > /new "fix the auth bug"

  Created session "fix-the-auth-bug"
  Worktree: .claude/worktrees/fix-the-auth-bug
  Branch: stack/fix-the-auth-bug
  Claude Code started. Attaching...
```
Worktree + branch + session created upfront. You're ready to code.

**Path B: Explore first** — `/chat` then formalize later
```
  > /chat

  Created session (untitled, no worktree)
  Claude Code started in main repo. Attaching...

  [Claude Code session — research, explore, ask questions]

  You: /jarvis-init "fix the auth bug"

  Jarvis: Created worktree + branch.
          Migrated session to new worktree.
          Session renamed to "fix-the-auth-bug".
```

- `/chat` creates a lightweight session — no worktree, no branch, runs in the main repo
- You explore, research, figure out what you're doing
- When ready to code: `/jarvis-init "title"` — creates worktree + branch, migrates the session
- Or just `/rename "auth investigation"` if it stays exploratory (no worktree needed)
- The session starts unfiled at root, you can `/move` it into a folder anytime

This is the same flow as V1, just integrated into the folder/session model.

### Attach Mode — Full Screen + Overlay Switcher

When attached, the session gets **full screen** — simple PTY passthrough, no panels. This keeps implementation simple and gives Claude Code's TUI the full terminal.

```
  I need to delete the build directory and rebuild:

    rm -rf build/ && npm run build

  Allow? (y/n)
```

Press **Ctrl-J** to open the **session switcher overlay**:

```
  I need to delete the build directory and rebuild:

    rm -rf buil┌─────────────────────────────────┐
               │ JARVIS — switch session          │
  Allow? (y/n)│                                   │
               │ ❯ ⚠ fix-bug-123      blocked     │
               │   ● implement-mid    working 2m  │
               │   ● INC-789          working 1m  │
               │   ✓ review-PR        done        │
               │   📤 1 draft pending              │
               │                                   │
               │ ↑↓ select  Enter switch           │
               │ d  detach  Esc dismiss            │
               └───────────────────────────────────┘
```

- **Ctrl-J** — open overlay (shows all sessions with live status)
- **↑↓** — navigate sessions
- **Enter** — switch to selected session (instant, no detach cycle)
- **d** — detach back to full dashboard
- **Esc** — dismiss overlay, stay in current session

This is the same pattern as tmux's `Ctrl-b s` (session picker). Simple to implement because:
- Attached mode = raw PTY byte passthrough (no terminal parsing needed)
- Overlay = a bubbletea popup rendered on top, disappears after selection
- No split panels, no PTY-in-a-panel, no focus routing

### Full Dashboard Mode

When no session is attached, the dashboard takes the full screen:

```
  JARVIS ─ 4 sessions · 1 draft                         12:34 PM

  ▼ Auth System Rewrite                                  2/4 done
    ▶ Phase 1                                            2/2 done
    ▼ Phase 2
      ● Implement middleware            working    writing migration    2m
      ◌ Migrate endpoints              queued
  ▼ Oncall
    ⚠ fix-bug-123                       blocked    Allow Bash: rm?    30s
    ● investigate-INC-789               working    reading logs        1m
  ● review-PR-1234                      working    reading diff        5m
  💡 2 ideas

  > _

  [enter] attach  [a]pprove  [/]search  [n]ew  [q]uit
```


### Outbox Review

```
  OUTBOX ─ PR #1234 review comments (3)

  ── Comment 1: src/auth/middleware.py:42 ──────────────
  This JWT validation doesn't check the `aud` claim.
  Consider adding audience validation to prevent
  token confusion attacks.

  ── Comment 2: src/auth/middleware.py:78 ──────────────
  The error message leaks internal service names.
  Suggest: "Authentication failed" instead of
  "Failed to reach auth-service-internal:8443"

  ── Comment 3: tests/test_auth.py:15 ─────────────────
  Missing test for expired token case.

  [s]end all  [e]dit #N  [d]iscard all  [b]ack
```

User reviews, optionally edits, then sends. Only then does Jarvis post to GitHub.

### External Event Flow

**Example: New PR review request**

```
1. github_watcher detects: PR #1234 assigned to user for review
2. Context registry: no existing session for github:universe#1234
3. Jarvis creates session: "review-PR-1234"
4. Claude Code launches with prompt:
   "Review PR #1234 in universe. Read the diff, understand the changes,
    and prepare review comments. Write comments to ~/.jarvis/sessions/<id>/outbox/pr-comments.json.
    Do NOT post comments directly."
5. Dashboard shows: ● review-PR-1234  working  reading diff...
6. Session completes → ✓ review-PR-1234  done  3 comments drafted  ←review
7. User opens outbox, reviews comments, sends
```

**Example: Slack message in existing thread**

```
1. slack_watcher detects: new message from @alice in thread p456
2. Context registry: session "task-def" already tracking slack:C0123/p456
3. Jarvis routes new message to existing session as additional context
4. Claude Code continues analysis with new information
5. Prepares updated response → outbox
```

### Auto-Intervention Policies

```yaml
# ~/.jarvis/config.yaml
policies:
  auto_approve:
    - tool: [Read, Grep, Glob, WebFetch]
      action: approve
    - tool: Bash
      command_matches: "npm install|npm run|pytest|make|cargo"
      action: approve
    - tool: Bash
      command_matches: "rm|drop|delete|force"
      action: ask_human

  watchers:
    github:
      poll_interval: 60s
      triggers:
        - event: review_requested
          action: create_session
          prompt_template: review_pr
    slack:
      poll_interval: 30s
      triggers:
        - event: direct_message
          action: create_session
          prompt_template: respond_slack
        - event: mention_in_thread
          action: route_or_create
          prompt_template: follow_thread
    pagerduty:
      poll_interval: 30s
      triggers:
        - event: incident_assigned
          action: create_session
          prompt_template: investigate_incident
```

---

## Folders, Sessions & Lifecycle

### Two Entity Types: Folders and Sessions

The organizational model is a **filesystem metaphor**. Two distinct entity types:

- **Folders** — pure organizational containers. No session, no worktree. Just a name and children. User creates and arranges these. Can nest arbitrarily.
- **Sessions** — leaf nodes. Each is a Claude Code session with a worktree, branches, and runtime status. Always a leaf. Cannot have children.

```
📁 Auth System Rewrite                       (folder — user created)
  📁 Phase 1                                 (folder — user created)
    ✓ Audit endpoints                        (session — done)
    ✓ Design JWT middleware                  (session — done)
  📁 Phase 2                                 (folder)
    ● Implement middleware                   (session — working)
    ◌ Migrate endpoints                      (session — queued)
📁 Oncall                                    (folder)
  ⚠ fix-bug-123                              (session — needs approval)
  ● investigate-INC-789                      (session — working)
● review-PR-1234                             (session — unfiled, top-level)
💡 Rate limiting for public API              (idea — not a session yet)
```

**Rules:**

- Folders contain folders and/or sessions — arbitrary nesting depth
- Sessions are **always leaves** — they do work, they don't organize
- Unfiled sessions are fine — top-level, no parent folder, like files on a desktop
- User owns the folder structure — they create, rename, rearrange freely
- Folders have no status of their own — they display an aggregate (e.g., `3/5 done`)

### Manual vs. System-Triggered Creation

**Manual** — user creates folders and sessions directly:

```
  > /folder "Auth System Rewrite"
  > /folder "Phase 1"                        (under selected folder)
  > /new "Audit existing endpoints"           (session, under selected folder)
```

**System-triggered** — watchers detect external events and create sessions. These start **unfiled** (top-level) unless routing rules place them.

### Session Routing

When a system event arrives, jarvis decides where to put the new session:

```
New event arrives
  │
  ├─ 1. Exact context match?
  │     Context registry: "slack:C0123/p456" → existing session
  │     → Route to existing session (append context, don't create new)
  │
  ├─ 2. Routing rule match?
  │     Config says: slack #oncall-storage → 📁 Oncall
  │     → Create session inside that folder
  │
  ├─ 3. No match
  │     → Create unfiled session (top-level)
  │     → User can /move it later
  │
```

**Routing config:**

```yaml
# ~/.jarvis/config.yaml
routing:
  rules:
    - match:
        source: slack
        channel: "#oncall-storage"
      place_in: "Oncall"                 # folder name

    - match:
        source: github
        repo: "universe"
      place_in: "Universe PRs"

    - match:
        source: pagerduty
      place_in: "Oncall"
```

Routing is **deterministic** — config rules, not AI guessing. Simple, predictable, no flakiness. User defines the rules, jarvis follows them. No match = unfiled.

### Session Lifecycle

```
              ┌──────────┐
              │  queued   │  (created, no session running yet)
              └────┬─────┘
                   │ launch
                   ▼
              ┌──────────┐
         ┌───►│  active   │◄───┐  (sidecar alive, Claude Code running)
         │    └──┬────┬──┘    │
         │       │    │       │ resume (new sidecar + claude --resume)
         │       │    │       │
         │       │    ▼       │
         │       │ ┌──────────┐
         │       │ │suspended │  (sidecar dead — instance restart)
         │       │ └──────────┘
         │       │          │ reopen
         │       │ finish   │
         │       ▼          │
         │    ┌──────────┐  │
         │    │   done   │──┘
         │    └────┬─────┘
         │         │ archive
         │         ▼
         │    ┌──────────┐
         └────│ archived │  (hidden, data preserved)
              └──────────┘
```

- **queued** — planned but not started. No sidecar, no worktree yet.
- **active** — sidecar running, Claude Code session alive. Live status from sidecar.
- **suspended** — sidecar dead (instance restarted). Last-known status derived from session JSONL. Resume spawns new sidecar + `claude --resume`.
- **done** — session exited or user marked complete. Visible in dashboard (greyed out).
- **archived** — hidden from dashboard. Data preserved on disk. Can be unarchived.

### Dashboard

```
  JARVIS ─ 4 sessions · 1 draft                         12:34 PM

  ▼ Auth System Rewrite                                  2/4 done
    ▶ Phase 1                                            2/2 done
    ▼ Phase 2
      ● Implement middleware            working    writing migration    2m
      ◌ Migrate endpoints              queued
  ▼ Oncall
    ⚠ fix-bug-123                       blocked    Allow Bash: rm?    30s
    ● investigate-INC-789               working    reading logs        1m
  ● review-PR-1234                      working    reading diff        5m
  💡 2 ideas

  > _

  [enter] attach  [a]pprove  [/]search  [n]ew  [q]uit
```

- **▼ / ▶** collapse/expand folders
- Aggregate counts on folders (`2/4 done`)
- Unfiled sessions at root level
- `⚠` floats to top — needs attention
- Search (`/`) filters across all folders and sessions

### Archiving

```
  > /archive
  Archive "fix-bug-123" and clean up worktree? (y/n) y
  Archived. Use /ls --archived to view.

  > /archive --done
  Archive 3 done sessions? (y/n) y

  > /archive --folder "Phase 1"
  Archive folder "Phase 1" and 2 sessions inside? (y/n) y
```

- Archived sessions: hidden from dashboard, data preserved
- Archived folders: hidden along with all contents
- Worktree cleaned up if branch is merged, otherwise kept
- `/unarchive` to restore

### Commands


| Command | Where | Action |
|---------|-------|--------|
| `/new "title"` | Dashboard | Create session with worktree + branch |
| `/chat` | Dashboard | Create lightweight session (no worktree, main repo) |
| `/jarvis-init "title"` | Inside session | Upgrade chat → worktree + branch, migrate session |
| `/rename "title"` | Inside session | Rename session (no worktree change) |
| `/folder "title"` | Dashboard | Create folder |
| `/move <name> under <folder>` | Dashboard | Move session or folder |
| `/attach` / Enter | Dashboard | Attach to selected session |
| `/approve` | Dashboard | Approve blocked session's pending prompt |
| `/archive` | Dashboard | Archive selected session/folder |
| `/archive --done` | Dashboard | Archive all done sessions |
| `/unarchive` | Dashboard | Restore archived item |
| `/ls --archived` | Dashboard | Show archived items |
| `/search <query>` | Dashboard | Search sessions, folders, ideas |
| `/outbox` | Dashboard | View pending drafts |
| `/q` | Dashboard | Quit jarvis (sessions continue in background) |
| Ctrl-\ | Attached | Detach back to dashboard |


---

## Data Model

### Two Entity Types

**folder.yaml** — organizational container:

```yaml
id: "f1a2b3c4"
type: folder
name: "Auth System Rewrite"
parent_id: null              # null = top-level
children:                    # ordered list, can mix folders and sessions
  - { type: folder, id: "f5e6d7c8" }
  - { type: session, id: "a1b2c3d4" }
  - { type: session, id: "b2c3d4e5" }
status: active               # active | archived
created_at: "2026-03-08T10:00:00Z"
archived_at: null
```

**session.yaml** — a Claude Code session (always a leaf):

```yaml
id: "a1b2c3d4"
type: session
name: "investigate the kafka schema issue"
status: active               # queued | active | suspended | done | archived
parent_id: "f1a2b3c4"       # folder ID, or null if unfiled

# Worktree & branches
cwd: "/home/user/universe/.claude/worktrees/investigate-kafka-schema-issue"
branches:
  - stack/kafka-schema-fix

# Claude Code session (for --resume across instance restarts)
claude_session_id: "298e5fe3-b33c-4896-9a85-7c65625407df"

# Sidecar (runtime — cleared on instance restart)
sidecar:
  pid: 12345
  socket: "/tmp/jarvis/task-a1b2c3d4.sock"
  started_at: "2026-03-19T10:00:00Z"
  state: "working"          # working | waiting_for_approval | idle | exited
  transport: "pty"          # pty (interactive with PTY) | headless (Claude Code --print, no PTY)

# Persisted by sidecar periodically — survives instance restart
last_known_state: "working"           # last state before sidecar died
last_known_detail: "writing migration file..."
last_activity_at: "2026-03-19T10:05:00Z"

# External context binding
context:
  type: "github_pr"         # github_pr | slack_thread | pagerduty_incident | manual
  ref: "databricks-eng/universe#1234"
  url: "https://github.com/databricks-eng/universe/pull/1234"

# Outbox
outbox:
  - type: "github_pr_comments"
    target: "databricks-eng/universe#1234"
    status: "pending_review"   # pending_review | sent | discarded
    file: "/tmp/jarvis/outbox/task-a1b2c3d4-comments.json"

# Timestamps
created_at: "2026-03-08T10:00:00Z"
updated_at: "2026-03-08T12:00:00Z"
archived_at: null
```

### Storage Layout

```
~/.jarvis/
  config.yaml                 # global config, policies, routing rules
  folders/
    <folder-id>.yaml          # folder metadata + children list
  sessions/
    <session-id>/
      session.yaml            # session metadata
      references.yaml
      notes.md
      outbox/                 # prepared responses
        pr-comments.json
        slack-reply.json
  context_registry.yaml       # external context → session ID mapping
```

Folders and sessions stored **flat on disk**. Hierarchy is maintained via `parent_id` (on sessions) and `children` list (on folders). Re-parenting = update two YAML files.

### context_registry.yaml

```yaml
# Maps external context → task ID (for deduplication and routing)
contexts:
  "github:databricks-eng/universe#1234": "a1b2c3d4"
  "slack:C0123/p456": "b2c3d4e5"
  "pagerduty:INC-789": "c3d4e5f6"
```

---

## Implementation Phases

### Phase 1: Sidecar + Attach/Detach ✅ DONE

- Sidecar daemon process with PTY + Unix socket (Go, `internal/sidecar/`)
- JSON-over-socket protocol (`internal/protocol/`)
- Session manager — spawn sidecar instead of blocking subprocess
- Attach/detach — raw PTY passthrough with Ctrl-\ detach
- Sidecar state persistence — periodically write status to session.yaml
- Resume after instance restart — detect dead sidecars, derive status from JSONL, `claude --resume`
- Ring buffer (10K lines) for attach catch-up

**Outcome:** Sessions survive jarvis exit. Sessions recoverable after instance restart via `claude --resume`. Single session works end-to-end through sidecar.

### Phase 2: Multi-Session Dashboard ✅ DONE

- Bubble Tea TUI dashboard (`internal/tui/`) — folder tree + session statuses
- Folder management — create, rename, delete, nest
- Live status polling — periodic `get_status` to all active sidecars
- Suspended status — read JSONL tail for dead sidecars
- `⚠ needs attention` — float blocked sessions to top
- Search — filter across folders and sessions by name
- Session switching — navigate and attach from dashboard
- Worktree + branch creation via `jarvis init` (git-stack with fallback)

**Outcome:** User can see all sessions at a glance, find what needs attention, search, and switch between sessions. The core "commander" loop works.

### Phase 3: Auto-Approve & Intervention Policies ✅ DONE

> **Why this is next:** The biggest daily pain point is sessions blocked on tool-approval prompts. The sidecar already detects approval patterns (`DetectState` in `internal/sidecar/status.go`); this phase adds the ability to auto-respond based on configurable policies. Highest ROI, lowest implementation cost.

- Policy engine — parse auto-approve rules from `~/.jarvis/config.yaml`
- Sidecar auto-respond — when an approval prompt matches a policy, send `y\n` automatically
- Deny-list patterns — certain commands (rm, drop, force push) always require human approval via `ask_human`
- Quick-approve from dashboard — press `a` on a blocked session to approve without attaching

```yaml
# ~/.jarvis/config.yaml
policies:
  auto_approve:
    - tool: [Read, Grep, Glob, WebFetch, WebSearch]
      action: approve
    - tool: Bash
      command_matches: "npm install|npm run|pytest|make|cargo|go test|go build"
      action: approve
    - tool: Bash
      command_matches: "rm|drop|delete|force|--force"
      action: ask_human
    - tool: Edit
      action: approve
    - tool: Write
      action: approve
```

**Outcome:** Routine prompts auto-handled. Dangerous operations surfaced for human decision. Sessions run unattended for much longer.

### Phase 4: External System Integration

> **Why after auto-approve:** Watchers create sessions automatically, and those sessions will hit approval prompts. Auto-approve (Phase 3) must be in place first, otherwise auto-created sessions just pile up as "blocked" with nobody watching.

- `internal/watcher/` — background goroutines that poll external systems
- Context registry — `~/.jarvis/context_registry.yaml` maps external refs to session IDs (dedup)
- Routing rules in config — deterministic placement into folders
- Prompt templates per trigger type — what instructions each auto-created session gets

**Watchers:**

| Watcher | Source | Trigger | Action |
|---------|--------|---------|--------|
| `github` | GitHub API / MCP | Review request assigned | Create session: review PR, prepare comments |
| `slack` | Slack API / MCP | DM or mention | Create or route: analyze context, prepare response |
| `pagerduty` | PagerDuty API / MCP | Incident assigned | Create session: investigate, prepare response |
| `jira` | Jira API / MCP | Ticket assigned/updated | Create or route: analyze, prepare response |

Watchers run as goroutines inside the jarvis process. They die when jarvis exits, restart when jarvis launches. The context registry (on disk) prevents duplicate session creation across restarts.

**Outcome:** Jarvis automatically creates sessions in response to external events. No more manual checking of Slack/GitHub/PD/Jira. Existing sessions receive follow-up context.

**Estimated effort:** ~2,700 lines (registry 400 + 4 watchers 2000 + routing 300)

### Phase 5: Outbox

> **Why after watchers:** The outbox is most useful when sessions are auto-created by watchers. A PR review session auto-created by the GitHub watcher produces draft comments → outbox → you review and send. Without watchers, the outbox is just a manual "save draft" feature.

- Outbox store — prepared responses per session (YAML/JSON under `~/.jarvis/sessions/<id>/outbox/`)
- Outbox TUI — review, edit, send, discard
- GitHub integration — post approved comments via MCP
- Slack integration — send approved messages via MCP
- Audit log — record what was sent, when, original vs. edited

**Outcome:** Jarvis prepares responses; human reviews and sends. Full audit trail. No external action without approval.

**Estimated effort:** ~1,900 lines (store 600 + TUI 800 + integrations 500)

### Phase 6: Polish & UX

- Session overlay switcher — Ctrl-J popup to switch sessions without detaching (like tmux `Ctrl-b s`)
- Session logging — persist all PTY output to file (not just ring buffer)
- Health monitoring — detect stuck/hung sessions (no output for 10+ minutes)
- Graceful shutdown — signal all sidecars on `jarvis stop`
- Archiving — hide done sessions, clean up worktrees for merged branches
- Dashboard slash commands — `/approve`, `/archive`, `/move` from the smart bar

**Outcome:** Polished daily-driver experience. No rough edges.

**Estimated effort:** ~1,400 lines

### Projected Code Size

| Phase | New Lines | Cumulative |
|-------|-----------|------------|
| Phase 1+2 (done) | ~5,500 | ~5,500 |
| Phase 3: Auto-approve (done) | ~800 | ~6,300 |
| Phase 4: Watchers | ~2,700 | ~9,000 |
| Phase 5: Outbox | ~1,900 | ~10,900 |
| Phase 6: Polish | ~1,400 | **~12,300** |

---

## Non-Goals (Explicitly Out of Scope)

- **Intra-session orchestration** — Claude Code already has sub-agents (agent teams) for parallelizing within a session. Jarvis does not replicate this.
- **Autonomous external actions** — Jarvis never posts, comments, merges, or sends on behalf of the user without explicit per-action approval.
- **GUI** — Jarvis is CLI/TUI only. No Electron, no browser.
- **Multi-user** — Jarvis is a single-user tool. No auth, no sharing, no collaboration features.

---

## Open Questions

1. **Max concurrent sessions** — should there be a limit? Claude Code sessions can be resource-heavy.
2. **Output buffering size** — how much scrollback should the sidecar retain? 10K lines? 100K?
3. **Watcher implementation** — polling vs. webhooks? Polling is simpler but adds latency. Webhooks require a local server.
4. **Outbox format** — structured JSON per output type? Or freeform markdown that Jarvis posts verbatim?
5. **Session lifetime** — when should a session auto-terminate? After idle for N minutes? After task completion?
6. **Watcher dedup window** — how long to suppress duplicate events for the same context?
7. **Watcher availability** — watchers run inside jarvis and die when jarvis exits. External events are missed until jarvis restarts. Is this acceptable, or should watchers be a separate always-on daemon?

---

## Tech Stack

No new external dependencies required for core:


| Component       | Library                                                     |
| --------------- | ----------------------------------------------------------- |
| Language        | Go 1.24                                                     |
| PTY management  | `creack/pty` (Go PTY library)                               |
| Unix sockets    | `net` (stdlib)                                              |
| Daemonization   | `os/exec` + `syscall.Setsid` (stdlib)                       |
| TUI (dashboard) | Bubble Tea + Bubbles + Lipgloss (charmbracelet)             |
| Config/storage  | `gopkg.in/yaml.v3`                                         |
| External APIs   | MCP servers (GitHub, Slack, PagerDuty — already configured) |


---

## Success Criteria

- User can run 3+ Claude Code sessions concurrently from one Jarvis instance
- Quitting Jarvis does not kill running sessions
- Restarting Jarvis reconnects to all running sessions within 1 second
- Permission prompts are surfaced in the dashboard within 2 seconds
- Auto-approve policies work for configured patterns with zero false positives
- A new GitHub review request triggers a session within 60 seconds of detection
- Slack messages to an existing thread are routed to the correct session (no duplicates)
- No external action is taken without explicit human approval in the outbox

