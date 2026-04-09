package sidecar

import (
	"testing"

	"jarvis/internal/config"
)

func TestExtractToolName_OldFormat(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Allow Bash? (y/n)", "Bash"},
		{"Allow Read? (y/n)", "Read"},
		{"\x1b[1mAllow Edit?\x1b[0m", "Edit"},
	}
	for _, tt := range tests {
		got := ExtractToolName(tt.input)
		if got != tt.want {
			t.Errorf("ExtractToolName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractToolName_NewMenuFormat(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name: "Read file prompt",
			input: `Read file

  Search(pattern: "**/*slack-monitor*", path: "~/.claude")

 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, allow reading from .claude/ during this session
   3. No`,
			want: "Read",
		},
		{
			name: "Bash prompt",
			input: `Bash

  go build ./...

 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, and don't ask again for Bash commands
   3. No`,
			want: "Bash",
		},
		{
			name: "Edit prompt",
			input: `Edit file

  internal/sidecar/daemon.go

 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, and don't ask again for Edit commands
   3. No`,
			want: "Edit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractToolName(tt.input)
			if got != tt.want {
				t.Errorf("ExtractToolName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractToolName_MCPFormat(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name: "Slack MCP tool",
			input: `Tool use

   claude.ai Slack - Search public messages and files(query: "slack bot token xoxb", limit: 10, sort: "timestamp",
   sort_dir: "desc") (MCP)
   Searches for messages, files in public Slack channels ONLY. Current logged in user's user_id is U050RJFF7T3.

 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, and don't ask again for claude.ai Slack - Search public messages and files commands in /home/jianyu.zhou/jarvis
   3. No`,
			want: "mcp",
		},
		{
			name: "Generic MCP tool",
			input: `Tool use

   some-server - do-something(arg: "value") (MCP)

 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, and don't ask again
   3. No`,
			want: "mcp",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractToolName(tt.input)
			if got != tt.want {
				t.Errorf("ExtractToolName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractToolName_Empty(t *testing.T) {
	got := ExtractToolName("some random text with no tool")
	if got != "" {
		t.Errorf("ExtractToolName() = %q, want empty", got)
	}
}

func TestMatchesTool_Exact(t *testing.T) {
	tools := config.ToolMatch{"Read", "Bash", "Edit"}
	if !matchesTool(tools, "Read") {
		t.Error("expected Read to match")
	}
	if !matchesTool(tools, "read") {
		t.Error("expected case-insensitive match")
	}
	if matchesTool(tools, "Write") {
		t.Error("expected Write not to match")
	}
}

func TestMatchesTool_Wildcard(t *testing.T) {
	tools := config.ToolMatch{"mcp__*"}
	if !matchesTool(tools, "mcp__slack") {
		t.Error("expected mcp__slack to match mcp__*")
	}
	if !matchesTool(tools, "mcp__github") {
		t.Error("expected mcp__github to match mcp__*")
	}
	if matchesTool(tools, "Bash") {
		t.Error("expected Bash not to match mcp__*")
	}
}

func TestMatchesTool_MCPShortName(t *testing.T) {
	// "mcp" (extracted from MCP tool prompts) should match "mcp" in tool list
	tools := config.ToolMatch{"mcp"}
	if !matchesTool(tools, "mcp") {
		t.Error("expected mcp to match mcp")
	}
}

func TestEvaluateApproval_ReadApproved(t *testing.T) {
	policies := []config.ApprovalRule{
		{Tool: config.ToolMatch{"Read", "Grep", "Glob"}, Action: config.ActionApprove},
	}

	detail := `Read file

  Search(pattern: "**/*slack-monitor*", path: "~/.claude")

 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, allow reading from .claude/ during this session
   3. No`

	decision := EvaluateApproval(policies, detail)
	if decision.Action != config.ActionApprove {
		t.Errorf("expected approve, got %q", decision.Action)
	}
	if decision.Rule == nil {
		t.Error("expected matched rule, got nil")
	}
}

func TestEvaluateApproval_MCPApproved(t *testing.T) {
	policies := []config.ApprovalRule{
		{Tool: config.ToolMatch{"Read", "Grep", "Glob", "mcp"}, Action: config.ActionApprove},
	}

	detail := `Tool use

   claude.ai Slack - Search public messages and files(query: "slack bot token xoxb", limit: 10) (MCP)

 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, and don't ask again
   3. No`

	decision := EvaluateApproval(policies, detail)
	if decision.Action != config.ActionApprove {
		t.Errorf("expected approve, got %q", decision.Action)
	}
}

func TestEvaluateApproval_BashDangerousBlocked(t *testing.T) {
	policies := []config.ApprovalRule{
		{Tool: config.ToolMatch{"Bash"}, CommandMatches: "rm|drop|force|reset --hard", Action: config.ActionAskHuman},
		{Tool: config.ToolMatch{"Bash"}, Action: config.ActionApprove},
	}

	// Safe command
	safeDetail := `Bash

  go build ./...

 Do you want to proceed?
 ❯ 1. Yes
   3. No`

	decision := EvaluateApproval(policies, safeDetail)
	if decision.Action != config.ActionApprove {
		t.Errorf("safe bash: expected approve, got %q", decision.Action)
	}

	// Dangerous command
	dangerDetail := `Bash

  rm -rf /tmp/foo

 Do you want to proceed?
 ❯ 1. Yes
   3. No`

	decision = EvaluateApproval(policies, dangerDetail)
	if decision.Action != config.ActionAskHuman {
		t.Errorf("dangerous bash: expected ask_human, got %q", decision.Action)
	}
}

func TestEvaluateApproval_NoMatchDefaultsToAskHuman(t *testing.T) {
	policies := []config.ApprovalRule{
		{Tool: config.ToolMatch{"Read"}, Action: config.ActionApprove},
	}

	detail := `Write file

  /tmp/foo.txt

 Do you want to proceed?
 ❯ 1. Yes
   3. No`

	decision := EvaluateApproval(policies, detail)
	if decision.Action != config.ActionAskHuman {
		t.Errorf("expected ask_human for unmatched tool, got %q", decision.Action)
	}
}
