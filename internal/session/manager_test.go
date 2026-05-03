package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInjectClaudeSettings(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "bare claude",
			in:   "claude",
			want: "claude --settings '/p.json'",
		},
		{
			name: "claude with resume",
			in:   "claude --resume abc",
			want: "claude --settings '/p.json' --resume abc",
		},
		{
			name: "claude with quoted append-system-prompt",
			in:   "claude --resume abc --append-system-prompt 'cd /x'",
			want: "claude --settings '/p.json' --resume abc --append-system-prompt 'cd /x'",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := injectClaudeSettings(tc.in, "/p.json")
			if got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestWriteClaudeSettings(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	sessionID := "abc123"
	path, err := writeClaudeSettings(sessionID)
	if err != nil {
		t.Fatalf("writeClaudeSettings: %v", err)
	}

	wantDir := filepath.Join(tmp, "sessions", sessionID)
	if filepath.Dir(path) != wantDir {
		t.Errorf("settings written to %s, expected dir %s", path, wantDir)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("settings not valid JSON: %v\n%s", err, data)
	}

	hooks, _ := parsed["hooks"].(map[string]any)
	if hooks == nil {
		t.Fatal("settings missing hooks key")
	}
	starts, _ := hooks["SessionStart"].([]any)
	if len(starts) != 1 {
		t.Fatalf("expected 1 SessionStart group, got %d: %v", len(starts), starts)
	}
	group := starts[0].(map[string]any)
	cmds, _ := group["hooks"].([]any)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 hook command, got %d", len(cmds))
	}
	hook := cmds[0].(map[string]any)
	cmd, _ := hook["command"].(string)
	if !strings.Contains(cmd, "hook-relay SessionStart") {
		t.Errorf("hook command missing 'hook-relay SessionStart': %q", cmd)
	}
	if hook["type"] != "command" {
		t.Errorf("hook type: got %v want \"command\"", hook["type"])
	}
}
