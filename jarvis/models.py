from __future__ import annotations

import re
import secrets
from dataclasses import dataclass, field
from datetime import datetime, timezone


FILLER_WORDS = {"the", "a", "an", "is", "are", "was", "were", "of", "for", "to", "in", "on", "at", "by", "with"}
MAX_SLUG_LENGTH = 50


def generate_task_id() -> str:
    return secrets.token_hex(4)



def _fallback_slug(name: str) -> str:
    """Simple text-based slug generation as fallback."""
    words = re.findall(r"[a-zA-Z0-9]+", name.lower())
    words = [w for w in words if w not in FILLER_WORDS]
    slug = "-".join(words)
    if len(slug) > MAX_SLUG_LENGTH:
        slug = slug[:MAX_SLUG_LENGTH].rstrip("-")
    return slug


def name_to_worktree_slug(name: str, existing_slugs: set[str] | None = None) -> str:
    slug = _fallback_slug(name)

    if existing_slugs is None:
        return slug

    if slug not in existing_slugs:
        return slug

    for i in range(2, 100):
        candidate = f"{slug}-{i}"
        if candidate not in existing_slugs:
            return candidate

    # Extremely unlikely fallback
    return f"{slug}-{secrets.token_hex(2)}"


@dataclass
class Reference:
    label: str
    url: str
    type: str
    added_at: str = field(default_factory=lambda: datetime.now(timezone.utc).isoformat())

    def to_dict(self) -> dict:
        return {"label": self.label, "url": self.url, "type": self.type, "added_at": self.added_at}

    @classmethod
    def from_dict(cls, data: dict) -> Reference:
        return cls(label=data["label"], url=data["url"], type=data["type"], added_at=data.get("added_at", ""))


@dataclass
class ClaudeSession:
    id: str
    started_at: str = field(default_factory=lambda: datetime.now(timezone.utc).isoformat())

    def to_dict(self) -> dict:
        return {"id": self.id, "started_at": self.started_at}

    @classmethod
    def from_dict(cls, data: dict) -> ClaudeSession:
        return cls(id=data["id"], started_at=data.get("started_at", ""))


@dataclass
class Task:
    id: str
    name: str
    status: str = "active"
    cwd: str = ""
    branches: list[str] = field(default_factory=list)
    claude_sessions: list[ClaudeSession] = field(default_factory=list)
    references: list[Reference] = field(default_factory=list)
    created_at: str = field(default_factory=lambda: datetime.now(timezone.utc).isoformat())
    updated_at: str = field(default_factory=lambda: datetime.now(timezone.utc).isoformat())
    tags: list[str] = field(default_factory=list)

    def to_dict(self) -> dict:
        return {
            "id": self.id,
            "name": self.name,
            "status": self.status,
            "cwd": self.cwd,
            "branches": self.branches,
            "claude_sessions": [s.to_dict() for s in self.claude_sessions],
            "created_at": self.created_at,
            "updated_at": self.updated_at,
            "tags": self.tags,
        }

    @classmethod
    def from_dict(cls, data: dict) -> Task:
        return cls(
            id=data["id"],
            name=data["name"],
            status=data.get("status", "active"),
            cwd=data.get("cwd", ""),
            branches=data.get("branches", []),
            claude_sessions=[ClaudeSession.from_dict(s) for s in data.get("claude_sessions", [])],
            references=[],  # loaded separately from references.yaml
            created_at=data.get("created_at", ""),
            updated_at=data.get("updated_at", ""),
            tags=data.get("tags", []),
        )

    def touch(self) -> None:
        self.updated_at = datetime.now(timezone.utc).isoformat()
