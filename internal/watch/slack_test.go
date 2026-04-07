package watch

import (
	"testing"
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
	// DMs use channel ID only (one session per DM conversation)
	if key != "slack:D456" {
		t.Errorf("context key: got %q, want %q", key, "slack:D456")
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

func TestSlackEventInitialPrompt(t *testing.T) {
	ev := SlackEvent{
		SenderName:  "alice",
		ChannelName: "#oncall",
		Permalink:   "https://example.slack.com/archives/C123/p456",
	}

	prompt := ev.InitialPrompt()
	if prompt == "" {
		t.Fatal("prompt should not be empty")
	}
	if !containsStr(prompt, "https://example.slack.com/archives/C123/p456") {
		t.Error("prompt should include permalink")
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
