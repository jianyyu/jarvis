"""Hook handler and JSONL parsing for Claude Code auto-enrichment.

This module serves two purposes:
  A. CLI entry point for hooks (`python -m jarvis.enrichment --event <type>`)
     The task ID is read from the JARVIS_TASK_ID environment variable,
     which is set by the launcher when spawning Claude Code.
  B. Hook installation into a task's worktree `.claude/settings.local.json`
"""
from __future__ import annotations

import argparse
import json
import os
import re
import shlex
import sys
from pathlib import Path

from jarvis.models import ClaudeSession, Task
from jarvis import store


# ---------------------------------------------------------------------------
# Pattern detection
# ---------------------------------------------------------------------------

# Git branch creation patterns in bash commands
BRANCH_PATTERNS = [
    re.compile(r"git\s+stack\s+create\s+(\S+)"),
    re.compile(r"git\s+checkout\s+-b\s+(\S+)"),
    re.compile(r"git\s+switch\s+-c\s+(\S+)"),
    re.compile(r"git\s+branch\s+([^-\s]\S*)"),  # exclude flags starting with -
]



def _extract_branch_from_command(command: str) -> str | None:
    """Extract a branch name from a git command string, or None."""
    for pattern in BRANCH_PATTERNS:
        m = pattern.search(command)
        if m:
            return m.group(1)
    return None


# ---------------------------------------------------------------------------
# Hook event handlers
# ---------------------------------------------------------------------------

def handle_session_start(data: dict, task: Task) -> bool:
    """Extract session_id from SessionStart hook data and add to task."""
    session_id = data.get("session_id")
    if not session_id:
        return False

    # Avoid duplicates
    existing_ids = {s.id for s in task.claude_sessions}
    if session_id in existing_ids:
        return False

    task.claude_sessions.append(ClaudeSession(id=session_id))
    return True


def handle_post_tool_use(data: dict, task: Task) -> bool:
    """Check Bash tool usage for git branch creation commands."""
    tool_name = data.get("tool_name", "")
    if tool_name != "Bash":
        return False

    tool_input = data.get("tool_input", {})
    command = tool_input.get("command", "")
    if not command:
        return False

    branch = _extract_branch_from_command(command)
    if branch and branch not in task.branches:
        task.branches.append(branch)
        return True

    return False


# ---------------------------------------------------------------------------
# CLI entry point
# ---------------------------------------------------------------------------

def enrich_main() -> None:
    """CLI entry point: read hook JSON from stdin, dispatch to handler."""
    parser = argparse.ArgumentParser(description="Jarvis enrichment hook handler")
    parser.add_argument("--event", required=True, choices=["session-start", "post-tool-use"])
    args = parser.parse_args()

    task_id = os.environ.get("JARVIS_TASK_ID")
    if not task_id:
        return

    # Read JSON payload from stdin
    raw = sys.stdin.read()
    if not raw.strip():
        return
    try:
        data = json.loads(raw)
    except json.JSONDecodeError:
        return

    task = store.get_task(task_id)
    if task is None:
        return

    handlers = {
        "session-start": handle_session_start,
        "post-tool-use": handle_post_tool_use,
    }

    handler = handlers[args.event]
    changed = handler(data, task)

    if changed:
        store.update_task(task)


# ---------------------------------------------------------------------------
# Hook installation
# ---------------------------------------------------------------------------

def _hook_command(event: str) -> str:
    return shlex.join([sys.executable, "-m", "jarvis.enrichment", "--event", event])


def get_hooks_config() -> dict:
    """Return the hooks JSON structure for Claude Code settings."""
    return {
        "hooks": {
            "SessionStart": [
                {
                    "hooks": [
                        {
                            "type": "command",
                            "command": _hook_command("session-start"),
                        }
                    ]
                }
            ],
            "PostToolUse": [
                {
                    "matcher": "Bash",
                    "hooks": [
                        {
                            "type": "command",
                            "command": _hook_command("post-tool-use"),
                        }
                    ],
                }
            ],
        }
    }


def install_hooks(task: Task) -> None:
    """Write Claude Code hooks config to the task worktree's .claude/settings.local.json."""
    settings_dir = Path(task.cwd) / ".claude"
    settings_dir.mkdir(parents=True, exist_ok=True)
    settings_path = settings_dir / "settings.local.json"

    config = get_hooks_config()

    # Merge with existing settings if present
    if settings_path.exists():
        try:
            existing = json.loads(settings_path.read_text())
        except (json.JSONDecodeError, OSError):
            existing = {}
        existing["hooks"] = config["hooks"]
        config = existing

    settings_path.write_text(json.dumps(config, indent=2) + "\n")


def uninstall_hooks(task: Task) -> None:
    """Remove Jarvis hooks from the task worktree's .claude/settings.local.json."""
    settings_path = Path(task.cwd) / ".claude" / "settings.local.json"
    if not settings_path.exists():
        return

    try:
        existing = json.loads(settings_path.read_text())
    except (json.JSONDecodeError, OSError):
        return

    if "hooks" in existing:
        del existing["hooks"]

    if existing:
        settings_path.write_text(json.dumps(existing, indent=2) + "\n")
    else:
        settings_path.unlink(missing_ok=True)


# ---------------------------------------------------------------------------
# Allow running as `python -m jarvis.enrichment`
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    enrich_main()
