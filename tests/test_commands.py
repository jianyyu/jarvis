from jarvis.commands import parse_command


class TestParseCommand:
    def test_new(self):
        assert parse_command("/new") == ("new", [])

    def test_done(self):
        assert parse_command("/done") == ("done", [])

    def test_cc(self):
        assert parse_command("/cc") == ("cc", [])

    def test_info(self):
        assert parse_command("/info") == ("info", [])

    def test_refs(self):
        assert parse_command("/refs") == ("refs", [])

    def test_note(self):
        assert parse_command("/note") == ("note", [])

    def test_ls(self):
        assert parse_command("/ls") == ("ls", [])

    def test_quit(self):
        assert parse_command("/q") == ("q", [])

    def test_empty(self):
        assert parse_command("") == ("noop", [])

    def test_whitespace(self):
        assert parse_command("   ") == ("noop", [])

    def test_plain_text_is_select(self):
        assert parse_command("hello") == ("select", ["hello"])

    def test_number_is_select(self):
        assert parse_command("3") == ("select", ["3"])

    def test_unknown_slash_is_search(self):
        assert parse_command("/foo") == ("search", ["foo"])

    def test_case_insensitive(self):
        assert parse_command("/NEW") == ("new", [])
        assert parse_command("/Done") == ("done", [])
