package watch

import (
	"testing"
	"time"
)

func TestGitHubEventContextKey(t *testing.T) {
	ev := GitHubEvent{
		Owner:    "databricks-eng",
		Repo:     "universe",
		PRNumber: 1234,
	}
	key := ev.ContextKey()
	if key != "github:databricks-eng/universe#1234" {
		t.Errorf("context key: got %q", key)
	}
}

func TestGitHubEventSessionNameReviewRequest(t *testing.T) {
	ev := GitHubEvent{
		PRNumber: 1234,
		PRTitle:  "Fix auth middleware",
		Reason:   "review_requested",
	}
	name := ev.SessionName()
	if name != "gh: Review PR #1234 — Fix auth middleware" {
		t.Errorf("session name: got %q", name)
	}
}

func TestGitHubEventSessionNameAuthor(t *testing.T) {
	ev := GitHubEvent{
		PRNumber: 5678,
		PRTitle:  "Add rate limiting",
		Reason:   "author",
	}
	name := ev.SessionName()
	if name != "gh: PR #5678 — Add rate limiting" {
		t.Errorf("session name: got %q", name)
	}
}

func TestGitHubEventInitialPromptReviewRequest(t *testing.T) {
	ev := GitHubEvent{
		PRNumber: 1234,
		PRURL:    "https://github.com/databricks-eng/universe/pull/1234",
		Reason:   "review_requested",
	}
	prompt := ev.InitialPrompt()
	if !containsStr(prompt, "https://github.com/databricks-eng/universe/pull/1234") {
		t.Error("prompt should include PR URL")
	}
	if !containsStr(prompt, "gh pr diff 1234") {
		t.Error("prompt should include gh pr diff command")
	}
	if !containsStr(prompt, "Do NOT") {
		t.Error("prompt should include safety guardrail")
	}
}

func TestGitHubEventInitialPromptAuthor(t *testing.T) {
	ev := GitHubEvent{
		PRNumber: 5678,
		PRURL:    "https://github.com/databricks-eng/universe/pull/5678",
		HeadRef:  "stack/fix-auth",
		Reason:   "author",
		Verdict:  VerdictChangesRequested,
		Comments: []PRComment{
			{User: "alice", Body: "This needs error handling", Path: "auth.go", Line: 42, CreatedAt: time.Now()},
		},
	}
	prompt := ev.InitialPrompt()
	if !containsStr(prompt, "CHANGES_REQUESTED") {
		t.Error("prompt should include review verdict")
	}
	if !containsStr(prompt, "alice") {
		t.Error("prompt should include commenter name")
	}
	if !containsStr(prompt, "auth.go") {
		t.Error("prompt should include file path")
	}
	if !containsStr(prompt, "stack/fix-auth") {
		t.Error("prompt should include head ref for branch verification")
	}
}

func TestGitHubEventFollowUpPrompt(t *testing.T) {
	ev := GitHubEvent{
		PRNumber: 1234,
		Verdict:  VerdictApproved,
		Comments: []PRComment{
			{User: "bob", Body: "LGTM", CreatedAt: time.Now()},
		},
	}
	prompt := ev.FollowUpPrompt()
	if !containsStr(prompt, "PR #1234") {
		t.Error("follow-up should include PR number")
	}
	if !containsStr(prompt, "APPROVED") {
		t.Error("follow-up should include verdict")
	}
	if !containsStr(prompt, "bob") {
		t.Error("follow-up should include commenter")
	}
}

func TestParseNotificationURL(t *testing.T) {
	tests := []struct {
		url    string
		expect int
	}{
		{"https://api.github.com/repos/databricks-eng/universe/pulls/1234", 1234},
		{"https://api.github.com/repos/databricks-eng/universe/pulls/9999999", 9999999},
		{"https://api.github.com/repos/other/repo/issues/100", 0},
	}
	for _, tt := range tests {
		got := parsePRNumberFromURL(tt.url)
		if got != tt.expect {
			t.Errorf("parsePRNumberFromURL(%q) = %d, want %d", tt.url, got, tt.expect)
		}
	}
}

func TestParsePRListJSON(t *testing.T) {
	jsonData := `[
		{"number": 1234, "headRefName": "stack/fix-auth"},
		{"number": 5678, "headRefName": "stack/add-feature"}
	]`
	m, err := parsePRListJSON(jsonData)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m["stack/fix-auth"] != 1234 {
		t.Errorf("fix-auth: got %d", m["stack/fix-auth"])
	}
	if m["stack/add-feature"] != 5678 {
		t.Errorf("add-feature: got %d", m["stack/add-feature"])
	}
}

func TestParseStackLS(t *testing.T) {
	output := `---------------Stack Info---------------
master (current)
  ├─✗ ● stack/fix-auth (!1)
  │       [https://github.com/databricks-eng/universe/pull/1234]
  ├─✗ · stack/local-only (!) [LOCAL]
  ├─✗ ● stack/add-feature (!1)
  │       [https://github.com/databricks-eng/universe/pull/5678]
----------------------------------------`
	m := parseStackLS(output)
	if m["stack/fix-auth"] != 1234 {
		t.Errorf("fix-auth: got %d", m["stack/fix-auth"])
	}
	if m["stack/add-feature"] != 5678 {
		t.Errorf("add-feature: got %d", m["stack/add-feature"])
	}
	if _, ok := m["stack/local-only"]; ok {
		t.Error("local-only should not be in map (no PR)")
	}
}

func TestFilterBotComments(t *testing.T) {
	p := &GitHubPoller{
		username:    "myuser",
		ignoreUsers: map[string]bool{"ci-bot": true},
	}

	if !p.shouldSkipUser("myuser", "User") {
		t.Error("should skip own user")
	}
	if !p.shouldSkipUser("MyUser", "User") {
		t.Error("should skip own user (case insensitive)")
	}
	if !p.shouldSkipUser("some-app[bot]", "Bot") {
		t.Error("should skip bot type")
	}
	if !p.shouldSkipUser("CI-Bot", "User") {
		t.Error("should skip ignored user (case insensitive)")
	}
	if p.shouldSkipUser("alice", "User") {
		t.Error("should not skip normal user")
	}
}

func TestReviewVerdictNone(t *testing.T) {
	ev := GitHubEvent{
		PRNumber: 1234,
		PRURL:    "https://example.com/pull/1234",
		HeadRef:  "stack/test",
		Reason:   "author",
		Verdict:  VerdictNone,
		Comments: []PRComment{
			{User: "alice", Body: "looks good"},
		},
	}
	prompt := ev.InitialPrompt()
	if containsStr(prompt, "Review verdict:") {
		t.Error("should not include verdict line when VerdictNone")
	}
}
