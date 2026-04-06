package watch

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestMCPClientLiveSlack tests the MCP client against the real Slack MCP server.
// Skip if the MCP server pex is not available.
func TestMCPClientLiveSlack(t *testing.T) {
	pexPath := "/home/jianyu.zhou/mcp/servers/slack_mcp/slack_mcp_deploy.pex"
	if _, err := os.Stat(pexPath); err != nil {
		t.Skipf("Slack MCP server not found at %s, skipping live test", pexPath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := NewMCPClient(ctx, "python3.10", pexPath)
	if err != nil {
		t.Fatalf("NewMCPClient: %v", err)
	}
	defer client.Close()

	// Test: search for recent mentions of Jianyu
	result, err := client.CallTool("slack_read_api_call", map[string]interface{}{
		"endpoint": "search.messages",
		"params": map[string]interface{}{
			"query":    "<@U050RJFF7T3>",
			"count":    3,
			"sort":     "timestamp",
			"sort_dir": "desc",
		},
		"raw":       true,
		"use_cache": false,
	})
	if err != nil {
		t.Fatalf("CallTool search.messages: %v", err)
	}

	// Verify we got valid JSON back
	var searchResult struct {
		OK       bool `json:"ok"`
		Messages struct {
			Total   int `json:"total"`
			Matches []struct {
				Text    string `json:"text"`
				Channel struct {
					Name string `json:"name"`
				} `json:"channel"`
				Username string `json:"username"`
			} `json:"matches"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(result), &searchResult); err != nil {
		t.Fatalf("parse search result: %v\nraw: %s", err, result[:min(len(result), 500)])
	}

	if !searchResult.OK {
		t.Fatal("search.messages returned ok=false")
	}
	t.Logf("Found %d total mentions, got %d matches", searchResult.Messages.Total, len(searchResult.Messages.Matches))
	for i, m := range searchResult.Messages.Matches {
		t.Logf("  [%d] @%s in #%s: %s", i, m.Username, m.Channel.Name, truncate(m.Text, 80))
	}
}

// TestMCPClientLiveDMs tests fetching DM channels.
func TestMCPClientLiveDMs(t *testing.T) {
	pexPath := "/home/jianyu.zhou/mcp/servers/slack_mcp/slack_mcp_deploy.pex"
	if _, err := os.Stat(pexPath); err != nil {
		t.Skipf("Slack MCP server not found at %s, skipping live test", pexPath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := NewMCPClient(ctx, "python3.10", pexPath)
	if err != nil {
		t.Fatalf("NewMCPClient: %v", err)
	}
	defer client.Close()

	result, err := client.CallTool("slack_read_api_call", map[string]interface{}{
		"endpoint": "conversations.list",
		"params": map[string]interface{}{
			"types":            "im",
			"limit":            5,
			"exclude_archived": true,
		},
		"raw":       true,
		"use_cache": false,
	})
	if err != nil {
		t.Fatalf("CallTool conversations.list: %v", err)
	}

	var convList struct {
		OK       bool `json:"ok"`
		Channels []struct {
			ID string `json:"id"`
		} `json:"channels"`
	}
	if err := json.Unmarshal([]byte(result), &convList); err != nil {
		t.Fatalf("parse conversations.list: %v", err)
	}

	if !convList.OK {
		t.Fatal("conversations.list returned ok=false")
	}
	t.Logf("Found %d DM channels", len(convList.Channels))
	for _, ch := range convList.Channels {
		t.Logf("  DM channel: %s", ch.ID)
	}
}

// TestSlackPollerLive tests the full poller flow against live Slack.
func TestSlackPollerLive(t *testing.T) {
	pexPath := "/home/jianyu.zhou/mcp/servers/slack_mcp/slack_mcp_deploy.pex"
	if _, err := os.Stat(pexPath); err != nil {
		t.Skipf("Slack MCP server not found at %s, skipping live test", pexPath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	poller := &SlackPoller{
		mcpCmd:  "python3.10",
		mcpArgs: []string{pexPath},
		userID:  "U050RJFF7T3",
	}
	defer poller.Close()

	events, err := poller.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}

	t.Logf("Poll returned %d events", len(events))
	for i, ev := range events {
		t.Logf("  [%d] %s: %s — %s", i, ev.ContextKey(), ev.SessionName(), truncate(ev.Text, 60))
	}

	// Second poll should return fewer events (lastTS updated)
	events2, err := poller.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll 2: %v", err)
	}
	t.Logf("Second poll returned %d events (should be fewer or same)", len(events2))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
