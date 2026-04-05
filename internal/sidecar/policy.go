package sidecar

// policy.go — Matches approval prompts against auto-approve policy rules.
//
// When the sidecar detects an approval prompt (e.g., "Allow Bash?"), it calls
// EvaluateApproval to decide whether to auto-approve or wait for the human.
// The decision is based on rules from ~/.jarvis/config.yaml.

import (
	"regexp"
	"strings"

	"jarvis/internal/config"
)

// ApprovalDecision is the result of evaluating an approval prompt against policies.
type ApprovalDecision struct {
	Action config.ApprovalAction
	Rule   *config.ApprovalRule // the rule that matched, nil if no match
}

// toolNamePattern extracts the tool name from "Allow ToolName?" prompts.
// Handles ANSI escape codes that may be embedded in PTY output.
var toolNamePattern = regexp.MustCompile(`(?i)Allow\s+(\w+)`)

// ExtractToolName pulls the tool name from an approval prompt string.
// Returns empty string if no tool name found.
//
// Examples:
//
//	"Allow Bash? (y/n)"         → "Bash"
//	"Allow Read? (y/n)"         → "Read"
//	"\x1b[1mAllow Edit?\x1b[0m" → "Edit"
func ExtractToolName(detail string) string {
	// Strip ANSI escape sequences for cleaner matching.
	cleaned := stripANSI(detail)
	m := toolNamePattern.FindStringSubmatch(cleaned)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

// EvaluateApproval checks an approval prompt against the auto-approve policies.
// It returns the decision: approve, ask_human, or no match (ask_human by default).
//
// The detail parameter is the raw PTY output that triggered the approval detection.
// It typically contains context like the command being run, spread across
// multiple lines before the "Allow X?" prompt.
func EvaluateApproval(policies []config.ApprovalRule, detail string) ApprovalDecision {
	toolName := ExtractToolName(detail)
	if toolName == "" {
		// Can't identify the tool — don't auto-approve.
		return ApprovalDecision{Action: config.ActionAskHuman}
	}

	for i := range policies {
		rule := &policies[i]
		if !matchesTool(rule.Tool, toolName) {
			continue
		}

		// Tool matches. Check command_matches regex if present.
		if rule.CommandMatches != "" {
			re, err := regexp.Compile("(?i)" + rule.CommandMatches)
			if err != nil {
				continue // bad regex, skip rule
			}
			if !re.MatchString(detail) {
				continue // command doesn't match
			}
		}

		return ApprovalDecision{Action: rule.Action, Rule: rule}
	}

	// No rule matched — default to asking the human.
	return ApprovalDecision{Action: config.ActionAskHuman}
}

// matchesTool checks if toolName matches any entry in the rule's tool list.
// Comparison is case-insensitive.
func matchesTool(tools config.ToolMatch, toolName string) bool {
	lower := strings.ToLower(toolName)
	for _, t := range tools {
		if strings.ToLower(t) == lower {
			return true
		}
	}
	return false
}
