package agent

import "testing"

func TestFromArgv0(t *testing.T) {
	cases := []struct {
		arg0 string
		want string // expected Name
	}{
		{"jarvis", "claude"},
		{"ijarvis", "isaac"},
		{"/usr/local/bin/jarvis", "claude"},
		{"/home/user/.local/bin/ijarvis", "isaac"},
		{"./ijarvis", "isaac"},
		{"ijarvis.exe", "isaac"},
		{"jarvis.exe", "claude"},
		{"", "claude"},
		{"something-else", "claude"},
	}
	for _, c := range cases {
		if got := FromArgv0(c.arg0).Name; got != c.want {
			t.Errorf("FromArgv0(%q).Name = %q, want %q", c.arg0, got, c.want)
		}
	}
}

func TestFromArgv0Exec(t *testing.T) {
	if got := FromArgv0("ijarvis").Exec; got != "isaac" {
		t.Errorf("ijarvis Exec = %q, want isaac", got)
	}
	if got := FromArgv0("jarvis").Exec; got != "claude" {
		t.Errorf("jarvis Exec = %q, want claude", got)
	}
}
