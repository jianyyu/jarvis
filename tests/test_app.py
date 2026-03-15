"""Test the JarvisApp state machine without a real terminal."""
import pytest

from jarvis import store
from jarvis.app import JarvisApp
from jarvis.models import ClaudeSession, Task


@pytest.fixture(autouse=True)
def isolate_store(tmp_path, monkeypatch):
    tasks_dir = tmp_path / "tasks"
    tasks_dir.mkdir()
    monkeypatch.setattr(store, "TASKS_DIR", tasks_dir)
    monkeypatch.setattr(store, "JARVIS_DIR", tmp_path)
    yield


class TestAppStateManagement:
    def test_init(self):
        app = JarvisApp()
        assert app.running is True
        assert app.selected_idx == 0
        assert app.filter_text == ""
        assert app.show_done is False

    def test_reload_empty(self):
        app = JarvisApp()
        app.reload_tasks()
        assert app.tasks == []
        assert app.selected_idx == 0

    def test_reload_with_tasks(self):
        store.create_task(Task(id="a", name="task a"))
        store.create_task(Task(id="b", name="task b"))
        app = JarvisApp()
        app.reload_tasks()
        assert len(app.tasks) == 2

    def test_selected_task_empty(self):
        app = JarvisApp()
        app.reload_tasks()
        assert app.selected_task() is None

    def test_selected_task(self):
        store.create_task(Task(id="a", name="task a"))
        app = JarvisApp()
        app.reload_tasks()
        assert app.selected_task().name == "task a"

    def test_selection_clamped(self):
        store.create_task(Task(id="a", name="task a"))
        app = JarvisApp()
        app.selected_idx = 999
        app.reload_tasks()
        assert app.selected_idx == 0

    def test_filter(self):
        store.create_task(Task(id="a", name="fix flaky test"))
        store.create_task(Task(id="b", name="kafka investigation"))
        app = JarvisApp()
        app.filter_text = "kafka"
        app.reload_tasks()
        assert len(app.tasks) == 1
        assert app.tasks[0].name == "kafka investigation"

    def test_show_done(self):
        t = Task(id="a", name="done task", status="done")
        store.create_task(t)
        store.create_task(Task(id="b", name="active task"))

        app = JarvisApp()
        app.reload_tasks()
        assert len(app.tasks) == 1  # only active

        app.show_done = True
        app.reload_tasks()
        assert len(app.tasks) == 2


class TestHandleInput:
    def test_quit(self):
        app = JarvisApp()
        app.handle_input("/q")
        assert app.running is False

    def test_ls_toggles_done(self):
        app = JarvisApp()
        assert app.show_done is False
        app.handle_input("/ls")
        assert app.show_done is True
        app.handle_input("/ls")
        assert app.show_done is False

    def test_numeric_select(self):
        store.create_task(Task(id="a", name="first"))
        store.create_task(Task(id="b", name="second"))
        app = JarvisApp()
        app.reload_tasks()
        app.handle_input("2")
        assert app.selected_idx == 1

    def test_search_via_text(self):
        store.create_task(Task(id="a", name="fix flaky test"))
        store.create_task(Task(id="b", name="kafka schema"))
        app = JarvisApp()
        app.reload_tasks()
        app.handle_input("kafka")
        assert app.filter_text == "kafka"

    def test_noop_on_empty(self):
        app = JarvisApp()
        app.handle_input("")  # should not crash
        assert app.running is True
