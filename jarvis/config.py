from __future__ import annotations

import os
import subprocess
import tempfile
from dataclasses import dataclass
from pathlib import Path

import yaml


CONFIG_PATH = Path.home() / ".jarvis" / "config.yaml"


def _detect_repo_path() -> str:
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--show-toplevel"],
            capture_output=True, text=True, check=True,
        )
        return result.stdout.strip()
    except (subprocess.CalledProcessError, FileNotFoundError):
        return ""


@dataclass
class JarvisConfig:
    worktree_base_dir: str = ""

    def effective_repo_path(self) -> str:
        return _detect_repo_path()

    def effective_worktree_base_dir(self) -> str:
        if self.worktree_base_dir:
            return self.worktree_base_dir
        repo = self.effective_repo_path()
        if repo:
            return str(Path(repo) / ".claude" / "worktrees")
        return ""

    def to_dict(self) -> dict:
        return {
            "worktree_base_dir": self.worktree_base_dir,
        }

    @classmethod
    def from_dict(cls, data: dict) -> JarvisConfig:
        return cls(
            worktree_base_dir=data.get("worktree_base_dir", ""),
        )


def load_config() -> JarvisConfig:
    if not CONFIG_PATH.exists():
        return JarvisConfig()
    with open(CONFIG_PATH) as f:
        data = yaml.safe_load(f)
    if not data:
        return JarvisConfig()
    return JarvisConfig.from_dict(data)


def save_config(config: JarvisConfig) -> None:
    CONFIG_PATH.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=CONFIG_PATH.parent, suffix=".tmp")
    try:
        with os.fdopen(fd, "w") as f:
            yaml.dump(config.to_dict(), f, default_flow_style=False, sort_keys=False)
        os.replace(tmp, CONFIG_PATH)
    except Exception:
        os.unlink(tmp)
        raise
