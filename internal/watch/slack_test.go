package watch

import (
	"testing"
	"time"
)

func TestSlackEventKey(t *testing.T) {
	ev := SlackEvent{
		ChannelID:   "C123",
		ThreadTS:    "1234567890.123456",
		Text:        "hey can you check the kafka lag?",
		SenderName:  "alice",
		ChannelName: "#oncall",
	}

	key := ev.ContextKey()
	if key != "slack:C123/1234567890.123456" {
		t.Errorf("context key: got %q", key)
	}
}

func TestSlackEventKeyDM(t *testing.T) {
	ev := SlackEvent{
		ChannelID:  "D456",
		ThreadTS:   "",
		MessageTS:  "1234567890.000001",
		Text:       "hello",
		SenderName: "bob",
		IsDM:       true,
	}

	key := ev.ContextKey()
	if key != "slack:D456/1234567890.000001" {
		t.Errorf("context key: got %q", key)
	}
}

func TestSlackEventSessionName(t *testing.T) {
	ev := SlackEvent{
		SenderName:  "alice",
		ChannelName: "#oncall",
		IsDM:        false,
	}
	name := ev.SessionName()
	if name != "slack: alice in #oncall" {
		t.Errorf("session name: got %q", name)
	}

	dm := SlackEvent{
		SenderName: "bob",
		IsDM:       true,
	}
	dmName := dm.SessionName()
	if dmName != "slack: DM from bob" {
		t.Errorf("DM session name: got %q", dmName)
	}
}

func TestSlackEventSystemPrompt(t *testing.T) {
	ev := SlackEvent{
		SenderName:  "alice",
		ChannelName: "#oncall",
		Text:        "can you check the kafka lag?",
		Timestamp:   time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC),
	}

	prompt := ev.SystemPrompt()
	if prompt == "" {
		t.Fatal("prompt should not be empty")
	}
	if !containsStr(prompt, "alice") {
		t.Error("prompt should mention sender")
	}
	if !containsStr(prompt, "#oncall") {
		t.Error("prompt should mention channel")
	}
	if !containsStr(prompt, "kafka lag") {
		t.Error("prompt should include message text")
	}
	if !containsStr(prompt, "Do NOT") {
		t.Error("prompt should include safety guardrail")
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
