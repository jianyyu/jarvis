"""CLI for managing Jarvis tasks from within Claude Code.

Usage:
    jarvis-task init "fix the staging bug"

Requires JARVIS_TASK_ID env var (set automatically by Jarvis when launching Claude Code).
"""
from __future__ import annotations

import argparse
import os
import sys
from pathlib import Path

from jarvis import enrichment, store, worktree
from jarvis.config import load_config
from jarvis.launcher import migrate_sessions
from jarvis.models import name_to_worktree_slug
from jarvis.worktree import ensure_checked_out


def _resolve_task():
    """Get the current task from JARVIS_TASK_ID env var."""
    task_id = os.environ.get("JARVIS_TASK_ID")
    if not task_id:
        print("Error: JARVIS_TASK_ID not set. Are you running inside a Jarvis task?", file=sys.stderr)
        sys.exit(1)
    task = store.get_task(task_id)
    if not task:
        print(f"Error: Task {task_id!r} not found.", file=sys.stderr)
        sys.exit(1)
    return task


def cmd_init(args: argparse.Namespace) -> None:
    """Set title, create worktree + branch, and install hooks."""
    task = _resolve_task()
    title = args.title

    config = load_config()
    repo_path = config.effective_repo_path()
    base_dir = config.effective_worktree_base_dir()

    if not repo_path or not base_dir:
        print("Error: Could not detect repo path.", file=sys.stderr)
        sys.exit(1)

    # Update title
    task.name = title

    # Create worktree + branch
    existing_slugs = {
        Path(t.cwd).name for t in store.list_tasks() if t.cwd
    }
    slug = name_to_worktree_slug(title, existing_slugs)

    try:
        worktree_path, branch_name = worktree.create_worktree(slug, base_dir, repo_path)
    except Exception as e:
        print(f"Error creating worktree: {e}", file=sys.stderr)
        sys.exit(1)
    old_cwd = task.cwd
    task.cwd = worktree_path
    task.branches = [branch_name]

    # Move session files so Jarvis can resume in the new worktree
    migrate_sessions(task, old_cwd, worktree_path)

    # Checkout files in the new worktree
    ensure_checked_out(worktree_path)

    # Install hooks in the new worktree so session tracking continues
    enrichment.install_hooks(task)

    store.update_task(task)

    print(f"Task: {task.name}")
    print(f"Worktree: {worktree_path}")
    print(f"Branch: {branch_name}")


def cmd_rename(args: argparse.Namespace) -> None:
    """Rename the current task."""
    task = _resolve_task()
    task.name = args.name
    store.update_task(task)
    print(f"Task renamed: {task.name}")


def main() -> None:
    parser = argparse.ArgumentParser(prog="jarvis-task", description="Manage Jarvis tasks")
    sub = parser.add_subparsers(dest="command")

    p_init = sub.add_parser("init", help="Set title and create worktree + branch")
    p_init.add_argument("title", help="Task title")

    p_rename = sub.add_parser("rename", help="Rename the current task")
    p_rename.add_argument("name", help="New task name")

    args = parser.parse_args()
    if not args.command:
        parser.print_help()
        sys.exit(1)

    commands = {
        "init": cmd_init,
        "rename": cmd_rename,
    }
    commands[args.command](args)


if __name__ == "__main__":
    main()
