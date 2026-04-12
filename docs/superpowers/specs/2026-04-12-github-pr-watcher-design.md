# GitHub PR Watcher — Design Spec

## Overview

A new Jarvis watcher that monitors GitHub PR activity and creates dedicated Claude sessions per PR. Uses the hybrid approach: GitHub Notifications API for cheap event discovery, then targeted `gh api` calls for PR details and comments.

Two use cases:
1. **Review requests** — someone requests your review → Claude auto-reviews the PR and drafts comments
2. **Author comments** — someone comments on your PR → Claude reads the feedback and drafts responses

One Claude session per PR. Follow-up activity on the same PR routes to the existing session.

## Scope

- Scoped to `databricks-eng/universe` only
- Notification reasons: `review_requested` and `author` only
- No MCP — uses `gh` CLI directly (already authenticated)

## Data Flow

```
Poll cycle (every N seconds):

  1. PR Discovery Scan (link manual sessions)
     ├── git stack ls → branch-to-PR map
     ├── store.ListSessions(active|suspended) → sessions with worktree_dir
     ├── For each: git -C <worktree_dir> branch --show-current
     ├── Match branch → PR# via map
     └── registry.Register("github:databricks-eng/universe#<PR>", sessionID)

  2. gh api /notifications?since=<last_poll_ts>&all=false
     ├── Filter: subject.type == "PullRequest"
     ├── Filter: repository.full_name == "databricks-eng/universe"
     ├── Filter: reason ∈ {review_requested, author}
     └── Dedup by PR number within poll

  3. For each matching notification:
     ├── Extract PR# from subject.url (/pulls/(\d+)$)
     ├── gh api /repos/{owner}/{repo}/pulls/{PR#}
     │   → title, author, state, html_url
     ├── Skip if state == "closed" or "merged"
     ├── gh api /repos/{owner}/{repo}/pulls/{PR#}/comments?since=<ts>
     │   → inline review comments (file-level)
     ├── gh api /repos/{owner}/{repo}/issues/{PR#}/comments?since=<ts>
     │   → general PR conversation comments
     ├── Filter comments: skip own user, skip bots
     └── If no new human comments on an "author" notification → skip

  4. Registry lookup: "github:databricks-eng/universe#<PR#>"
     ├── Found + session exists → sendInput (follow-up)
     ├── Found + session deleted → unregister, recreate
     └── Not found → spawn new session
```

## Configuration

In `~/.jarvis/config.yaml`:

```yaml
watchers:
  github:
    enabled: true
    owner: "databricks-eng"
    repo: "universe"
    username: "jianyu-zhou_data"
    poll_interval: 60          # seconds
    folder: "GitHub PRs"
    reasons:                   # notification reasons to act on
      - review_requested
      - author
    ignore_users: []           # usernames to skip (e.g. bots)
```

## Event Types & Session Prompts

### Review Request (reason: review_requested)

**Session name:** `gh: Review PR #1234 — <title>`

**Initial prompt:**
```
Please review this GitHub PR: <html_url>

Read the full diff and understand the changes. Provide a thorough code review
covering correctness, edge cases, and potential issues. Draft review comments
that I can post.

Do NOT post any comments, approve, or take any external-facing actions.
```

### Author Comment (reason: author)

**Session name:** `gh: PR #1234 — <title>`

**Initial prompt:**
```
Someone left comments on my PR: <html_url>

New comments since last check:
- <user> on <file>:<line>: "<body>"
- <user> (general comment): "<body>"

Read the PR diff for context, understand the review feedback, and draft
responses to each comment. If code changes are suggested, explain what
changes would address the feedback.

Do NOT post any comments or take any external-facing actions.
```

### Follow-up (new activity on PR with existing session)

Injected via `sendInput`:
```
[New comments on PR #1234 at <timestamp>]:
- <user> on <file>:<line>: "<body>"
- <user> (general comment): "<body>"

Please read the new comments and draft responses.
```

## PR Discovery: Linking Manual Sessions

When a user creates a session via `/jarvis-init`, works on code, and pushes a PR, the watcher must link that PR to the existing session so comments route there instead of creating a duplicate.

**Algorithm (runs at the start of each poll cycle):**

1. Run `git stack ls` in the repo dir → parse branch name + PR URL pairs into a `map[string]int` (branch → PR number)
2. `store.ListSessions(&SessionFilter{StatusIn: [active, suspended]})` → filter to sessions where `WorktreeDir` is set and is not the repo root
3. For each such session: `git -C <WorktreeDir> branch --show-current` → branch name
4. Look up branch in the map → if found, build context key `github:databricks-eng/universe#<PR#>`
5. `registry.Lookup(key)` → if not found, `registry.Register(key, session.ID)`

Idempotent: once registered, subsequent polls skip re-registration. Cost: 1 `git stack ls` + N `git branch` calls (N = unlinked sessions with worktrees, typically 0 after first run).

## Architecture & File Structure

```
internal/watch/
├── helpers.go          # NEW — extracted shared code (ensureFolder, placeSessionInFolder, sendInputToSession)
├── github_daemon.go    # NEW — GitHubDaemon (poll loop, discovery scan, event routing)
├── github.go           # NEW — GitHubEvent, GitHubPoller (gh CLI wrapper), PRComment
├── github_test.go      # NEW — unit tests
├── daemon.go           # MODIFY — remove shared helpers (now in helpers.go)
├── gmail_daemon.go     # existing, unchanged
├── registry.go         # existing, reused as-is
├── mcpclient.go        # existing, not used by GitHub watcher
├── slack.go            # existing, unchanged
└── ...

internal/config/
└── config.go           # MODIFY — add GitHubWatcherConfig, wire into WatchersConfig

cmd/watch/
└── main.go             # MODIFY — add GitHub daemon startup
```

### Key Types

```go
// github.go

type PRComment struct {
    User      string
    Body      string
    Path      string    // file path (empty for general comments)
    Line      int       // line number (0 for general comments)
    CreatedAt time.Time
}

type GitHubEvent struct {
    Owner    string
    Repo     string
    PRNumber int
    PRTitle  string
    PRURL    string       // html_url
    PRAuthor string
    Reason   string       // "review_requested" or "author"
    Comments []PRComment  // new human comments from detail fetch
    NotifID  string
    Timestamp time.Time
}

// github_daemon.go

type GitHubDaemon struct {
    cfg      *config.Config
    mgr      *session.Manager
    registry *Registry
    poller   *GitHubPoller
    folderID string
    polling  bool
}
```

### GitHubPoller

Wraps `gh api` via `exec.Command`. No library dependencies.

- `Poll(ctx)` → calls `gh api notifications`, filters, fetches details, returns `[]GitHubEvent`
- `FetchPRDetails(owner, repo, number)` → PR metadata
- `FetchNewComments(owner, repo, number, since)` → review + issue comments, filtered
- `DiscoverPRs(repoDir)` → runs `git stack ls`, returns `map[string]int` (branch → PR#)

Auth handled by `gh` CLI automatically. Startup validates with `gh auth status`.

## Edge Cases

1. **PR closed/merged** — skip notification; don't create session. Existing sessions left alone (user marks done).
2. **Session deleted by user** — registry lookup succeeds but `store.GetSession()` fails → unregister stale entry → recreate.
3. **No new human comments** — `author` notification but only bot/CI activity → skip entirely.
4. **Multiple notifications for same PR in one poll** — dedup by PR number, process once.
5. **First run (no checkpoint)** — `since` defaults to current time (RFC3339). Only future notifications picked up.
6. **`gh` CLI not authenticated** — `NewGitHubPoller` runs `gh auth status` at startup. Fails with clear error.
7. **Worktree_dir == repo root** — discovery scan skips these (master branch, no PR).
8. **`git stack ls` not available** — if the command fails, log warning and skip discovery (notification-based routing still works for watcher-created sessions).

## Timestamp Management

One global `last_poll_ts` (RFC3339) stored in `~/.jarvis/watch/github/registry.yaml`. Used for both the Notifications API `since` parameter and the comment fetch `since` parameter. This means comment fetches may return some already-seen comments, but the follow-up routing deduplicates: if a session already exists and the comments are the same ones that created it, the event is skipped (no new human comments after filtering).

## Rate Limiting

Per poll cycle: 1 (notifications) + N × 3 (PR + review comments + issue comments) where N = new notifications. Typical N is 0–3, so ~10 API calls max. GitHub allows 5,000/hour for authenticated users — plenty of headroom at 60s intervals.

## Testing

Unit tests (`github_test.go`):
- `TestGitHubEventContextKey` — verify key format `github:owner/repo#number`
- `TestGitHubEventSessionName` — verify name format for review_requested vs author
- `TestGitHubEventInitialPrompt` — verify prompt includes URL and safety guardrail
- `TestParseNotifications` — verify filtering by type, repo, reason
- `TestParseStackLS` — verify branch→PR parsing from `git stack ls` output
- `TestSkipClosedPR` — verify closed/merged PRs are filtered out
- `TestDedupSamePR` — verify multiple notifications for same PR produce one event
