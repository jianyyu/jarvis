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

const idleTimeout = 30 * time.Second

// DetectState examines a line of PTY output and returns the inferred sidecar state.
func DetectState(line []byte, timeSinceLastOutput time.Duration) (model.SidecarState, string) {
	for _, p := range approvalPatterns {
		if p.Match(line) {
			return model.StateWaitingForApproval, string(line)
		}
	}
	if timeSinceLastOutput < idleTimeout {
		return model.StateWorking, ""
	}
	return model.StateIdle, ""
}
