from __future__ import annotations

import functools
import re
import subprocess
from pathlib import Path

VALID_SLUG_PATTERN = re.compile(r"^[a-z0-9][a-z0-9-]*[a-z0-9]$")


@functools.lru_cache(maxsize=1)
def has_git_stack() -> bool:
    """Check if git-stack is installed and available."""
    try:
        subprocess.run(
            ["git", "stack", "version"],
            capture_output=True, check=True,
        )
        return True
    except (subprocess.CalledProcessError, FileNotFoundError):
        return False


def _get_default_branch(repo_path: str) -> str:
    """Detect the default branch (main or master)."""
    for branch in ("main", "master"):
        result = subprocess.run(
            ["git", "rev-parse", "--verify", branch],
            cwd=repo_path, capture_output=True,
        )
        if result.returncode == 0:
            return branch
    return "main"


def create_worktree(slug: str, base_dir: str, repo_path: str) -> tuple[str, str]:
    """Create a worktree with a branch for the given slug.

    Returns (worktree_path, branch_name).
    """
    if not slug or not VALID_SLUG_PATTERN.match(slug):
        raise ValueError(f"Invalid worktree slug: {slug!r}")
    worktree_path = str(Path(base_dir) / slug)
    Path(base_dir).mkdir(parents=True, exist_ok=True)
    # Clean up any stale worktree at this path from a previous failed task
    if Path(worktree_path).exists():
        remove_worktree(worktree_path, repo_path)

    default_branch = _get_default_branch(repo_path)

    if has_git_stack():
        # Using --detach avoids conflicts when the default branch is already
        # checked out in the main worktree. --create-on tells git stack to
        # parent the new branch off the default branch.
        subprocess.run(
            ["git", "worktree", "add", "--detach", "--", worktree_path, default_branch],
            cwd=repo_path, check=True, capture_output=True, text=True,
        )
        subprocess.run(
            ["git", "stack", "create", slug, "--create-on", default_branch],
            cwd=worktree_path, check=True, capture_output=True, text=True,
        )
        branch_name = f"stack/{slug}"
    else:
        branch_name = slug
        subprocess.run(
            ["git", "worktree", "add", "-b", branch_name, "--", worktree_path, default_branch],
            cwd=repo_path, check=True, capture_output=True, text=True,
        )

    return worktree_path, branch_name


def ensure_checked_out(worktree_path: str) -> None:
    """Ensure the worktree has files checked out. Needed after --no-checkout creation."""
    if not Path(worktree_path).exists():
        raise FileNotFoundError(f"Worktree path not found: {worktree_path}")
    # Check if real source files exist (not just .git and .claude from hooks)
    entries = [e.name for e in Path(worktree_path).iterdir()]
    has_source_files = any(name not in (".git", ".claude") for name in entries)
    if not has_source_files:
        # Show progress — this can take ~50s on large repos
        subprocess.run(
            ["git", "checkout", "--progress", "HEAD"],
            cwd=worktree_path,
            check=True,
        )


def remove_worktree(worktree_path: str, repo_path: str) -> bool:
    result = subprocess.run(
        ["git", "worktree", "remove", worktree_path, "--force"],
        cwd=repo_path, capture_output=True, text=True,
    )
    subprocess.run(
        ["git", "worktree", "prune"],
        cwd=repo_path, capture_output=True, text=True,
    )
    return result.returncode == 0


def is_branch_merged(branch: str, repo_path: str, target: str = "master") -> bool:
    result = subprocess.run(
        ["git", "branch", "--merged", target],
        cwd=repo_path, capture_output=True, text=True,
    )
    merged_branches = [b.strip().lstrip("* ") for b in result.stdout.splitlines()]
    return branch in merged_branches


def get_current_branch(worktree_path: str) -> str:
    result = subprocess.run(
        ["git", "rev-parse", "--abbrev-ref", "HEAD"],
        cwd=worktree_path, capture_output=True, text=True, check=True,
    )
    return result.stdout.strip()


def list_worktree_branches(repo_path: str) -> list[str]:
    result = subprocess.run(
        ["git", "worktree", "list", "--porcelain"],
        cwd=repo_path, capture_output=True, text=True, check=True,
    )
    branches = []
    for line in result.stdout.splitlines():
        if line.startswith("branch "):
            ref = line.split(" ", 1)[1]
            # Strip refs/heads/ prefix
            if ref.startswith("refs/heads/"):
                ref = ref[len("refs/heads/"):]
            branches.append(ref)
    return branches
