// Package autorename gives untitled sessions a real name at TUI startup.
// It infers each title from the session's full Claude conversation via a
// headless `claude -p --resume --fork-session` call, so the running session
// is never attached to and its transcript is never modified.
package autorename

import (
	"os"
	"sync/atomic"

	"jarvis/internal/model"
	"jarvis/internal/searchindex"
	"jarvis/internal/session"
	"jarvis/internal/store"
)

// UntitledName is the placeholder name given to `jarvis chat` sessions.
const UntitledName = "(untitled chat)"

// runInFlight makes Run single-flight per process (see Run).
var runInFlight atomic.Bool

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

// hasRealUserMessage reports whether the session's transcript contains at
// least one human-typed message (system reminders, hook output and other
// synthetic records don't count). A session with no real content yet can't
// be named meaningfully — it stays untitled until the next scan.
func hasRealUserMessage(sess *model.Session) bool {
	path := session.SessionJSONLPath(sess.ClaudeSessionID, sess.LaunchDir)
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	ps, _ := searchindex.ParseTranscript(f)
	return ps.InitialPrompt != ""
}

// Run scans for untitled sessions and renames each one whose transcript has
// real content. Sequential on purpose: one headless claude process at a time.
// Failures skip the session silently — this is a best-effort background
// enhancement and must never surface errors into the TUI.
// notify (optional) fires after each successful rename.
func Run(gen Generator, notify func(sessionID, newName string)) {
	// The TUI re-dispatches Run on every dashboard (re)entry; a scan can take
	// minutes, so overlapping calls would double-invoke claude for the same
	// sessions. Only one scan runs per process; extras return immediately.
	if !runInFlight.CompareAndSwap(false, true) {
		return
	}
	defer runInFlight.Store(false)

	sessions, err := store.ListSessions(&store.SessionFilter{
		StatusIn: []model.SessionStatus{model.StatusActive, model.StatusSuspended},
	})
	if err != nil {
		return
	}
	for _, sess := range FindCandidates(sessions) {
		if !hasRealUserMessage(sess) {
			continue
		}
		title, err := gen.Title(sess)
		if err != nil {
			continue
		}
		// Title generation can take minutes; if the user manually renamed
		// the session meanwhile, their choice wins.
		if cur, err := store.GetSession(sess.ID); err != nil || cur.Name != UntitledName {
			continue
		}
		if _, err := store.RenameSession(sess.ID, title); err != nil {
			continue
		}
		if notify != nil {
			notify(sess.ID, title)
		}
	}
}
