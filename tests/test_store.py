import shutil
from pathlib import Path

import pytest

from jarvis import store
from jarvis.models import ClaudeSession, Reference, Task


@pytest.fixture(autouse=True)
def isolate_store(tmp_path, monkeypatch):
    """Redirect store to a temp directory for each test."""
    tasks_dir = tmp_path / "tasks"
    tasks_dir.mkdir()
    monkeypatch.setattr(store, "TASKS_DIR", tasks_dir)
    monkeypatch.setattr(store, "JARVIS_DIR", tmp_path)
    yield


def _make_task(id: str = "t1", name: str = "test task", **kwargs) -> Task:
    return Task(id=id, name=name, **kwargs)


class TestCreateAndGet:
    def test_create_and_get(self):
        task = _make_task()
        store.create_task(task)
        loaded = store.get_task("t1")
        assert loaded is not None
        assert loaded.name == "test task"
        assert loaded.status == "active"

    def test_creates_notes_file(self):
        task = _make_task()
        store.create_task(task)
        notes = store.get_notes_path("t1")
        assert notes.exists()
        assert "test task" in notes.read_text()

    def test_get_nonexistent(self):
        assert store.get_task("nope") is None

    def test_persists_references(self):
        task = _make_task()
        task.references = [
            Reference(label="Slack", url="https://slack.com/test", type="slack"),
            Reference(label="Jira", url="https://jira.com/ES-1", type="jira"),
        ]
        store.create_task(task)
        loaded = store.get_task("t1")
        assert len(loaded.references) == 2
        assert loaded.references[0].label == "Slack"
        assert loaded.references[1].type == "jira"

    def test_persists_sessions(self):
        task = _make_task()
        task.claude_sessions = [ClaudeSession(id="s1"), ClaudeSession(id="s2")]
        store.create_task(task)
        loaded = store.get_task("t1")
        assert len(loaded.claude_sessions) == 2
        assert loaded.claude_sessions[0].id == "s1"


class TestUpdate:
    def test_update_status(self):
        task = _make_task()
        store.create_task(task)
        task.status = "done"
        store.update_task(task)
        loaded = store.get_task("t1")
        assert loaded.status == "done"

    def test_update_touches_timestamp(self):
        task = _make_task()
        store.create_task(task)
        old = task.updated_at
        import time
        time.sleep(0.01)
        store.update_task(task)
        loaded = store.get_task("t1")
        assert loaded.updated_at > old

    def test_update_references(self):
        task = _make_task()
        store.create_task(task)
        task.references.append(Reference(label="new", url="http://x", type="link"))
        store.update_task(task)
        loaded = store.get_task("t1")
        assert len(loaded.references) == 1


class TestListTasks:
    def test_empty(self):
        assert store.list_tasks() == []

    def test_list_all(self):
        store.create_task(_make_task("a", "task a"))
        store.create_task(_make_task("b", "task b"))
        tasks = store.list_tasks()
        assert len(tasks) == 2

    def test_filter_by_status(self):
        t1 = _make_task("a", "active task")
        t2 = _make_task("b", "done task", status="done")
        store.create_task(t1)
        store.create_task(t2)
        active = store.list_tasks(status="active")
        assert len(active) == 1
        assert active[0].name == "active task"

    def test_sorted_by_updated_at(self):
        import time
        t1 = _make_task("a", "older")
        store.create_task(t1)
        time.sleep(0.01)
        t2 = _make_task("b", "newer")
        store.create_task(t2)
        tasks = store.list_tasks()
        assert tasks[0].name == "newer"
        assert tasks[1].name == "older"


class TestDelete:
    def test_delete(self):
        store.create_task(_make_task())
        assert store.delete_task("t1") is True
        assert store.get_task("t1") is None

    def test_delete_nonexistent(self):
        assert store.delete_task("nope") is False
