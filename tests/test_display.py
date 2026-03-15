from datetime import datetime, timedelta, timezone

from jarvis.display import format_relative_time


class TestFormatRelativeTime:
    def _ago(self, **kwargs) -> datetime:
        return datetime.now(timezone.utc) - timedelta(**kwargs)

    def test_just_now(self):
        assert format_relative_time(self._ago(seconds=30)) == "just now"

    def test_minutes(self):
        assert format_relative_time(self._ago(minutes=5)) == "5m ago"

    def test_hours(self):
        assert format_relative_time(self._ago(hours=3)) == "3h ago"

    def test_days(self):
        assert format_relative_time(self._ago(days=2)) == "2d ago"

    def test_weeks(self):
        assert format_relative_time(self._ago(weeks=2)) == "2w ago"

    def test_months(self):
        assert format_relative_time(self._ago(days=60)) == "2mo ago"

    def test_years(self):
        assert format_relative_time(self._ago(days=400)) == "1y ago"

    def test_naive_datetime_treated_as_utc(self):
        # Naive datetime should not crash
        dt = datetime.now() - timedelta(hours=1)
        result = format_relative_time(dt)
        assert "ago" in result or result == "just now"
