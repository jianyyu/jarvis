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
	ChannelID     string
	ChannelName   string
	ThreadTS      string // thread parent timestamp (empty if top-level)
	MessageTS     string // this message's timestamp
	Text          string
	SenderID      string
	SenderName    string
	IsDM          bool
	Timestamp     time.Time
	Permalink     string // Slack message link
	ThreadContext string // full thread conversation for context (populated by FetchThreadContext)
}

// ContextKey returns a unique key for deduplication in the context registry.
// For DMs, the key is just the channel ID (one session per DM conversation).
// For channel messages, the key includes the thread timestamp.
func (e SlackEvent) ContextKey() string {
	if e.IsDM {
		return fmt.Sprintf("slack:%s", e.ChannelID)
	}
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

// InitialPrompt returns the user message that kicks off the Claude session.
func (e SlackEvent) InitialPrompt() string {
	return fmt.Sprintf("Please look at this Slack message and investigate whatever is necessary: %s\n\nDo NOT send any Slack messages, post any comments, or take any external-facing actions. Only investigate and prepare a draft response.", e.Permalink)
}

// SlackPoller polls Slack via a local MCP server for new messages.
// It uses search.messages which returns raw JSON (unlike conversations.history
// which is privacy-summarized by the MCP server).
type SlackPoller struct {
	mcpCmd   string   // command to launch MCP server
	mcpArgs  []string // args for MCP server
	userID   string
	keywords []string   // additional search queries
	lastTS   string     // last seen mention timestamp
	client   *MCPClient // lazily initialized
}

// NewSlackPoller creates a poller that uses the local Slack MCP server.
// lastTS should be provided from the persisted registry; if empty, defaults
// to "now" so only messages arriving after the watcher starts are picked up.
func NewSlackPoller(cfg config.SlackWatcherConfig, lastTS string) *SlackPoller {
	parts := strings.Fields(cfg.MCPServerCmd)
	cmd := parts[0]
	args := parts[1:]
	if lastTS == "" {
		lastTS = fmt.Sprintf("%d.000000", time.Now().Unix())
	}
	return &SlackPoller{
		mcpCmd:   cmd,
		mcpArgs:  args,
		userID:   cfg.UserID,
		keywords: cfg.Keywords,
		lastTS:   lastTS,
	}
}

// LastTS returns the timestamp of the newest message seen.
func (p *SlackPoller) LastTS() string {
	return p.lastTS
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

// Poll checks for new messages via multiple search queries:
// 1. @mentions of the user
// 2. DMs sent to the user
// 3. Each configured keyword (e.g. "in:#channel-name")
// Results are deduped by message timestamp.
func (p *SlackPoller) Poll(ctx context.Context) ([]SlackEvent, error) {
	if err := p.ensureClient(ctx); err != nil {
		return nil, err
	}

	// Build list of queries: mentions, DMs, then keywords.
	queries := []string{
		fmt.Sprintf("<@%s>", p.userID),
		fmt.Sprintf("to:<@%s>", p.userID),
	}
	queries = append(queries, p.keywords...)

	seen := make(map[string]bool) // dedup by message ts
	var events []SlackEvent

	for _, query := range queries {
		matches, err := p.searchMessages(query)
		if err != nil {
			log.Printf("slack: search %q error: %v", query, err)
			continue
		}

		for _, ev := range matches {
			if seen[ev.MessageTS] {
				continue
			}
			seen[ev.MessageTS] = true
			events = append(events, ev)
		}
	}

	log.Printf("slack: poll found %d new events (%d queries)", len(events), len(queries))
	return events, nil
}

// searchMessages runs a single search.messages query and returns new events.
func (p *SlackPoller) searchMessages(query string) ([]SlackEvent, error) {
	result, err := p.slackAPICall("search.messages", map[string]interface{}{
		"query":    query,
		"count":    20,
		"sort":     "timestamp",
		"sort_dir": "desc",
	})
	if err != nil {
		return nil, fmt.Errorf("search %q: %w", query, err)
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
				Permalink string `json:"permalink"`
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
		if match.User == p.userID {
			continue
		}
		if match.Timestamp <= p.lastTS {
			continue
		}

		isDM := match.Channel.IsIM || strings.HasPrefix(match.Channel.ID, "D")
		channelName := "#" + match.Channel.Name
		if isDM {
			channelName = ""
		}

		senderName := match.Username
		if senderName == "" && match.User != "" {
			senderName = p.resolveUserName(match.User)
		}

		events = append(events, SlackEvent{
			ChannelID:   match.Channel.ID,
			ChannelName: channelName,
			MessageTS:   match.Timestamp,
			ThreadTS:    match.ThreadTS,
			Text:        match.Text,
			SenderID:    match.User,
			SenderName:  senderName,
			IsDM:        isDM,
			Timestamp:   parseSlackTS(match.Timestamp),
			Permalink:   match.Permalink,
		})
	}

	// Update last seen to newest message
	for _, ev := range events {
		if ev.MessageTS > p.lastTS {
			p.lastTS = ev.MessageTS
		}
	}

	return events, nil
}

// resolveUserName looks up a user's display name by ID.
func (p *SlackPoller) resolveUserName(userID string) string {
	result, err := p.slackAPICall("users.info", map[string]interface{}{
		"user": userID,
	})
	if err != nil {
		return userID
	}
	var userInfo struct {
		OK   bool `json:"ok"`
		User struct {
			RealName string `json:"real_name"`
			Profile  struct {
				DisplayName string `json:"display_name"`
			} `json:"profile"`
		} `json:"user"`
	}
	if err := json.Unmarshal([]byte(result), &userInfo); err != nil {
		return userID
	}
	if userInfo.User.Profile.DisplayName != "" {
		return userInfo.User.Profile.DisplayName
	}
	if userInfo.User.RealName != "" {
		return userInfo.User.RealName
	}
	return userID
}

// FetchThreadContext fetches the full thread for an event (if it's in a thread).
func (p *SlackPoller) FetchThreadContext(ev *SlackEvent) {
	ts := ev.ThreadTS
	if ts == "" {
		ts = ev.MessageTS
	}
	if ts == "" || ev.ChannelID == "" {
		return
	}

	result, err := p.slackAPICall("conversations.replies", map[string]interface{}{
		"channel": ev.ChannelID,
		"ts":      ts,
		"limit":   20,
	})
	if err != nil {
		log.Printf("slack: fetch thread %s/%s: %v", ev.ChannelID, ts, err)
		return
	}

	// conversations.replies may be privacy-summarized (returns markdown).
	// If it's valid JSON with messages, format them. Otherwise use the raw text.
	var threadResp struct {
		OK       bool `json:"ok"`
		Messages []struct {
			User string `json:"user"`
			Text string `json:"text"`
			TS   string `json:"ts"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(result), &threadResp); err == nil && threadResp.OK && len(threadResp.Messages) > 1 {
		var lines []string
		for _, msg := range threadResp.Messages {
			if msg.TS == ev.MessageTS {
				continue // skip the triggering message itself
			}
			lines = append(lines, fmt.Sprintf("- %s: %s", msg.User, msg.Text))
		}
		if len(lines) > 0 {
			ev.ThreadContext = strings.Join(lines, "\n")
		}
		return
	}

	// Privacy-summarized response — use the raw text as-is
	if len(result) > 0 && !strings.HasPrefix(result, "{") {
		ev.ThreadContext = result
	}
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
