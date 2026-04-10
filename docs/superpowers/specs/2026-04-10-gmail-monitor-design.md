# Gmail Monitor Design

**Date:** 2026-04-10
**Status:** Approved

## Overview

A Claude Code skill that periodically monitors Gmail, classifies emails by importance using AI, and presents them in an interactive Markdown file. Users archive items by checking checkboxes, which triggers Gmail read-marking on the next poll cycle.

## Goals

1. Surface important emails without requiring the user to leave the terminal
2. AI-powered classification — no manual rules, pure content-based judgment
3. Interactive archive workflow via Markdown checkboxes
4. Clickable Gmail links for drill-down to original emails in browser

## Non-Goals (for v1)

- Dashboard card preview (future: read `state.yaml` from Go side)
- Mini App detail view UI (future: replace Markdown with interactive TUI)
- Email reply/compose from Jarvis
- Custom classification rules or filters

## Architecture

### Implementation Approach

Claude Code skill running via `/loop 10m /gmail-monitor`. No Go code changes required for v1. The skill uses MCP Gmail tools for API access and AI for classification.

### Data Flow

```
Each poll cycle:

1. Read digest.md → find [x] (checked) items
2. For each [x] item → call Gmail API to mark as read (remove UNREAD label)
3. Remove [x] items from digest.md
4. Fetch new Gmail messages (since last checkpoint)
5. AI classifies each message: important / action_needed / skipped
6. Append new items to digest.md under appropriate section headers
7. Update state.yaml (checkpoint + counts)
```

### File Structure

```
~/.jarvis/watch/gmail/
  ├── digest.md      # Interactive interface: unchecked = unprocessed emails
  └── state.yaml     # Poll checkpoint + card preview counts
```

## Output Formats

### digest.md

The primary user interface. Each email is a checkbox line with a summary and Gmail link. Organized by classification tier.

```markdown
## Important
- [ ] **SmartRevert: commit 334166a5 reverted** — PR #1792549 created, under test. You approved the original PR. [-> Gmail](https://mail.google.com/mail/u/0/#inbox/MESSAGE_ID)
- [ ] **XTA-16794: awaiting Tian's reply** — You asked about staging availability, no response yet. [-> Gmail](https://mail.google.com/mail/u/0/#inbox/MESSAGE_ID)

## Action Needed
- [ ] **Interview Notetaker launching 4/14** — You're in the first cohort. Training sessions: 4/13 8AM / 4/14 9AM / 4/15 5PM PT. [-> Gmail](https://mail.google.com/mail/u/0/#inbox/MESSAGE_ID)

## Skipped
- [ ] Ali Ghodsi reply to Iceberg v3 announcement [-> Gmail](https://mail.google.com/mail/u/0/#inbox/MESSAGE_ID)
- [ ] Uber Eats receipts x 2
```

**Format rules:**
- Each item: `- [ ] **Subject/title** — One-line AI summary. [-> Gmail](link)`
- Skipped items can be grouped (e.g., "Uber Eats receipts x 2") — grouped items link to the most recent one; checking archives all in the group
- Links use format: `https://mail.google.com/mail/u/0/#inbox/<message_id>`
- Section headers are only written when there are items in that tier
- Each item includes a hidden HTML comment with message ID(s) for archive processing: `<!-- ids:19d75c8091ad0103,19d7525d0d213060 -->`

### state.yaml

Machine-readable state for future dashboard integration.

```yaml
last_check: "2026-04-10T10:30:00Z"
last_message_id: "19d75c8091ad0103"
counts:
  important: 2
  action_needed: 1
  skipped: 4
```

## Classification Logic

Pure AI judgment based on email content. No predefined rules. The AI considers:

- Whether the email is addressed directly to the user vs. a mailing list blast
- Whether action is required from the user specifically
- Whether it relates to the user's active work (PRs they authored/reviewed, tickets they're on)
- Whether it's a notification/receipt/auto-generated vs. human-written

**Three tiers:**
- **Important**: Directly relevant to the user's work, likely needs awareness soon (e.g., reverts of approved PRs, replies to user's questions, production incidents)
- **Action Needed**: Requires the user to do something (e.g., training sign-up, review request, deadline)
- **Skipped**: FYI-only, receipts, CEO mass replies, mailing list chatter

## Archive Workflow

1. User opens `digest.md` in any editor
2. Changes `- [ ]` to `- [x]` on items they've seen/handled
3. Saves the file
4. Next poll cycle: skill reads the file, finds `[x]` items, calls `gmail_message_modify` to remove `UNREAD` label, then removes those lines from the file

## Startup

```
/loop 10m /gmail-monitor
```

## Error Handling

- If Gmail API fails: log error in state.yaml, retry next cycle
- If digest.md doesn't exist: create it with section headers
- If digest.md has no `[x]` items: skip archive step, proceed to fetch

## Future Extensions

- **Dashboard card preview**: Go side reads `state.yaml`, displays count in Monitors section of dashboard
- **Mini App detail view**: Replace Markdown file with interactive TUI when the card/mini-app framework is built
- **History**: Archive processed digests to `~/.jarvis/watch/gmail/history/YYYY-MM-DD.md`
- **Priority notifications**: Push important items to terminal notification when detected
