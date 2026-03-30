package sidecar

import (
	"regexp"
	"time"

	"jarvis/v2/internal/model"
)

var approvalPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)Allow\s+\w+.*\?`),
	regexp.MustCompile(`\(y\/n\)`),
	regexp.MustCompile(`(?i)do you want to proceed`),
}

// Patterns that indicate Claude Code is waiting for user input (idle at prompt)
var waitingForInputPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^>\s*$`),           // Claude Code's ">" prompt on its own line
	regexp.MustCompile(`\x1b\[\d*[GH]>\s*$`),   // ">" prompt with ANSI cursor positioning
}

const idleTimeout = 5 * time.Second

// DetectState examines PTY output and returns the inferred sidecar state.
func DetectState(line []byte, timeSinceLastOutput time.Duration) (model.SidecarState, string) {
	// Check for approval prompts first (highest priority)
	for _, p := range approvalPatterns {
		if p.Match(line) {
			return model.StateWaitingForApproval, string(line)
		}
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
