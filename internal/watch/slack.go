package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"jarvis/internal/config"
)

// SlackEvent represents an actionable Slack message.
type SlackEvent struct {
	ChannelID   string
	ChannelName string
	ThreadTS    string // thread parent timestamp (empty if top-level)
	MessageTS   string // this message's timestamp
	Text        string
	SenderID    string
	SenderName  string
	IsDM        bool
	Timestamp   time.Time
}

// ContextKey returns a unique key for deduplication in the context registry.
func (e SlackEvent) ContextKey() string {
	ts := e.ThreadTS
	if ts == "" {
		ts = e.MessageTS
	}
	return fmt.Sprintf("slack:%s/%s", e.ChannelID, ts)
}

// SessionName returns a human-readable session name for the dashboard.
func (e SlackEvent) SessionName() string {
	if e.IsDM {
		return fmt.Sprintf("slack: DM from %s", e.SenderName)
	}
	return fmt.Sprintf("slack: %s in %s", e.SenderName, e.ChannelName)
}

// slackPromptTemplate is the system prompt appended to Claude Code sessions
// created by the Slack watcher. It instructs Claude to investigate and draft
// a response without taking any external actions.
const slackPromptTemplate = `You received a Slack message that needs your attention.

**From:** %s
**Time:** %s
**Message:**
> %s

Your job:
1. Analyze the message and understand what is being asked
2. Investigate if needed (read code, check logs, etc.)
3. Prepare a draft response

**IMPORTANT:** Do NOT send any Slack messages, post any comments, or take any external-facing actions. Only investigate and prepare a draft.
`

// SystemPrompt builds the instruction for the Claude Code session.
func (e SlackEvent) SystemPrompt() string {
	from := e.SenderName
	if e.IsDM {
		from += " (DM)"
	} else {
		from += " in " + e.ChannelName
	}
	return fmt.Sprintf(slackPromptTemplate, from, e.Timestamp.Format(time.RFC3339), e.Text)
}

// SlackPoller polls Slack via a local MCP server for new messages.
// It uses search.messages which returns raw JSON (unlike conversations.history
// which is privacy-summarized by the MCP server).
type SlackPoller struct {
	mcpCmd  string   // command to launch MCP server
	mcpArgs []string // args for MCP server
	userID  string
	lastTS  string     // last seen mention timestamp
	client  *MCPClient // lazily initialized
}

// NewSlackPoller creates a poller that uses the local Slack MCP server.
// It initializes lastTS to "now" so only messages arriving after the watcher
// starts are picked up (avoids processing the entire history on first run).
func NewSlackPoller(cfg config.SlackWatcherConfig) *SlackPoller {
	parts := strings.Fields(cfg.MCPServerCmd)
	cmd := parts[0]
	args := parts[1:]
	// Set lastTS to current time so we only see new messages from now on.
	now := fmt.Sprintf("%d.000000", time.Now().Unix())
	return &SlackPoller{
		mcpCmd:  cmd,
		mcpArgs: args,
		userID:  cfg.UserID,
		lastTS:  now,
	}
}

// ensureClient starts the MCP server process if not already running.
func (p *SlackPoller) ensureClient(ctx context.Context) error {
	if p.client != nil {
		return nil
	}
	client, err := NewMCPClient(ctx, p.mcpCmd, p.mcpArgs...)
	if err != nil {
		return fmt.Errorf("start MCP server: %w", err)
	}
	p.client = client
	return nil
}

// slackAPICall calls a Slack API endpoint via the MCP server.
func (p *SlackPoller) slackAPICall(endpoint string, params map[string]interface{}) (string, error) {
	args := map[string]interface{}{
		"endpoint":  endpoint,
		"params":    params,
		"raw":       true,
		"use_cache": false,
	}
	return p.client.CallTool("slack_read_api_call", args)
}

// Poll checks for new messages mentioning the user via search.messages.
func (p *SlackPoller) Poll(ctx context.Context) ([]SlackEvent, error) {
	if err := p.ensureClient(ctx); err != nil {
		return nil, err
	}

	query := fmt.Sprintf("<@%s>", p.userID)
	result, err := p.slackAPICall("search.messages", map[string]interface{}{
		"query":    query,
		"count":    20,
		"sort":     "timestamp",
		"sort_dir": "desc",
	})
	if err != nil {
		return nil, fmt.Errorf("search mentions: %w", err)
	}

	var searchResult struct {
		OK       bool `json:"ok"`
		Messages struct {
			Matches []struct {
				Channel struct {
					ID   string `json:"id"`
					Name string `json:"name"`
					IsIM bool   `json:"is_im"`
				} `json:"channel"`
				User      string `json:"user"`
				Username  string `json:"username"`
				Text      string `json:"text"`
				Timestamp string `json:"ts"`
				ThreadTS  string `json:"thread_ts"`
			} `json:"matches"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(result), &searchResult); err != nil {
		return nil, fmt.Errorf("parse search results: %w", err)
	}
	if !searchResult.OK {
		return nil, fmt.Errorf("search.messages returned ok=false")
	}

	var events []SlackEvent
	for _, match := range searchResult.Messages.Matches {
		// Skip own messages
		if match.User == p.userID {
			continue
		}
		// Skip messages we've already seen
		if match.Timestamp <= p.lastTS {
			continue
		}

		isDM := match.Channel.IsIM || strings.HasPrefix(match.Channel.ID, "D")
		channelName := "#" + match.Channel.Name
		if isDM {
			channelName = ""
		}

		events = append(events, SlackEvent{
			ChannelID:   match.Channel.ID,
			ChannelName: channelName,
			MessageTS:   match.Timestamp,
			ThreadTS:    match.ThreadTS,
			Text:        match.Text,
			SenderID:    match.User,
			SenderName:  match.Username,
			IsDM:        isDM,
			Timestamp:   parseSlackTS(match.Timestamp),
		})
	}

	// Update last seen to newest message
	if len(searchResult.Messages.Matches) > 0 {
		newest := searchResult.Messages.Matches[0].Timestamp
		for _, m := range searchResult.Messages.Matches {
			if m.Timestamp > newest {
				newest = m.Timestamp
			}
		}
		if newest > p.lastTS {
			p.lastTS = newest
		}
	}

	log.Printf("slack: poll found %d new events", len(events))
	return events, nil
}

// Close shuts down the MCP server process.
func (p *SlackPoller) Close() {
	if p.client != nil {
		p.client.Close()
		p.client = nil
	}
}

func parseSlackTS(ts string) time.Time {
	parts := strings.SplitN(ts, ".", 2)
	if len(parts) == 0 {
		return time.Time{}
	}
	var sec int64
	for _, c := range parts[0] {
		sec = sec*10 + int64(c-'0')
	}
	return time.Unix(sec, 0)
}
