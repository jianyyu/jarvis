package autorename

import "testing"

func TestSanitizeTitle(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Fix Login Bug", "Fix Login Bug"},
		{"  Fix Login Bug  \n", "Fix Login Bug"},
		{"\"Fix Login Bug\"", "Fix Login Bug"},
		{"'Fix   Login\tBug'", "Fix Login Bug"},
		{"Fix Login Bug\nExtra explanation line", "Fix Login Bug"},
		{"", ""},
		{"   \n  ", ""},
		// 70 x's: capped at 60 runes
		{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
			"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"[:60]},
	}
	for _, c := range cases {
		if got := SanitizeTitle(c.in); got != c.want {
			t.Errorf("SanitizeTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseClaudeOutput(t *testing.T) {
	out := []byte(`{"type":"result","subtype":"success","is_error":false,"result":"Fix Login Bug","session_id":"fork-123"}`)
	title, forkID, err := parseClaudeOutput(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if title != "Fix Login Bug" {
		t.Errorf("title = %q", title)
	}
	if forkID != "fork-123" {
		t.Errorf("forkID = %q", forkID)
	}
}

func TestParseClaudeOutputError(t *testing.T) {
	cases := [][]byte{
		[]byte(`not json`),
		[]byte(`{"is_error":true,"result":"something failed","session_id":"fork-1"}`),
		[]byte(`{"is_error":false,"result":"","session_id":"fork-2"}`), // empty title
	}
	for _, out := range cases {
		if _, _, err := parseClaudeOutput(out); err == nil {
			t.Errorf("parseClaudeOutput(%s): expected error, got nil", out)
		}
	}
}

func TestParseClaudeOutputErrorStillReturnsForkID(t *testing.T) {
	out := []byte(`{"is_error":true,"result":"boom","session_id":"fork-9"}`)
	_, forkID, err := parseClaudeOutput(out)
	if err == nil {
		t.Fatal("expected error")
	}
	if forkID != "fork-9" {
		t.Errorf("forkID = %q, want fork-9 (needed for cleanup even on error)", forkID)
	}
}
