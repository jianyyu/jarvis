package searchindex

import (
	"strings"
	"testing"
)

func TestParseTranscript_RealPromptAndReply(t *testing.T) {
	jsonl := strings.Join([]string{
		`{"type":"ai-title","aiTitle":"Socket deadlock fix"}`,
		`{"type":"user","message":{"role":"user","content":"the socket hangs on detach"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"The daemon never closes the attached client connection, so readPTY blocks forever on the write."}]}}`,
	}, "\n")

	ps, err := ParseTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ps.AITitle != "Socket deadlock fix" {
		t.Errorf("AITitle = %q", ps.AITitle)
	}
	if ps.InitialPrompt != "the socket hangs on detach" {
		t.Errorf("InitialPrompt = %q", ps.InitialPrompt)
	}
	if !strings.Contains(ps.UserText, "the socket hangs on detach") {
		t.Errorf("UserText missing prompt: %q", ps.UserText)
	}
	if !strings.Contains(ps.AssistantText, "readPTY blocks forever") {
		t.Errorf("AssistantText missing reply: %q", ps.AssistantText)
	}
	if strings.Contains(ps.AssistantText, "hmm") {
		t.Errorf("AssistantText should not include thinking blocks: %q", ps.AssistantText)
	}
}

func TestParseTranscript_SkipsSyntheticUser(t *testing.T) {
	jsonl := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"<system-reminder>be nice</system-reminder>"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"file contents"}]}}`,
		`{"type":"user","message":{"role":"user","content":"[Request interrupted by user]"}}`,
		`{"type":"user","message":{"role":"user","content":"actually find the marketplace session"}}`,
	}, "\n")

	ps, err := ParseTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// The first *real* prompt must be the 4th record, not the synthetic ones.
	if ps.InitialPrompt != "actually find the marketplace session" {
		t.Errorf("InitialPrompt = %q, want the real prompt", ps.InitialPrompt)
	}
	if strings.Contains(ps.UserText, "system-reminder") ||
		strings.Contains(ps.UserText, "file contents") ||
		strings.Contains(ps.UserText, "Request interrupted") {
		t.Errorf("UserText leaked synthetic content: %q", ps.UserText)
	}
}

func TestParseTranscript_SkipsToolNoise(t *testing.T) {
	longReply := "This is a substantive assistant reply that is comfortably longer than the fifty character noise threshold."
	jsonl := strings.Join([]string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Let me read that."}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"[Tool: Bash] running a command that is otherwise long enough to pass the length gate easily."}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + longReply + `"}]}}`,
	}, "\n")

	ps, err := ParseTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if strings.Contains(ps.AssistantText, "Let me read") {
		t.Errorf("short filler not filtered: %q", ps.AssistantText)
	}
	if strings.Contains(ps.AssistantText, "[Tool:") {
		t.Errorf("tool marker not filtered: %q", ps.AssistantText)
	}
	if !strings.Contains(ps.AssistantText, "substantive assistant reply") {
		t.Errorf("real reply dropped: %q", ps.AssistantText)
	}
}

func TestParseTranscript_TruncatesLongReply(t *testing.T) {
	body := strings.Repeat("A", 500) + strings.Repeat("B", 400) + strings.Repeat("C", 200) // 1100 chars
	jsonl := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + body + `"}]}}`

	ps, err := ParseTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strings.Contains(ps.AssistantText, "…") {
		t.Errorf("expected ellipsis in truncated reply: %q", ps.AssistantText)
	}
	if strings.Count(ps.AssistantText, "B") == 400 {
		t.Errorf("middle should have been dropped by truncation")
	}
	// Kept head (A) and tail (C), dropped the middle (B).
	if !strings.HasPrefix(ps.AssistantText, strings.Repeat("A", 500)) {
		t.Errorf("head not preserved")
	}
	if !strings.HasSuffix(strings.TrimRight(ps.AssistantText, "\n"), strings.Repeat("C", 200)) {
		t.Errorf("tail not preserved")
	}
}

func TestParseTranscript_InitialPromptTruncated(t *testing.T) {
	prompt := strings.Repeat("x", 500)
	jsonl := `{"type":"user","message":{"role":"user","content":"` + prompt + `"}}`
	ps, err := ParseTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len([]rune(ps.InitialPrompt)) > 200 {
		t.Errorf("InitialPrompt not truncated to 200 runes: got %d", len([]rune(ps.InitialPrompt)))
	}
}
