package searchindex

import (
	"strings"
	"testing"
	"unicode/utf8"
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

func TestParseTranscript_OversizedLineSkipped(t *testing.T) {
	// One line larger than the 16MB per-line cap must be skipped as noise,
	// not abort the whole parse.
	huge := strings.Repeat("x", 17*1024*1024)
	jsonl := strings.Join([]string{
		huge,
		`{"type":"user","message":{"role":"user","content":"the socket hangs on detach"}}`,
	}, "\n")

	ps, err := ParseTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("parse should not fail on an oversized line: %v", err)
	}
	if ps.InitialPrompt != "the socket hangs on detach" {
		t.Errorf("InitialPrompt = %q, want the record after the oversized line", ps.InitialPrompt)
	}
	if strings.Contains(ps.UserText, "xxxx") {
		t.Errorf("oversized line leaked into UserText")
	}
}

func TestParseTranscript_MultimodalUserPrompt(t *testing.T) {
	jsonl := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"AAAA"}},{"type":"text","text":"why does this screenshot show a deadlock"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"file contents"}]}}`,
	}, "\n")

	ps, err := ParseTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ps.InitialPrompt != "why does this screenshot show a deadlock" {
		t.Errorf("InitialPrompt = %q, want text from multimodal prompt", ps.InitialPrompt)
	}
	if !strings.Contains(ps.UserText, "why does this screenshot show a deadlock") {
		t.Errorf("UserText missing multimodal prompt text: %q", ps.UserText)
	}
	if strings.Contains(ps.UserText, "file contents") {
		t.Errorf("tool_result carrier leaked into UserText: %q", ps.UserText)
	}
}

func TestParseTranscript_CJKTruncation(t *testing.T) {
	body := strings.Repeat("中", 500) + strings.Repeat("文", 200) + strings.Repeat("字", 200) // 900 runes
	jsonl := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + body + `"}]}}`

	ps, err := ParseTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := ps.AssistantText
	if !utf8.ValidString(got) {
		t.Fatalf("truncated reply is not valid UTF-8")
	}
	if n := utf8.RuneCountInString(got); n != 500+1+200 {
		t.Errorf("rune count = %d, want %d", n, 500+1+200)
	}
	if !strings.HasPrefix(got, strings.Repeat("中", 500)) {
		t.Errorf("head not preserved as 500 '中' runes")
	}
	if !strings.HasSuffix(got, strings.Repeat("字", 200)) {
		t.Errorf("tail not preserved as 200 '字' runes")
	}
}

func TestParseTranscript_AllSynthetic(t *testing.T) {
	jsonl := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"<system-reminder>be nice</system-reminder>"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"file contents"}]}}`,
		`{"type":"user","message":{"role":"user","content":"[Request interrupted by user]"}}`,
		`{"type":"user","message":{"role":"user","content":"Caveat: the messages below were generated"}}`,
	}, "\n")

	ps, err := ParseTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ps.InitialPrompt != "" {
		t.Errorf("InitialPrompt = %q, want empty for all-synthetic transcript", ps.InitialPrompt)
	}
	if ps.UserText != "" {
		t.Errorf("UserText = %q, want empty for all-synthetic transcript", ps.UserText)
	}
}

func TestParseTranscript_SkipsIsMetaInjection(t *testing.T) {
	// Injected skill/command expansions are user-role records flagged with
	// top-level isMeta:true; they must never enter InitialPrompt/UserText.
	jsonl := strings.Join([]string{
		`{"type":"user","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"Initialize the current Jarvis session with a title, worktree, and branch based on this task"}]}}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"[Your previous response was interrupted]"}}`,
		`{"type":"user","message":{"role":"user","content":"the socket hangs on detach"}}`,
	}, "\n")

	ps, err := ParseTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ps.InitialPrompt != "the socket hangs on detach" {
		t.Errorf("InitialPrompt = %q, want the real prompt, not the isMeta injection", ps.InitialPrompt)
	}
	if strings.Contains(ps.UserText, "Initialize the current") {
		t.Errorf("isMeta array-content injection leaked into UserText: %q", ps.UserText)
	}
	if strings.Contains(ps.UserText, "previous response") {
		t.Errorf("isMeta string-content injection leaked into UserText: %q", ps.UserText)
	}
}
