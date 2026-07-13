// Package autorename gives untitled sessions a real name at TUI startup.
// It infers each title from the session's full Claude conversation via a
// headless `claude -p --resume --fork-session` call, so the running session
// is never attached to and its transcript is never modified.
package autorename

import (
	"jarvis/internal/model"
)

// UntitledName is the placeholder name given to `jarvis chat` sessions.
const UntitledName = "(untitled chat)"

// FindCandidates returns the sessions eligible for auto-rename: still
// untitled, not finished, and with a known Claude session to read context
// from. Sessions without a ClaudeSessionID are skipped (not guessed at):
// untitled chats share a LaunchDir, so "latest JSONL in the project dir"
// could belong to a different session.
func FindCandidates(sessions []*model.Session) []*model.Session {
	var out []*model.Session
	for _, s := range sessions {
		if s.Name != UntitledName {
			continue
		}
		if s.Status != model.StatusActive && s.Status != model.StatusSuspended {
			continue
		}
		if s.ClaudeSessionID == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}
