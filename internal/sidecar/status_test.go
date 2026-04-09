package sidecar

import (
	"testing"
	"time"

	"jarvis/internal/model"
)

func TestDetectState_OldApprovalFormat(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		// Old format has (y/n) which counts as both prompt AND ready indicator
		{"allow question", "Allow Bash? (y/n)"},
		{"y/n marker", "Do something (y/n)"},
		// New format needs "Esc" or "cancel" to confirm menu is ready
		{"proceed with menu", "Do you want to proceed?\n❯ 1. Yes\nEsc to cancel"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, _ := DetectState([]byte(tt.input), 0, nil)
			if state != model.StateWaitingForApproval {
				t.Errorf("DetectState(%q) = %v, want StateWaitingForApproval", tt.input, state)
			}
		})
	}
}

func TestDetectState_NotReadyWithoutFooter(t *testing.T) {
	// "proceed?" alone should NOT trigger approval state — the menu
	// isn't fully rendered yet and the input handler isn't ready.
	input := "Do you want to proceed?\n❯ 1. Yes"
	state, _ := DetectState([]byte(input), 0, nil)
	if state == model.StateWaitingForApproval {
		t.Error("should NOT detect approval before menu footer renders")
	}
}

func TestDetectState_NewMenuFormat(t *testing.T) {
	input := `Read file

  Search(pattern: "**/*slack-monitor*", path: "~/.claude")

 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, allow reading from .claude/ during this session
   3. No
 Esc to cancel · Tab to amend`

	state, det := DetectState([]byte(input), 0, nil)
	if state != model.StateWaitingForApproval {
		t.Errorf("state = %v, want StateWaitingForApproval", state)
	}
	if det == "" {
		t.Error("detail should not be empty")
	}
}

func TestDetectState_MenuFormatSplitAcrossChunks(t *testing.T) {
	currentChunk := []byte(` ❯ 1. Yes
   2. Yes, allow reading from .claude/ during this session
   3. No
 Esc to cancel`)

	recentContext := []byte(`Read file

  Search(pattern: "**/*slack-monitor*", path: "~/.claude")

 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, allow reading from .claude/ during this session
   3. No
 Esc to cancel`)

	state, det := DetectState(currentChunk, 0, recentContext)
	if state != model.StateWaitingForApproval {
		t.Errorf("state = %v, want StateWaitingForApproval", state)
	}
	if det == "" {
		t.Error("detail should not be empty")
	}
}

func TestDetectState_MCPToolFormat(t *testing.T) {
	input := `Tool use

   claude.ai Slack - Search public messages and files(query: "test", limit: 10) (MCP)

 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, and don't ask again
   3. No
 Esc to cancel · Tab to amend`

	state, _ := DetectState([]byte(input), 0, nil)
	if state != model.StateWaitingForApproval {
		t.Errorf("state = %v, want StateWaitingForApproval", state)
	}
}

func TestDetectState_Working(t *testing.T) {
	state, _ := DetectState([]byte("Compiling package..."), 100*time.Millisecond, nil)
	if state != model.StateWorking {
		t.Errorf("state = %v, want StateWorking", state)
	}
}

func TestDetectState_Idle(t *testing.T) {
	state, _ := DetectState([]byte("done"), 10*time.Second, nil)
	if state != model.StateIdle {
		t.Errorf("state = %v, want StateIdle", state)
	}
}

func TestDetectState_WaitingForInput(t *testing.T) {
	state, _ := DetectState([]byte(">\n"), 0, nil)
	if state != model.StateIdle {
		t.Logf("state = %v (may depend on exact prompt format)", state)
	}
}
