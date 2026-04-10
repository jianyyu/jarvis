# Gmail Monitor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create a Claude Code skill that monitors Gmail, classifies emails by importance, and outputs to an interactive Markdown file with checkbox-based archive workflow.

**Architecture:** Single SKILL.md file at `~/.claude/skills/gmail-monitor/SKILL.md`. Uses MCP Gmail tools (`mcp__google__gmail_*`) for API access. State tracked in `~/.claude/skills/monitor-state.json` (shared with other monitors). Output written to `~/.jarvis/watch/gmail/digest.md`.

**Tech Stack:** Claude Code skill (Markdown), MCP Gmail tools, YAML state file

**Spec:** `/home/jianyu.zhou/jarvis/docs/superpowers/specs/2026-04-10-gmail-monitor-design.md`

---

### Task 1: Create the SKILL.md file with frontmatter and configuration

**Files:**
- Create: `~/.claude/skills/gmail-monitor/SKILL.md`

- [ ] **Step 1: Create the skill directory**

```bash
mkdir -p ~/.claude/skills/gmail-monitor
```

- [ ] **Step 2: Write the SKILL.md file**

Write the following to `~/.claude/skills/gmail-monitor/SKILL.md`:

```markdown
---
name: gmail-monitor
description: Monitor Gmail for messages needing Jianyu Zhou's attention. Use when asked to check Gmail, monitor emails, or run as a recurring /loop task. Triggers on "check gmail", "gmail monitor", "gmail messages", "what emails do I have", "check my email".
---

# Gmail Monitor

Monitor Gmail for actionable messages. Classify by importance using AI judgment, output to an interactive Markdown digest with checkbox-based archive workflow.

## Configuration

- **User Email:** jianyu.zhou@databricks.com
- **Digest File:** ~/.jarvis/watch/gmail/digest.md
- **Default Lookback:** 2 hours (override with argument, e.g. `/gmail-monitor 4h`)
- **Max Messages Per Poll:** 30

## State File

**Path:** `~/.claude/skills/monitor-state.json`

Streaming-based: each run covers exactly the window `[last_run, now]` so no messages are ever missed between cycles.

At the **start** of each run:
1. Read `gmail-monitor.last_run` from the state file
2. If `last_run` exists: query everything since `last_run`
3. If `last_run` is missing (first run): fall back to the default lookback (2h) or user-provided argument
4. **Safety cap:** If the gap since `last_run` exceeds **24 hours**, cap at 24h ago and warn
5. If the user provides an explicit lookback argument (e.g. `/gmail-monitor 4h`), use that instead of `last_run`

At the **end** of each run, update the state file:
```json
"gmail-monitor": {
  "last_run": "<current UTC timestamp>",
  "last_message_id": "<id of newest message processed>",
  "status": "ok"
}
```

Only update `last_run` on successful completion.

## Workflow

### Phase 1: Process Archived Items

1. Read `~/.jarvis/watch/gmail/digest.md`
2. Find all lines matching `- [x]` (checked checkboxes)
3. For each checked item, extract message ID(s) from the HTML comment: `<!-- ids:ID1,ID2 -->`
4. For each message ID, call `mcp__google__gmail_message_modify` with `remove_label_ids: ["UNREAD"]` to mark as read in Gmail
5. Remove all `- [x]` lines (and their `<!-- ids:... -->` comments) from the file content
6. If a section header has no remaining items under it, remove the header too
7. Write the cleaned content back to `digest.md`
8. If `digest.md` does not exist, skip this phase

### Phase 2: Compute Lookback Window

1. Read `gmail-monitor.last_run` from `~/.claude/skills/monitor-state.json`
2. If user provides an explicit argument (e.g. `4h`): compute CUTOFF = now - argument
3. If `last_run` exists and no explicit argument: CUTOFF = `last_run`
4. If `last_run` is missing: CUTOFF = now - 2h
5. Safety cap: if CUTOFF > 24h ago, cap at 24h and warn
6. Convert CUTOFF to Gmail query format: `after:EPOCH_SECONDS`

### Phase 3: Fetch New Messages

1. Call `mcp__google__gmail_message_list` with:
   - `q`: `is:inbox after:{EPOCH_SECONDS}`
   - `max_results`: 30
2. For each message in the result, check if its ID already exists in `digest.md` (search for `<!-- ids:...ID... -->`) — skip duplicates
3. For messages that need full content for classification, call `mcp__google__gmail_message_get` with `message_id` and `format: "full"`
4. If no new messages, skip to Phase 6

### Phase 4: Classify Messages

For each new message, use AI judgment to classify into one of three tiers:

- **Important**: Directly relevant to Jianyu's work. Examples:
  - SmartRevert notifications for PRs he authored or approved
  - Replies to his questions or comments
  - Production incidents affecting his services
  - Direct emails (not mailing list) from teammates about active work
- **Action Needed**: Requires Jianyu to do something specific. Examples:
  - Training sign-ups with deadlines
  - Review requests
  - Meeting scheduling that needs a response
  - JIRA tickets assigned to or mentioning him
- **Skipped**: FYI-only, noise. Examples:
  - CEO mass replies to announcement threads
  - Receipts (Uber, DoorDash, etc.)
  - Mailing list chatter where Jianyu is not directly involved
  - Auto-generated notifications that are purely informational

For each message, also generate:
- A bold title (subject or a clearer rewrite if the subject is vague)
- A one-line summary explaining why it matters or what action is needed
- The Gmail link: `https://mail.google.com/mail/u/0/#inbox/{message_id}`

### Phase 5: Append to Digest

1. Ensure `~/.jarvis/watch/gmail/` directory exists (`mkdir -p`)
2. Read existing `digest.md` (or start with empty content)
3. For each classified message, format as:
   ```
   - [ ] **Title** — Summary [-> Gmail](link) <!-- ids:MESSAGE_ID -->
   ```
   For grouped skipped items (e.g., multiple receipts from same sender):
   ```
   - [ ] Uber Eats receipts x 3 [-> Gmail](link_to_most_recent) <!-- ids:ID1,ID2,ID3 -->
   ```
4. Append new items under the correct section header:
   - If the section header (`## Important`, `## Action Needed`, `## Skipped`) already exists in the file, append the new items right after the last item in that section (before the next `## ` header or end of file)
   - If the section header doesn't exist, add it at the appropriate position (Important first, then Action Needed, then Skipped)
5. Write the updated content back to `digest.md`

### Phase 6: Update State

1. Update `~/.claude/skills/monitor-state.json`:
   ```json
   "gmail-monitor": {
     "last_run": "<current UTC timestamp>",
     "last_message_id": "<newest message ID from this run, or keep previous>",
     "status": "ok"
   }
   ```
2. Update `~/.jarvis/watch/gmail/state.yaml`:
   ```yaml
   last_check: "<current UTC timestamp>"
   last_message_id: "<newest message ID>"
   counts:
     important: <count of unchecked Important items in digest.md>
     action_needed: <count of unchecked Action Needed items in digest.md>
     skipped: <count of unchecked Skipped items in digest.md>
   ```
3. Print a brief summary to stdout:
   ```
   📬 Gmail: 2 important, 1 action needed, 4 skipped (digest: ~/.jarvis/watch/gmail/digest.md)
   ```

## Error Handling

- **Gmail API failure:** Log error, set `status: "error"` in state file, do NOT update `last_run` (next run retries from same point)
- **Token expiry (403):** Set `status: "token_expired"`, report to user, stop
- **digest.md doesn't exist:** Create it fresh in Phase 5 (skip Phase 1)
- **No [x] items:** Skip Phase 1 archive processing, proceed normally

## Loop Integration

```
/loop 10m /gmail-monitor
```

For a broader catch-up:
```
/gmail-monitor 4h
```
```

- [ ] **Step 3: Verify the skill file is recognized**

Run `/gmail-monitor` manually in Claude Code. Expected: the skill triggers and attempts to run through the workflow phases. It should at minimum read the state file and attempt to fetch Gmail messages.

- [ ] **Step 4: Commit**

```bash
cd ~/.claude && git add skills/gmail-monitor/SKILL.md && git commit -m "feat: add gmail-monitor skill"
```

Note: if `~/.claude` is not a git repo, just verify the file exists:
```bash
cat ~/.claude/skills/gmail-monitor/SKILL.md | head -5
```

---

### Task 2: Create the output directory and seed files

**Files:**
- Create: `~/.jarvis/watch/gmail/digest.md`
- Create: `~/.jarvis/watch/gmail/state.yaml`

- [ ] **Step 1: Create the directory structure**

```bash
mkdir -p ~/.jarvis/watch/gmail
```

- [ ] **Step 2: Create the initial digest.md**

Write to `~/.jarvis/watch/gmail/digest.md`:

```markdown
## Important

## Action Needed

## Skipped
```

- [ ] **Step 3: Create the initial state.yaml**

Write to `~/.jarvis/watch/gmail/state.yaml`:

```yaml
last_check: ""
last_message_id: ""
counts:
  important: 0
  action_needed: 0
  skipped: 0
```

- [ ] **Step 4: Add gmail-monitor entry to monitor-state.json**

Read `~/.claude/skills/monitor-state.json`, add a new key:

```json
"gmail-monitor": {
  "last_run": "",
  "last_message_id": "",
  "status": "new"
}
```

Write the updated JSON back to the file (preserving existing entries for slack-monitor, pagerduty-investigate, github-pr-review, jira-monitor).

- [ ] **Step 5: Commit**

```bash
git add -f ~/.jarvis/watch/gmail/digest.md ~/.jarvis/watch/gmail/state.yaml
git commit -m "feat: seed gmail monitor output files"
```

Note: if these paths are outside the jarvis repo, just verify files exist:
```bash
ls -la ~/.jarvis/watch/gmail/
cat ~/.jarvis/watch/gmail/digest.md
```

---

### Task 3: End-to-end manual test

**Files:**
- No new files — this task validates the skill works end-to-end

- [ ] **Step 1: Run the skill manually**

In Claude Code, run:
```
/gmail-monitor
```

This should:
1. Skip Phase 1 (no `[x]` items in fresh digest.md)
2. Compute a 2h lookback window (no previous `last_run`)
3. Fetch recent inbox messages via `mcp__google__gmail_message_list`
4. Classify each message into Important / Action Needed / Skipped
5. Append formatted items to `~/.jarvis/watch/gmail/digest.md`
6. Update state files

- [ ] **Step 2: Verify digest.md output**

```bash
cat ~/.jarvis/watch/gmail/digest.md
```

Expected: Messages organized under `## Important`, `## Action Needed`, `## Skipped` headers. Each item has:
- `- [ ]` checkbox
- Bold title
- One-line summary
- `[-> Gmail](link)` with real message ID
- `<!-- ids:... -->` HTML comment

- [ ] **Step 3: Verify state files**

```bash
cat ~/.jarvis/watch/gmail/state.yaml
cat ~/.claude/skills/monitor-state.json | python3 -c "import sys,json; print(json.dumps(json.load(sys.stdin).get('gmail-monitor',{}), indent=2))"
```

Expected: `last_run` is set to a recent timestamp, `last_message_id` is populated, counts match digest.md content.

- [ ] **Step 4: Test the archive workflow**

Open `~/.jarvis/watch/gmail/digest.md` in an editor. Change one `- [ ]` to `- [x]`. Save.

Run `/gmail-monitor` again.

Expected:
1. The `[x]` item is processed — Gmail API called to remove UNREAD label
2. The `[x]` line is removed from digest.md
3. Any new messages since last run are appended
4. Counts in state.yaml are updated

- [ ] **Step 5: Verify Gmail mark-as-read worked**

Check Gmail (web or via API) to confirm the archived message is now marked as read.

```
/gmail-monitor
```

Then check: the message that was checked off should no longer appear as unread in Gmail.

- [ ] **Step 6: Test loop integration**

Start the recurring loop:
```
/loop 10m /gmail-monitor
```

Wait for one cycle to complete. Verify it ran successfully by checking the state file timestamp updated.

---

### Task 4: Iterate on classification quality

**Files:**
- Modify: `~/.claude/skills/gmail-monitor/SKILL.md` (Phase 4 classification guidance)

This task is done after observing a few poll cycles and reviewing classification accuracy.

- [ ] **Step 1: Review classification results after 3-5 poll cycles**

```bash
cat ~/.jarvis/watch/gmail/digest.md
```

Check:
- Are Important items actually important?
- Are Skipped items truly skippable?
- Are any items in the wrong tier?

- [ ] **Step 2: Adjust classification guidance in SKILL.md if needed**

If classification is off, update the Phase 4 section in `~/.claude/skills/gmail-monitor/SKILL.md` with more specific guidance. Examples of adjustments:
- "Emails from `noreply@` addresses are almost always Skipped unless they contain incident alerts"
- "JIRA automation emails should be Action Needed if the ticket is assigned to Jianyu, Skipped otherwise"
- "Mailing list emails where Jianyu is in the To/CC (not just BCC via group) should be elevated to Action Needed"

- [ ] **Step 3: Re-run and verify**

```bash
/gmail-monitor 2h
```

Check that the adjusted classification produces better results.

- [ ] **Step 4: Commit if changes were made**

```bash
cd ~/.claude && git add skills/gmail-monitor/SKILL.md && git commit -m "fix: tune gmail classification guidance"
```
