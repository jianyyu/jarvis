from jarvis.models import (
    ClaudeSession,
    Reference,
    Task,
    _fallback_slug,
    generate_task_id,
    name_to_worktree_slug,
)


class TestGenerateTaskId:
    def test_returns_8_char_hex(self):
        tid = generate_task_id()
        assert len(tid) == 8
        int(tid, 16)  # should not raise

    def test_unique(self):
        ids = {generate_task_id() for _ in range(100)}
        assert len(ids) == 100


class TestFallbackSlug:
    """Test the deterministic fallback slug generation."""

    def test_basic(self):
        assert _fallback_slug("fix the flaky test") == "fix-flaky-test"

    def test_strips_filler_words(self):
        slug = _fallback_slug("investigate the kafka schema issue")
        assert "the" not in slug.split("-")
        assert slug == "investigate-kafka-schema-issue"

    def test_lowercase(self):
        assert _fallback_slug("Fix Flaky Test") == "fix-flaky-test"

    def test_strips_special_chars(self):
        assert _fallback_slug("fix bug #123 (urgent!)") == "fix-bug-123-urgent"

    def test_truncates_long_names(self):
        long_name = " ".join(f"word{i}" for i in range(20))
        slug = _fallback_slug(long_name)
        assert len(slug) <= 50

    def test_empty_after_filter(self):
        assert _fallback_slug("the a an") == ""


class TestNameToWorktreeSlug:
    """Test the full slug generation (may use AI or fallback)."""

    def test_returns_valid_slug(self):
        slug = name_to_worktree_slug("fix the flaky test")
        assert slug  # non-empty
        assert all(c in "abcdefghijklmnopqrstuvwxyz0123456789-" for c in slug)

    def test_max_length(self):
        long_name = " ".join(f"word{i}" for i in range(20))
        slug = name_to_worktree_slug(long_name)
        assert len(slug) <= 50

    def test_dedup_with_existing(self):
        slug1 = name_to_worktree_slug("fix bug")
        existing = {slug1}
        slug2 = name_to_worktree_slug("fix bug", existing)
        assert slug2 != slug1
        assert slug2.startswith(slug1)

    def test_dedup_multiple(self):
        slug = _fallback_slug("fix bug")
        existing = {slug, f"{slug}-2", f"{slug}-3"}
        result = name_to_worktree_slug("fix bug", existing)
        assert result not in existing

    def test_no_dedup_needed(self):
        existing = {"other-task"}
        slug = name_to_worktree_slug("fix bug", existing)
        assert slug not in existing


class TestReference:
    def test_roundtrip(self):
        ref = Reference(label="Slack thread", url="https://slack.com/test", type="slack")
        d = ref.to_dict()
        ref2 = Reference.from_dict(d)
        assert ref2.label == ref.label
        assert ref2.url == ref.url
        assert ref2.type == ref.type
        assert ref2.added_at == ref.added_at

    def test_from_dict_missing_added_at(self):
        ref = Reference.from_dict({"label": "x", "url": "y", "type": "link"})
        assert ref.added_at == ""


class TestClaudeSession:
    def test_roundtrip(self):
        s = ClaudeSession(id="abc-123")
        d = s.to_dict()
        s2 = ClaudeSession.from_dict(d)
        assert s2.id == "abc-123"
        assert s2.started_at == s.started_at


class TestTask:
    def test_defaults(self):
        t = Task(id="x", name="test")
        assert t.status == "active"
        assert t.branches == []
        assert t.claude_sessions == []
        assert t.references == []
        assert t.tags == []

    def test_roundtrip(self):
        t = Task(
            id="abc",
            name="test task",
            cwd="/tmp/test",
            branches=["main"],
            claude_sessions=[ClaudeSession(id="s1")],
            tags=["oncall"],
        )
        d = t.to_dict()
        t2 = Task.from_dict(d)
        assert t2.id == "abc"
        assert t2.name == "test task"
        assert t2.cwd == "/tmp/test"
        assert t2.branches == ["main"]
        assert len(t2.claude_sessions) == 1
        assert t2.claude_sessions[0].id == "s1"
        assert t2.tags == ["oncall"]
        # references are loaded separately
        assert t2.references == []

    def test_touch_updates_timestamp(self):
        t = Task(id="x", name="test")
        old = t.updated_at
        import time
        time.sleep(0.01)
        t.touch()
        assert t.updated_at > old
