package watch

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"jarvis/internal/config"

	"github.com/slack-go/slack"
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

// SlackPoller polls the Slack API for new messages directed at the user.
type SlackPoller struct {
	client *slack.Client
	userID string
	lastTS map[string]string // channel ID → last seen message timestamp
}

// NewSlackPoller creates a poller from watcher config.
func NewSlackPoller(cfg config.SlackWatcherConfig) *SlackPoller {
	return &SlackPoller{
		client: slack.New(cfg.Token),
		userID: cfg.UserID,
		lastTS: make(map[string]string),
	}
}

// Poll checks for new DMs and mentions since last poll. Returns actionable events.
func (p *SlackPoller) Poll(ctx context.Context) ([]SlackEvent, error) {
	var events []SlackEvent

	dmEvents, err := p.pollDMs(ctx)
	if err != nil {
		log.Printf("slack: DM poll error: %v", err)
	} else {
		events = append(events, dmEvents...)
	}

	mentionEvents, err := p.pollMentions(ctx)
	if err != nil {
		log.Printf("slack: mention poll error: %v", err)
	} else {
		events = append(events, mentionEvents...)
	}

	return events, nil
}

func (p *SlackPoller) pollDMs(ctx context.Context) ([]SlackEvent, error) {
	params := &slack.GetConversationsParameters{
		Types:           []string{"im"},
		Limit:           100,
		ExcludeArchived: true,
	}
	channels, _, err := p.client.GetConversationsContext(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list DM channels: %w", err)
	}

	var events []SlackEvent
	for _, ch := range channels {
		lastSeen := p.lastTS[ch.ID]
		histParams := &slack.GetConversationHistoryParameters{
			ChannelID: ch.ID,
			Limit:     10,
			Oldest:    lastSeen,
		}
		hist, err := p.client.GetConversationHistoryContext(ctx, histParams)
		if err != nil {
			log.Printf("slack: DM history %s: %v", ch.ID, err)
			continue
		}

		for _, msg := range hist.Messages {
			if msg.User == p.userID {
				continue
			}
			if msg.Timestamp <= lastSeen {
				continue
			}

			userName := p.resolveUserName(ctx, msg.User)
			events = append(events, SlackEvent{
				ChannelID:  ch.ID,
				MessageTS:  msg.Timestamp,
				ThreadTS:   msg.ThreadTimestamp,
				Text:       msg.Text,
				SenderID:   msg.User,
				SenderName: userName,
				IsDM:       true,
				Timestamp:  parseSlackTS(msg.Timestamp),
			})
		}

		if len(hist.Messages) > 0 {
			newest := hist.Messages[0].Timestamp
			for _, m := range hist.Messages {
				if m.Timestamp > newest {
					newest = m.Timestamp
				}
			}
			p.lastTS[ch.ID] = newest
		}
	}

	return events, nil
}

func (p *SlackPoller) pollMentions(ctx context.Context) ([]SlackEvent, error) {
	query := fmt.Sprintf("<@%s>", p.userID)
	params := slack.SearchParameters{
		Sort:          "timestamp",
		SortDirection: "desc",
		Count:         20,
	}
	results, err := p.client.SearchMessagesContext(ctx, query, params)
	if err != nil {
		return nil, fmt.Errorf("search mentions: %w", err)
	}

	var events []SlackEvent
	for _, match := range results.Matches {
		// Skip DM channels (channel IDs starting with "D")
		if strings.HasPrefix(match.Channel.ID, "D") {
			continue
		}
		if match.User == p.userID {
			continue
		}

		lastSeen := p.lastTS["mentions:"+match.Channel.ID]
		if match.Timestamp <= lastSeen {
			continue
		}

		// Use Previous context message timestamp as thread parent if available.
		threadTS := match.Previous.Timestamp

		events = append(events, SlackEvent{
			ChannelID:   match.Channel.ID,
			ChannelName: "#" + match.Channel.Name,
			MessageTS:   match.Timestamp,
			ThreadTS:    threadTS,
			Text:        match.Text,
			SenderID:    match.User,
			SenderName:  match.Username,
			IsDM:        false,
			Timestamp:   parseSlackTS(match.Timestamp),
		})

		if match.Timestamp > lastSeen {
			p.lastTS["mentions:"+match.Channel.ID] = match.Timestamp
		}
	}

	return events, nil
}

func (p *SlackPoller) resolveUserName(ctx context.Context, userID string) string {
	user, err := p.client.GetUserInfoContext(ctx, userID)
	if err != nil {
		return userID
	}
	if user.Profile.DisplayName != "" {
		return user.Profile.DisplayName
	}
	return user.RealName
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
