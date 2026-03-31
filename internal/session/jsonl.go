package session

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var nonAlphaNum = regexp.MustCompile(`[^a-zA-Z0-9]`)

// EncodeCWD encodes a path the same way Claude Code does for project directories.
func EncodeCWD(cwd string) string {
	return nonAlphaNum.ReplaceAllString(cwd, "-")
}

// projectDirs returns the candidate Claude project directories for a CWD.
// If the CWD is inside a git worktree, it also includes the main repo root's project dir.
func projectDirs(cwd string) []string {
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, ".claude", "projects")
	dirs := []string{filepath.Join(base, EncodeCWD(cwd))}

	// If CWD is a worktree, also check the git repo root
	cmd := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel")
	if out, err := cmd.Output(); err == nil {
		repoRoot := strings.TrimSpace(string(out))
		if repoRoot != cwd {
			dirs = append(dirs, filepath.Join(base, EncodeCWD(repoRoot)))
		}
	}
	return dirs
}

// SessionJSONLPath returns the path to a Claude Code session JSONL file.
// It checks the CWD's project dir first, then falls back to the git repo root's project dir.
func SessionJSONLPath(sessionID, cwd string) string {
	for _, dir := range projectDirs(cwd) {
		path := filepath.Join(dir, sessionID+".jsonl")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	// Not found anywhere — return the primary path (for error reporting)
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects", EncodeCWD(cwd), sessionID+".jsonl")
}

// FindLatestSession scans the Claude project dirs for the most recent valid session JSONL.
// Checks both the CWD's project dir and the git repo root's project dir for worktrees.
func FindLatestSession(cwd string) string {
	var bestID string
	var bestTime time.Time

	for _, dir := range projectDirs(cwd) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			id := strings.TrimSuffix(name, ".jsonl")
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.ModTime().After(bestTime) {
				if SessionIsValid(id, cwd) {
					bestTime = info.ModTime()
					bestID = id
				}
			}
		}
	}
	return bestID
}

// SessionIsValid checks if a JSONL file exists and has at least one user message.
func SessionIsValid(sessionID, cwd string) bool {
	path := SessionJSONLPath(sessionID, cwd)
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB line buffer
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, `"type":"user"`) || strings.Contains(line, `"type": "user"`) {
			return true
		}
	}
	return false
}

type jsonlEvent struct {
	Type    string          `json:"type"`
	Message json.RawMessage `json:"message,omitempty"`
}

type messageContent struct {
	Content []struct {
		Type string `json:"type"`
		Name string `json:"name,omitempty"`
	} `json:"content,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
}

// DeriveStatusFromJSONL reads the tail of a session JSONL and infers the last state.
func DeriveStatusFromJSONL(sessionID, cwd string) (lastState string, detail string, err error) {
	path := SessionJSONLPath(sessionID, cwd)
	f, err := os.Open(path)
	if err != nil {
		return "unknown", "", err
	}
	defer f.Close()

	// Read all lines (we only care about the last few, but need to scan forward)
	var lastEvents []jsonlEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var evt jsonlEvent
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			continue
		}
		lastEvents = append(lastEvents, evt)
		// Keep only last 50 events
		if len(lastEvents) > 50 {
			lastEvents = lastEvents[1:]
		}
	}

	if len(lastEvents) == 0 {
		return "unknown", "", nil
	}

	// Walk backward to determine state
	for i := len(lastEvents) - 1; i >= 0; i-- {
		evt := lastEvents[i]
		switch evt.Type {
		case "assistant":
			// Check if the last assistant message ended with a tool_use
			var msg messageContent
			if err := json.Unmarshal(evt.Message, &msg); err == nil {
				if msg.StopReason == "tool_use" {
					// Check if there's a corresponding tool_result after this
					hasResult := false
					for j := i + 1; j < len(lastEvents); j++ {
						if lastEvents[j].Type == "user" {
							hasResult = true
							break
						}
					}
					if !hasResult {
						return "waiting_for_approval", "tool use pending", nil
					}
				}
				if msg.StopReason == "end_turn" {
					return "idle", "waiting for user input", nil
				}
			}
			return "working", "", nil
		case "user":
			return "working", "processing user input", nil
		}
	}

	return "unknown", "", nil
}
