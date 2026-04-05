package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"jarvis/internal/store"

	"gopkg.in/yaml.v3"
)

// ApprovalAction is what to do when an approval prompt matches a policy rule.
type ApprovalAction string

const (
	ActionApprove  ApprovalAction = "approve"
	ActionAskHuman ApprovalAction = "ask_human"
)

// ApprovalRule is a single auto-approve policy entry.
//
// Example config:
//
//	policies:
//	  auto_approve:
//	    - tool: [Read, Grep, Glob]
//	      action: approve
//	    - tool: [Bash]
//	      command_matches: "rm|drop|force"
//	      action: ask_human
type ApprovalRule struct {
	// Tool matches tool names from Claude Code approval prompts.
	// Can be a single string or a list: "Bash" or ["Read", "Grep", "Glob"].
	Tool ToolMatch `yaml:"tool"`
	// CommandMatches is an optional regex applied to the command/detail text.
	// Only relevant for tools like Bash where the command matters.
	CommandMatches string `yaml:"command_matches,omitempty"`
	// Action is what to do: "approve" or "ask_human".
	Action ApprovalAction `yaml:"action"`
}

// ToolMatch handles YAML that can be either a string or a list of strings.
type ToolMatch []string

func (t *ToolMatch) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// Try single string first.
	var single string
	if err := unmarshal(&single); err == nil {
		*t = []string{single}
		return nil
	}
	// Try list of strings.
	var list []string
	if err := unmarshal(&list); err != nil {
		return err
	}
	*t = list
	return nil
}

// Policies holds all policy configuration.
type Policies struct {
	AutoApprove []ApprovalRule `yaml:"auto_approve,omitempty"`
}

type Config struct {
	WorktreeBaseDir string   `yaml:"worktree_base_dir,omitempty"`
	Policies        Policies `yaml:"policies,omitempty"`
	repoPath        string
}

func Load() (*Config, error) {
	path := filepath.Join(store.JarvisHome(), "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) RepoPath() string {
	if c.repoPath != "" {
		return c.repoPath
	}
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	c.repoPath = strings.TrimSpace(string(out))
	return c.repoPath
}

func (c *Config) EffectiveWorktreeBaseDir() string {
	if c.WorktreeBaseDir != "" {
		return c.WorktreeBaseDir
	}
	repo := c.RepoPath()
	if repo != "" {
		return filepath.Join(repo, ".claude", "worktrees")
	}
	return ""
}
