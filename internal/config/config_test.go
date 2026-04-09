package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadWatcherConfig(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	configYAML := []byte(`watchers:
  slack:
    enabled: true
    mcp_server_cmd: "python3.10 /path/to/slack_mcp.pex"
    poll_interval: 30
    folder: "Slack"
    user_id: "U12345"
`)
	os.MkdirAll(tmp, 0o755)
	os.WriteFile(filepath.Join(tmp, "config.yaml"), configYAML, 0o644)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.Watchers.Slack.MCPServerCmd != "python3.10 /path/to/slack_mcp.pex" {
		t.Errorf("mcp_server_cmd: got %q", cfg.Watchers.Slack.MCPServerCmd)
	}
	if cfg.Watchers.Slack.PollInterval != 30 {
		t.Errorf("poll_interval: got %d", cfg.Watchers.Slack.PollInterval)
	}
	if cfg.Watchers.Slack.Folder != "Slack" {
		t.Errorf("folder: got %q", cfg.Watchers.Slack.Folder)
	}
	if cfg.Watchers.Slack.UserID != "U12345" {
		t.Errorf("user_id: got %q", cfg.Watchers.Slack.UserID)
	}
	if !cfg.Watchers.Slack.Enabled {
		t.Error("enabled should be true")
	}
}

func TestLoadConfigNoWatchers(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	configYAML := []byte(`policies:
  auto_approve: []
`)
	os.WriteFile(filepath.Join(tmp, "config.yaml"), configYAML, 0o644)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.Watchers.Slack.Enabled {
		t.Error("slack should not be enabled by default")
	}
	if cfg.Watchers.Slack.PollInterval != 0 {
		t.Errorf("poll_interval should be 0, got %d", cfg.Watchers.Slack.PollInterval)
	}
}
