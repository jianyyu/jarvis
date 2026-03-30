package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"jarvis/internal/store"

	"gopkg.in/yaml.v3"
)

type Config struct {
	WorktreeBaseDir string `yaml:"worktree_base_dir,omitempty"`
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
