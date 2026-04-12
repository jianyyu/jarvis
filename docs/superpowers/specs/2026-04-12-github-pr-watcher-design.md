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
     ├── gh pr list --author @me (primary) or git stack ls (secondary)
     │   → branch-to-PR map
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
     │   → title, author, state, html_url, head.ref
     ├── Skip if state == "closed" or "merged"
     ├── gh api /repos/{owner}/{repo}/pulls/{PR#}/reviews
     │   → review verdicts (APPROVED, CHANGES_REQUESTED, COMMENTED)
     ├── gh api /repos/{owner}/{repo}/pulls/{PR#}/comments?since=<per_PR_ts>
     │   → inline review comments (file-level)
     ├── gh api /repos/{owner}/{repo}/issues/{PR#}/comments?since=<per_PR_ts>
     │   → general PR conversation comments
     ├── Filter comments: skip own username, skip bot users (user.type == "Bot"
     │   OR username ∈ ignore_users config)
     ├── If no new human comments on an "author" notification → skip
     └── Mark notification as read: PATCH /notifications/threads/{thread_id}

  4. Registry lookup: "github:databricks-eng/universe#<PR#>"
     ├── Found + session exists + sidecar alive → sendInput (follow-up)
     ├── Found + session exists + sidecar dead  → mgr.Resume(sess), wait, sendInput
     ├── Found + session deleted → unregister, recreate
     └── Not found → spawn new session

  5. Update per-PR comment timestamp in registry
  6. Update global last_poll_ts in registry
```

## Configuration

In `~/.jarvis/config.yaml`:

```yaml
watchers:
  github:
    enabled: true
    owner: "databricks-eng"
    repo: "universe"
    username: "jianyu-zhou_data"    # used to filter out your own comments
    poll_interval: 60               # seconds
    folder: "GitHub PRs"
    reasons:                        # notification reasons to act on
      - review_requested
      - author
    ignore_users:                   # usernames to skip in addition to bot detection
      - "databricks-staging-ci-emu-1[bot]"
      - "databricks-ci-emu-2[bot]"
```

**`username` field:** Used solely for comment filtering — skip your own comments when building the event. The Notifications API already scopes to the authenticated `gh` user, so `username` is not needed for notification discovery.

**Bot detection:** Two layers:
1. GitHub API `user.type == "Bot"` — catches all GitHub Apps and bot accounts automatically
2. `ignore_users` config list — catches non-bot accounts you want to skip (e.g. CI service accounts that aren't flagged as bots)

## Event Types & Session Prompts

### Review Request (reason: review_requested)

**Session name:** `gh: Review PR #1234 — <title>`

**Initial prompt:**
```
Please review this GitHub PR: <html_url>

Run `gh pr diff <number>` to read the full diff. Provide a thorough code
review covering correctness, edge cases, and potential issues. Draft review
comments that I can post.

Do NOT post any comments, approve, or take any external-facing actions.
```

### Author Comment (reason: author)

**Session name:** `gh: PR #1234 — <title>`

**CWD selection:** The session must run in the correct worktree so Claude can make code changes if needed:
1. Get the PR's head ref from the detail fetch (`gh api .../pulls/{n}` → `.head.ref`)
2. Find the matching local worktree: run `git worktree list --porcelain` in the repo, match by branch name. If multiple worktrees match the same branch (shouldn't happen, but possible with detached HEAD), pick the first match.
3. If found → spawn session with `cwd = <worktree path>`
4. If not found → spawn in repo root, include branch name in prompt for Claude to handle

**Initial prompt (with review verdict):**
```
Someone left comments on my PR: <html_url>

Review verdict: <APPROVED | CHANGES_REQUESTED | COMMENTED | none>

New comments since last check:
- <user> on <file>:<line>: "<body>"
- <user> (general comment): "<body>"

Read the PR diff with `gh pr diff <number>` for context. Understand the review
feedback and draft responses to each comment. If code changes are suggested,
explain what changes would address the feedback.

IMPORTANT: Verify you are on the correct branch (<head_ref>) before making
any code changes. Run `git branch --show-current` to confirm.

Do NOT post any comments or take any external-facing actions.
```

The review verdict (from `GET /repos/{owner}/{repo}/pulls/{PR#}/reviews`, most recent non-COMMENTED review) is included because "changes requested" context changes how Claude should respond vs. a simple question comment.

### Follow-up (new activity on PR with existing session)

**Resume flow:** If the session's sidecar is dead (suspended):
1. Call `mgr.Resume(sess)` to restart sidecar with `claude --resume`
2. Poll for socket readiness: check `sidecar.SocketPath(sessID)` exists + responds to ping, up to 10 seconds (1s interval)
3. On timeout → log warning, skip this event. The notification stays unread and will be reprocessed next poll cycle. The per-PR comment timestamp is NOT advanced, so no comments are lost.

Injected via `sendInput`:
```
[New comments on PR #1234 at <timestamp>]:

Review verdict: <APPROVED | CHANGES_REQUESTED | COMMENTED | none>

- <user> on <file>:<line>: "<body>"
- <user> (general comment): "<body>"

Run `gh pr diff <number>` if you need to refresh context on the changes.
Please read the new comments and draft responses.
```

## Timestamp Management

Two levels of timestamps, stored in `~/.jarvis/watch/github/registry.yaml`:

1. **Global `last_poll_ts`** (RFC3339): Used as the `since` parameter for the Notifications API. Advanced at the end of every poll cycle to the current time.

2. **Per-PR `last_comment_ts`** (map[contextKey]→RFC3339): Used as the `since` parameter for comment fetches on each specific PR. Advanced after successfully processing comments for that PR.

This avoids the subtle bug where a delayed notification could cause missed comments: PR #100 triggers at poll T1 with comments fetched since T0. At poll T3, PR #100 gets another notification — comments are fetched since T1 (per-PR timestamp), not since T3 (global timestamp).

**Registry file format:**
```yaml
contexts:
  "github:databricks-eng/universe#1234": "session-id-abc"
  "github:databricks-eng/universe#5678": "session-id-def"
last_poll_ts: "2026-04-12T01:00:00Z"
comment_timestamps:
  "github:databricks-eng/universe#1234": "2026-04-12T00:30:00Z"
  "github:databricks-eng/universe#5678": "2026-04-12T00:45:00Z"
```

**Note:** The registry file format adds a `comment_timestamps` map. This requires a small change to the `registryFile` struct in `registry.go` — add a `CommentTimestamps map[string]string` field. The existing Slack watcher doesn't use this field, so it's backwards-compatible.

## Marking Notifications as Read

After successfully processing a notification (session created or follow-up sent), mark it as read:

```
PATCH /notifications/threads/{thread_id}
```

The `thread_id` is extracted from the notification's `id` field. This is critical: without it, the `all=false` filter is ineffective and the same notifications reappear every poll cycle.

**On failure to process** (e.g., sidecar resume timeout): do NOT mark as read. The notification will be reprocessed on the next poll, giving the system another chance.

## PR Discovery: Linking Manual Sessions

When a user creates a session via `/jarvis-init`, works on code, and pushes a PR, the watcher must link that PR to the existing session so comments route there instead of creating a duplicate.

**Algorithm (runs at the start of each poll cycle):**

1. Build branch→PR map using `gh pr list`:
   ```
   gh pr list --author @me --repo {owner}/{repo} --state open --json number,headRefName
   ```
   This is the primary method — works without `git stack ls`, handles non-stacked PRs, and returns clean JSON.

   Fallback: if `gh pr list` fails, try `git stack ls` in the repo dir and parse branch + PR URL pairs. If both fail, log warning and skip discovery.

2. `store.ListSessions(&SessionFilter{StatusIn: [active, suspended]})` → filter to sessions where `WorktreeDir` is set and is not the repo root

3. For each such session: `git -C <WorktreeDir> branch --show-current` → branch name

4. Look up branch in the map → if found, build context key `github:databricks-eng/universe#<PR#>`

5. `registry.Lookup(key)` → if not found, `registry.Register(key, session.ID)`

Idempotent: once registered, subsequent polls skip re-registration. Cost: 1 `gh pr list` + N `git branch` calls (N = unlinked sessions with worktrees, typically 0 after first run).

## Architecture & File Structure

```
internal/watch/
├── helpers.go          # NEW — extracted shared code (ensureFolder, placeSessionInFolder, sendInputToSession)
├── github_daemon.go    # NEW — GitHubDaemon (poll loop, discovery scan, event routing)
├── github.go           # NEW — GitHubEvent, GitHubPoller (gh CLI wrapper), PRComment
├── github_test.go      # NEW — unit tests
├── daemon.go           # MODIFY — remove shared helpers (now in helpers.go)
├── gmail_daemon.go     # existing, unchanged
├── registry.go         # MODIFY — add CommentTimestamps field to registryFile
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

type ReviewVerdict string

const (
    VerdictApproved         ReviewVerdict = "APPROVED"
    VerdictChangesRequested ReviewVerdict = "CHANGES_REQUESTED"
    VerdictCommented        ReviewVerdict = "COMMENTED"
    VerdictNone             ReviewVerdict = ""
)

type GitHubEvent struct {
    Owner    string
    Repo     string
    PRNumber int
    PRTitle  string
    PRURL    string        // html_url
    PRAuthor string
    HeadRef  string        // branch name (e.g. "stack/fix-...")
    Reason   string        // "review_requested" or "author"
    Verdict  ReviewVerdict // most recent review verdict (for author events)
    Comments []PRComment   // new human comments from detail fetch
    NotifID  string        // notification thread ID (for marking read)
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
- `FetchPRDetails(owner, repo, number)` → PR metadata including head.ref
- `FetchReviews(owner, repo, number)` → review verdicts, returns most recent non-COMMENTED verdict
- `FetchNewComments(owner, repo, number, since)` → review + issue comments, filtered
- `DiscoverPRs(owner, repo)` → `gh pr list --author @me`, returns `map[string]int` (branch → PR#)
- `DiscoverPRsFallback(repoDir)` → `git stack ls`, returns `map[string]int` (branch → PR#)
- `MarkNotificationRead(threadID)` → `PATCH /notifications/threads/{threadID}`

Auth handled by `gh` CLI automatically. Startup validates with `gh auth status`.

## Edge Cases

1. **PR closed/merged** — skip notification; don't create session. Existing sessions left alone (user marks done).
2. **Session deleted by user** — registry lookup succeeds but `store.GetSession()` fails → unregister stale entry → recreate.
3. **No new human comments** — `author` notification but only bot/CI activity → skip entirely. Still mark notification as read to avoid reprocessing.
4. **Multiple notifications for same PR in one poll** — dedup by PR number, process once.
5. **First run (no checkpoint)** — `since` defaults to current time (RFC3339). Only future notifications picked up.
6. **`gh` CLI not authenticated** — `NewGitHubPoller` runs `gh auth status` at startup. Fails with clear error.
7. **Worktree_dir == repo root** — discovery scan skips these (master branch, no PR).
8. **PR discovery fallback** — primary: `gh pr list --author @me`. If fails, fallback to `git stack ls`. If both fail, log warning and skip discovery. Watcher-created sessions still route correctly via registry.
9. **Sidecar resume timeout** — poll socket for up to 10s. On timeout, skip event (notification stays unread, retried next cycle). Per-PR comment timestamp not advanced.
10. **Multiple worktrees for same branch** — pick the first match from `git worktree list --porcelain`. This shouldn't happen in normal git usage.
11. **Stale registry entries for closed PRs** — periodic cleanup: during discovery scan, check `gh pr list` results for closed/merged PRs that still have registry entries. Unregister them. This keeps the registry clean without a separate cleanup loop.

## Rate Limiting

Per poll cycle: 1 (notifications) + 1 (discovery: `gh pr list`) + N × 4 (PR + reviews + review comments + issue comments) + N × 1 (mark read) where N = new notifications. Typical N is 0–3, so ~16 API calls max. GitHub allows 5,000/hour for authenticated users — plenty of headroom at 60s intervals.

## Testing

Unit tests (`github_test.go`):
- `TestGitHubEventContextKey` — verify key format `github:owner/repo#number`
- `TestGitHubEventSessionName` — verify name format for review_requested vs author
- `TestGitHubEventInitialPrompt` — verify prompt includes URL, verdict, and safety guardrail
- `TestParseNotifications` — verify filtering by type, repo, reason
- `TestParsePRList` — verify branch→PR parsing from `gh pr list` JSON output
- `TestParseStackLS` — verify branch→PR parsing from `git stack ls` output (fallback)
- `TestSkipClosedPR` — verify closed/merged PRs are filtered out
- `TestDedupSamePR` — verify multiple notifications for same PR produce one event
- `TestFilterBotComments` — verify bot detection (user.type == "Bot" + ignore_users)
- `TestPerPRTimestamp` — verify per-PR comment timestamps don't miss delayed comments
- `TestReviewVerdict` — verify most recent non-COMMENTED verdict is extracted
