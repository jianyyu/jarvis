package searchindex

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
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
	IsMeta  bool   `json:"isMeta"` // injected skill/command expansion, not the human typing
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
	maxColumnRunes        = 200_000          // per-column safety cap
	maxLineBytes          = 16 * 1024 * 1024 // lines beyond this are skipped as noise
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
	"<task-notification>", // background-subagent completion notices
	"[Request interrupted",
	"Caveat:",
	"This session is being continued",
}

// ParseTranscript streams a Claude Code JSONL transcript and returns its
// denoised searchable buckets. Malformed and oversized lines are skipped.
func ParseTranscript(r io.Reader) (ParsedSession, error) {
	var ps ParsedSession
	var users, assistants []string

	// ReadSlice (not ReadBytes) so an oversized line is drained chunk by
	// chunk without ever buffering more than maxLineBytes: a pathological
	// multi-GB line costs bounded memory. Oversized lines are skippable
	// noise, same as malformed ones — one bad line must not lose the session.
	br := bufio.NewReaderSize(r, 64*1024)
	lineBuf := make([]byte, 0, 64*1024)
	lineLen := 0 // cumulative bytes of the current line, including discarded chunks
	for {
		chunk, err := br.ReadSlice('\n')
		lineLen += len(chunk)
		if lineLen <= maxLineBytes {
			lineBuf = append(lineBuf, chunk...)
		} else {
			lineBuf = lineBuf[:0] // over the cap: drop what we buffered, keep draining
		}
		if err == bufio.ErrBufferFull {
			continue // line continues in the next slice
		}
		// Delimiter found (err == nil), final unterminated line (io.EOF), or
		// a real read error — the current line, if kept, is complete.
		if len(lineBuf) > 0 {
			parseLine(lineBuf, &ps, &users, &assistants)
		}
		lineBuf = lineBuf[:0]
		lineLen = 0
		if err == io.EOF {
			break
		}
		if err != nil {
			return ps, err
		}
	}

	ps.UserText = truncateRunes(strings.Join(users, "\n"), maxColumnRunes)
	ps.AssistantText = truncateRunes(strings.Join(assistants, "\n"), maxColumnRunes)
	return ps, nil
}

// parseLine dispatches a single JSONL line into the output buckets.
// Malformed lines are ignored.
func parseLine(line []byte, ps *ParsedSession, users, assistants *[]string) {
	var rec transcriptRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return // skip malformed / partial line
	}

	switch rec.Type {
	case "ai-title":
		if rec.AITitle != "" {
			ps.AITitle = rec.AITitle
		}
	case "user":
		text := realUserText(rec)
		if text == "" {
			return
		}
		if ps.InitialPrompt == "" {
			ps.InitialPrompt = truncateRunes(text, maxInitialPromptRunes)
		}
		*users = append(*users, text)
	case "assistant":
		if rec.Message == nil {
			return
		}
		for _, blk := range textBlocks(rec.Message.Content) {
			if isToolNoise(blk) {
				continue
			}
			*assistants = append(*assistants, truncateReply(blk))
		}
	}
}

// realUserText returns the human-typed text of a user record, or "" if the
// record is synthetic. isMeta records are injected skill/command expansions
// (real typed prompts never carry it). String content is the prompt itself;
// array content is either a multimodal prompt (image + text blocks — keep the
// text) or a tool_result carrier (no text blocks — synthetic).
func realUserText(rec transcriptRecord) string {
	if rec.IsMeta || rec.Message == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(rec.Message.Content, &s); err != nil {
		s = strings.Join(textBlocks(rec.Message.Content), "\n")
	}
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) < 5 {
		return ""
	}
	if isSyntheticUserText(s) {
		return ""
	}
	return s
}

// textBlocks returns the text of every `type:"text"` block in an array
// content payload (dropping thinking / tool_use / tool_result / image).
func textBlocks(content json.RawMessage) []string {
	var blocks []contentBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
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

// isSyntheticUserText expects already-trimmed input.
func isSyntheticUserText(s string) bool {
	for _, p := range syntheticUserPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func isToolNoise(s string) bool {
	t := strings.TrimSpace(s)
	if utf8.RuneCountInString(t) < toolNoiseMinLen {
		return true
	}
	return strings.Contains(t, "[Tool:")
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
