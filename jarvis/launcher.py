from __future__ import annotations

import subprocess
from pathlib import Path

from jarvis.config import JarvisConfig
from jarvis.display import console
from jarvis.models import Task
from jarvis.worktree import ensure_checked_out


def _encode_cwd(cwd: str) -> str:
    """Encode a cwd path the same way Claude Code does for project directories."""
    import re
    return re.sub(r"[^a-zA-Z0-9]", "-", cwd)


def _session_is_valid(session_id: str, task_cwd: str) -> bool:
    """Check if a Claude Code session JSONL file exists and has real conversation data."""
    encoded = _encode_cwd(task_cwd)
    session_path = Path.home() / ".claude" / "projects" / encoded / f"{session_id}.jsonl"
    if not session_path.exists():
        return False
    # A valid session needs more than just a file-history-snapshot
    # Check that there's at least a user or assistant message
    try:
        with open(session_path) as f:
            for line in f:
                if '"type":"user"' in line or '"type": "user"' in line:
                    return True
    except OSError:
        pass
    return False


def _build_system_prompt(task: Task) -> str:
    lines = [f'You are working on: "{task.name}"']
    if task.branches:
        lines.append(f"Branch: {task.branches[0]}")
    lines.append("")
    lines.append("Git workflow rules (follow strictly):")
    lines.append("- Always use `git stack commit` instead of `git commit` to create commits.")
    lines.append("- Always use `git stack amend` instead of `git commit --amend` to amend commits.")
    lines.append("- Always use `git stack push` instead of `git push` or `git pp` to push and create PRs.")
    if task.references:
        lines.append("")
        lines.append("References:")
        for ref in task.references:
            lines.append(f"- {ref.type.capitalize()}: {ref.label} ({ref.url})")
    return "\n".join(lines)


def _task_env(task: Task) -> dict[str, str]:
    """Build environment with JARVIS_TASK_ID for subprocess access."""
    import os
    return {**os.environ, "JARVIS_TASK_ID": task.id}


def launch_new_session(task: Task, config: JarvisConfig) -> None:
    prompt = _build_system_prompt(task)
    cmd = ["claude", "--append-system-prompt", prompt]
    subprocess.run(cmd, cwd=task.cwd, env=_task_env(task))


def _find_valid_session(task: Task) -> str | None:
    """Find the most recent session that has a JSONL file on disk."""
    for session in reversed(task.claude_sessions):
        if _session_is_valid(session.id, task.cwd):
            return session.id
    return None


def migrate_sessions(task: Task, old_cwd: str, new_cwd: str) -> None:
    """Move session JSONL files from old project dir to new one."""
    if old_cwd == new_cwd:
        return
    old_dir = Path.home() / ".claude" / "projects" / _encode_cwd(old_cwd)
    new_dir = Path.home() / ".claude" / "projects" / _encode_cwd(new_cwd)
    new_dir.mkdir(parents=True, exist_ok=True)
    for session in task.claude_sessions:
        old_path = old_dir / f"{session.id}.jsonl"
        new_path = new_dir / f"{session.id}.jsonl"
        if old_path.exists() and not new_path.exists():
            old_path.rename(new_path)


def resume_session(task: Task, config: JarvisConfig) -> None:
    if not task.claude_sessions:
        raise ValueError(f"Task {task.id!r} has no Claude sessions to resume")

    session_id = _find_valid_session(task)
    if not session_id:
        console.print("  [red]Error:[/red] No valid sessions found. Session files may have been deleted.")
        return

    prompt = _build_system_prompt(task)
    cmd = ["claude", "--resume", session_id, "--append-system-prompt", prompt]
    console.print(f"  Resuming session {session_id[:8]}...", style="dim")
    subprocess.run(cmd, cwd=task.cwd, env=_task_env(task))


def _is_chat_task(task: Task, config: JarvisConfig) -> bool:
    """A chat task has no branches and its cwd is the main repo."""
    return not task.branches and task.cwd == config.effective_repo_path()


def open_claude(task: Task, config: JarvisConfig) -> None:
    if not task.cwd or not Path(task.cwd).exists():
        console.print(f"  [red]Worktree missing:[/red] {task.cwd}", style="red")
        console.print("  Recreating worktree...", style="dim")
        import subprocess as sp
        Path(task.cwd).parent.mkdir(parents=True, exist_ok=True)
        try:
            sp.run(
                ["git", "worktree", "add", "--detach", "--no-checkout", "--", task.cwd],
                cwd=config.effective_repo_path(),
                check=True, capture_output=True, text=True,
            )
        except sp.CalledProcessError as e:
            console.print(f"  [red]Failed to recreate worktree:[/red] {e.stderr or e}")
            return

    if not _is_chat_task(task, config):
        console.print("  Preparing worktree...", style="dim")
        ensure_checked_out(task.cwd)

    console.print("  Launching Claude Code...", style="dim")
    if task.claude_sessions:
        resume_session(task, config)
    else:
        launch_new_session(task, config)
