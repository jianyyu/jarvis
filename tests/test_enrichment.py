from jarvis.enrichment import (
    _extract_branch_from_command,
    handle_post_tool_use,
    handle_session_start,
)
from jarvis.models import ClaudeSession, Task


def _task(**kwargs) -> Task:
    return Task(id="t1", name="test", **kwargs)


class TestExtractBranch:
    def test_git_stack_create(self):
        assert _extract_branch_from_command("git stack create my-feature") == "my-feature"

    def test_git_checkout_b(self):
        assert _extract_branch_from_command("git checkout -b fix/bug-123") == "fix/bug-123"

    def test_git_switch_c(self):
        assert _extract_branch_from_command("git switch -c new-branch") == "new-branch"

    def test_git_branch(self):
        assert _extract_branch_from_command("git branch experiment") == "experiment"

    def test_no_match(self):
        assert _extract_branch_from_command("git status") is None

    def test_no_match_checkout_without_b(self):
        assert _extract_branch_from_command("git checkout main") is None


class TestHandleSessionStart:
    def test_adds_session(self):
        task = _task()
        changed = handle_session_start({"session_id": "abc-123"}, task)
        assert changed is True
        assert len(task.claude_sessions) == 1
        assert task.claude_sessions[0].id == "abc-123"

    def test_deduplicates(self):
        task = _task()
        task.claude_sessions.append(ClaudeSession(id="abc-123"))
        changed = handle_session_start({"session_id": "abc-123"}, task)
        assert changed is False
        assert len(task.claude_sessions) == 1

    def test_missing_session_id(self):
        task = _task()
        assert handle_session_start({}, task) is False

    def test_empty_session_id(self):
        task = _task()
        assert handle_session_start({"session_id": ""}, task) is False


class TestHandlePostToolUse:
    def test_detects_branch(self):
        task = _task()
        data = {"tool_name": "Bash", "tool_input": {"command": "git stack create cool-feature"}}
        changed = handle_post_tool_use(data, task)
        assert changed is True
        assert "cool-feature" in task.branches

    def test_ignores_non_bash(self):
        task = _task()
        data = {"tool_name": "Read", "tool_input": {"command": "git checkout -b x"}}
        assert handle_post_tool_use(data, task) is False

    def test_ignores_non_branch_commands(self):
        task = _task()
        data = {"tool_name": "Bash", "tool_input": {"command": "git status"}}
        assert handle_post_tool_use(data, task) is False

    def test_deduplicates_branches(self):
        task = _task(branches=["my-branch"])
        data = {"tool_name": "Bash", "tool_input": {"command": "git checkout -b my-branch"}}
        assert handle_post_tool_use(data, task) is False

    def test_empty_command(self):
        task = _task()
        data = {"tool_name": "Bash", "tool_input": {"command": ""}}
        assert handle_post_tool_use(data, task) is False
