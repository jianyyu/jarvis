package autorename

import (
	"encoding/json"
	"fmt"
	"strings"
)

const maxTitleRunes = 60

// SanitizeTitle normalizes a model-produced title: first line only,
// surrounding quotes stripped, whitespace collapsed, length capped.
// Returns "" if nothing usable remains.
func SanitizeTitle(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'`“”‘’")
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxTitleRunes {
		s = strings.TrimSpace(string(r[:maxTitleRunes]))
	}
	return s
}

// claudeResult is the subset of `claude -p --output-format json` we consume.
type claudeResult struct {
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	IsError   bool   `json:"is_error"`
}

// parseClaudeOutput extracts the sanitized title and the forked session ID
// from headless claude stdout. The fork ID is returned even on error so the
// caller can always clean up the temporary forked JSONL.
func parseClaudeOutput(out []byte) (title, forkSessionID string, err error) {
	var r claudeResult
	if err := json.Unmarshal(out, &r); err != nil {
		return "", "", fmt.Errorf("parse claude output: %w", err)
	}
	if r.IsError {
		return "", r.SessionID, fmt.Errorf("claude returned is_error")
	}
	title = SanitizeTitle(r.Result)
	if title == "" {
		return "", r.SessionID, fmt.Errorf("empty title after sanitization")
	}
	return title, r.SessionID, nil
}
