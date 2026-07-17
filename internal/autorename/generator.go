package autorename

import (
	"context"
	"os"
	"os/exec"
	"time"

	"jarvis/internal/model"
	"jarvis/internal/session"
)

// titlePrompt asks for the title and nothing else; the full conversation
// context comes from --resume, not from this prompt.
const titlePrompt = "Based on the entire conversation so far, output ONLY a 3-8 word title-case task name for this session. No explanation, no quotes, no trailing punctuation."

// Generator produces a display name for a session from its conversation.
type Generator interface {
	Title(sess *model.Session) (string, error)
}

// ClaudeGenerator infers a title by resuming the session's full context in
// a one-shot headless call. --fork-session keeps the rename exchange out of
// the original transcript; the forked JSONL is deleted afterwards. The
// headless call is granted no tools — it can only print text.
//
// Exec is the agent CLI to invoke (claude or isaac); empty defaults to
// "claude". Isaac wraps Claude Code and honors the same -p/--resume/
// --fork-session flags, so the same call works for both.
type ClaudeGenerator struct {
	Timeout time.Duration
	Exec    string
}

func (g ClaudeGenerator) Title(sess *model.Session) (string, error) {
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	exe := g.Exec
	if exe == "" {
		exe = "claude"
	}
	cmd := exec.CommandContext(ctx, exe, "-p",
		"--resume", sess.ClaudeSessionID,
		"--fork-session",
		"--output-format", "json",
		titlePrompt)
	cmd.Dir = sess.LaunchDir

	out, err := cmd.Output()
	if err != nil {
		// No parseable output means no fork session_id — if claude created a
		// forked transcript before failing, it can't be located for cleanup.
		return "", err
	}

	title, forkID, perr := parseClaudeOutput(out)
	if forkID != "" && forkID != sess.ClaudeSessionID {
		// Best-effort cleanup of the temporary forked transcript.
		os.Remove(session.SessionJSONLPath(forkID, sess.LaunchDir))
	}
	return title, perr
}
