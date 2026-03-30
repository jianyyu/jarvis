package session

import (
	"bufio"
	"encoding/json"
	"os"
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

// SessionJSONLPath returns the path to a Claude Code session JSONL file.
func SessionJSONLPath(sessionID, cwd string) string {
	home, _ := os.UserHomeDir()
	encoded := EncodeCWD(cwd)
	return filepath.Join(home, ".claude", "projects", encoded, sessionID+".jsonl")
}

// FindLatestSession scans the Claude project dir for the most recent valid session JSONL.
func FindLatestSession(cwd string) string {
	home, _ := os.UserHomeDir()
	encoded := EncodeCWD(cwd)
	dir := filepath.Join(home, ".claude", "projects", encoded)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	var bestID string
	var bestTime time.Time

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
			// Verify it has real conversation data
			if SessionIsValid(id, cwd) {
				bestTime = info.ModTime()
				bestID = id
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
