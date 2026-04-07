// internal/watch/registry.go
package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"jarvis/internal/store"

	"gopkg.in/yaml.v3"
)

// Registry maps external context keys (e.g. "slack:C123/p456") to session IDs.
// It persists to ~/.jarvis/context_registry.yaml for recovery across restarts.
type Registry struct {
	mu         sync.Mutex
	name       string            // watcher name (e.g. "slack", "github", "pagerduty")
	entries    map[string]string // context key → session ID
	lastPollTS string            // persisted timestamp of newest seen message
}

func NewRegistry(watcherName string) *Registry {
	return &Registry{name: watcherName, entries: make(map[string]string)}
}

// registryPath returns the path for a watcher-specific registry file.
// Each watcher type (slack, github, pagerduty) gets its own directory.
func registryPath(watcherName string) string {
	return filepath.Join(store.JarvisHome(), "watch", watcherName, "registry.yaml")
}

func (r *Registry) Register(contextKey, sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[contextKey] = sessionID
}

func (r *Registry) Lookup(contextKey string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.entries[contextKey]
	return id, ok
}

func (r *Registry) Unregister(contextKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, contextKey)
}

type registryFile struct {
	Contexts   map[string]string `yaml:"contexts"`
	LastPollTS string            `yaml:"last_poll_ts,omitempty"` // Slack timestamp of newest seen message
}

func (r *Registry) SetLastPollTS(ts string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastPollTS = ts
}

func (r *Registry) GetLastPollTS() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastPollTS
}

func (r *Registry) Save() error {
	r.mu.Lock()
	data, err := yaml.Marshal(registryFile{Contexts: r.entries, LastPollTS: r.lastPollTS})
	r.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}
	return store.WriteAtomic(registryPath(r.name), data)
}

func (r *Registry) Load() error {
	data, err := os.ReadFile(registryPath(r.name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read registry: %w", err)
	}
	var f registryFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("unmarshal registry: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if f.Contexts != nil {
		r.entries = f.Contexts
	}
	r.lastPollTS = f.LastPollTS
	return nil
}
