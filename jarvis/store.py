from __future__ import annotations

import os
import shutil
import tempfile
from pathlib import Path

import yaml

from jarvis.models import Reference, Task


JARVIS_DIR = Path.home() / ".jarvis"
TASKS_DIR = JARVIS_DIR / "tasks"


def _task_dir(task_id: str) -> Path:
    return TASKS_DIR / task_id


def _write_atomic(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=path.parent, suffix=".tmp")
    try:
        with os.fdopen(fd, "w") as f:
            f.write(content)
        os.replace(tmp, path)
    except Exception:
        os.unlink(tmp)
        raise


def _save_task_yaml(task: Task) -> None:
    path = _task_dir(task.id) / "task.yaml"
    _write_atomic(path, yaml.dump(task.to_dict(), default_flow_style=False, sort_keys=False))


def _save_references(task: Task) -> None:
    path = _task_dir(task.id) / "references.yaml"
    refs = [r.to_dict() for r in task.references]
    _write_atomic(path, yaml.dump(refs, default_flow_style=False, sort_keys=False))


def _load_references(task_id: str) -> list[Reference]:
    path = _task_dir(task_id) / "references.yaml"
    if not path.exists():
        return []
    with open(path) as f:
        data = yaml.safe_load(f)
    if not data:
        return []
    return [Reference.from_dict(r) for r in data]


def get_notes_path(task_id: str) -> Path:
    return _task_dir(task_id) / "notes.md"


def create_task(task: Task) -> Task:
    task_dir = _task_dir(task.id)
    task_dir.mkdir(parents=True, exist_ok=True)

    _save_task_yaml(task)
    _save_references(task)

    notes_path = task_dir / "notes.md"
    if not notes_path.exists():
        _write_atomic(notes_path, f"# {task.name}\n")

    return task


def get_task(task_id: str) -> Task | None:
    path = _task_dir(task_id) / "task.yaml"
    if not path.exists():
        return None
    with open(path) as f:
        data = yaml.safe_load(f)
    if not data:
        return None
    task = Task.from_dict(data)
    task.references = _load_references(task_id)
    return task


def update_task(task: Task) -> Task:
    task.touch()
    _save_task_yaml(task)
    _save_references(task)
    return task


def list_tasks(status: str | None = None) -> list[Task]:
    if not TASKS_DIR.exists():
        return []
    tasks = []
    for entry in TASKS_DIR.iterdir():
        if not entry.is_dir():
            continue
        task = get_task(entry.name)
        if task is None:
            continue
        if status is not None and task.status != status:
            continue
        tasks.append(task)
    tasks.sort(key=lambda t: t.updated_at, reverse=True)
    return tasks


def delete_task(task_id: str) -> bool:
    task_dir = _task_dir(task_id)
    if not task_dir.exists():
        return False
    shutil.rmtree(task_dir)
    return True
