package sidecar

import (
	"regexp"
	"time"

	"jarvis/internal/model"
)

// approvalPatterns detect the approval prompt question.
var approvalPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)Allow\s+\w+.*\?`),
	regexp.MustCompile(`\(y\/n\)`),
	regexp.MustCompile(`(?i)do\s*you\s*want\s*to\s*proceed`),
	regexp.MustCompile(`(?i)proceed\?`),
	regexp.MustCompile(`❯?\s*1\.\s*Yes`),
	regexp.MustCompile(`(?m)1\.Yes`),
}

// approvalReadyPatterns confirm the menu is fully rendered and ready for input.
// We require one of these to match BEFORE sending a keystroke, to avoid
// sending input before Claude Code's input handler is initialized.
var approvalReadyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`Esc`),                // "Esc to cancel" footer
	regexp.MustCompile(`cancel`),             // alternative footer text
	regexp.MustCompile(`(?i)Tab.*amend`),     // "Tab to amend" footer
	regexp.MustCompile(`\(y\/n\)`),           // old format is always ready
}

// Patterns that indicate Claude Code is waiting for user input (idle at prompt)
var waitingForInputPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^>\s*$`),           // Claude Code's ">" prompt on its own line
	regexp.MustCompile(`\x1b\[\d*[GH]>\s*$`),   // ">" prompt with ANSI cursor positioning
}

const idleTimeout = 5 * time.Second

// DetectState examines PTY output and returns the inferred sidecar state.
// recentContext is optional recent ring buffer content used to detect approval
// prompts that span multiple PTY read chunks.
func DetectState(line []byte, timeSinceLastOutput time.Duration, recentContext []byte) (model.SidecarState, string) {
	// Check for approval prompts first (highest priority).
	// Try the current chunk first, then fall back to recent context
	// since approval prompt text may arrive across multiple reads.
	//
	// We require BOTH an approval pattern AND a ready pattern to match,
	// because the approval prompt text ("proceed?") arrives before the
	// full menu is rendered and the input handler is ready. Sending a
	// keystroke too early will be silently dropped.
	for _, source := range [][]byte{line, recentContext} {
		if source == nil {
			continue
		}
		promptFound := false
		for _, p := range approvalPatterns {
			if p.Match(source) {
				promptFound = true
				break
			}
		}
		if !promptFound {
			continue
		}
		// Check that the menu is fully rendered (ready for input).
		readyFound := false
		for _, p := range approvalReadyPatterns {
			if p.Match(source) {
				readyFound = true
				break
			}
		}
		if !readyFound {
			continue
		}
		// Both matched — approval prompt is ready.
		detail := string(line)
		if recentContext != nil {
			detail = string(recentContext)
		}
		return model.StateWaitingForApproval, detail
	}

	// Check for Claude Code waiting for user input
	for _, p := range waitingForInputPatterns {
		if p.Match(line) {
			return model.StateIdle, "waiting for user input"
		}
	}

	// If output was recent, Claude is working
	if timeSinceLastOutput < idleTimeout {
		return model.StateWorking, ""
	}

	return model.StateIdle, ""
}
