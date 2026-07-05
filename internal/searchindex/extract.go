package searchindex

import (
	"bufio"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

// ParsedSession is the denoised, searchable content extracted from one
// Claude Code transcript JSONL file.
type ParsedSession struct {
	AITitle       string // latest ai-title record
	InitialPrompt string // first real human prompt, truncated to 200 runes
	UserText      string // all real user messages, newline-joined
	AssistantText string // all assistant text replies, denoised + truncated
}

// transcriptRecord is the subset of a JSONL line we care about.
type transcriptRecord struct {
	Type    string `json:"type"`
	AITitle string `json:"aiTitle"`
	Message *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

const (
	maxInitialPromptRunes = 200
	replyTruncateAbove    = 800
	replyHeadRunes        = 500
	replyTailRunes        = 200
	toolNoiseMinLen       = 50
	maxColumnRunes        = 200_000 // per-column safety cap
)

// syntheticUserPrefixes mark `user`-role records that are not the human typing:
// system reminders, hook context, slash-command wrappers, command output, and
// interruption notices. Content that starts with any of these is excluded.
var syntheticUserPrefixes = []string{
	"<system-reminder>",
	"<local-command-stdout>",
	"<local-command-stderr>",
	"<command-name>",
	"<command-message>",
	"<user-prompt-submit-hook>",
	"[Request interrupted",
	"Caveat:",
	"This session is being continued",
}

// ParseTranscript streams a Claude Code JSONL transcript and returns its
// denoised searchable buckets. Malformed lines are skipped.
func ParseTranscript(r io.Reader) (ParsedSession, error) {
	var ps ParsedSession
	var users, assistants []string

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // transcript lines can be large

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec transcriptRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // skip malformed / partial line
		}

		switch rec.Type {
		case "ai-title":
			if rec.AITitle != "" {
				ps.AITitle = rec.AITitle
			}
		case "user":
			text := realUserText(rec)
			if text == "" {
				continue
			}
			if ps.InitialPrompt == "" {
				ps.InitialPrompt = truncateRunes(text, maxInitialPromptRunes)
			}
			users = append(users, text)
		case "assistant":
			for _, blk := range textBlocks(rec) {
				if isToolNoise(blk) {
					continue
				}
				assistants = append(assistants, truncateReply(blk))
			}
		}
	}
	if err := sc.Err(); err != nil {
		return ps, err
	}

	ps.UserText = capRunes(strings.Join(users, "\n"), maxColumnRunes)
	ps.AssistantText = capRunes(strings.Join(assistants, "\n"), maxColumnRunes)
	return ps, nil
}

// realUserText returns the human-typed text of a user record, or "" if the
// record is synthetic (array content = tool_result carrier, or a synthetic
// prefix).
func realUserText(rec transcriptRecord) string {
	if rec.Message == nil {
		return ""
	}
	// Real prompts have string content; tool_result carriers have array content.
	var s string
	if err := json.Unmarshal(rec.Message.Content, &s); err != nil {
		return "" // array content → synthetic
	}
	s = strings.TrimSpace(s)
	if len([]rune(s)) < 5 {
		return ""
	}
	if isSyntheticUserText(s) {
		return ""
	}
	return s
}

// textBlocks returns the text of every `type:"text"` block in an assistant
// record (dropping thinking / tool_use / image).
func textBlocks(rec transcriptRecord) []string {
	if rec.Message == nil {
		return nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(rec.Message.Content, &blocks); err != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			out = append(out, strings.TrimSpace(b.Text))
		}
	}
	return out
}

func isSyntheticUserText(s string) bool {
	t := strings.TrimSpace(s)
	for _, p := range syntheticUserPrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

var toolMarkerRe = regexp.MustCompile(`\[Tool:`)

func isToolNoise(s string) bool {
	t := strings.TrimSpace(s)
	if len([]rune(t)) < toolNoiseMinLen {
		return true
	}
	if toolMarkerRe.MatchString(t) {
		return true
	}
	return false
}

func truncateReply(s string) string {
	r := []rune(s)
	if len(r) <= replyTruncateAbove {
		return s
	}
	return string(r[:replyHeadRunes]) + "…" + string(r[len(r)-replyTailRunes:])
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func capRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
