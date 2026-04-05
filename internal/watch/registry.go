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
	mu      sync.Mutex
	entries map[string]string // context key → session ID
}

func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]string)}
}

func registryPath() string {
	return filepath.Join(store.JarvisHome(), "context_registry.yaml")
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

type registryFile struct {
	Contexts map[string]string `yaml:"contexts"`
}

func (r *Registry) Save() error {
	r.mu.Lock()
	data, err := yaml.Marshal(registryFile{Contexts: r.entries})
	r.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}
	return store.WriteAtomic(registryPath(), data)
}

func (r *Registry) Load() error {
	data, err := os.ReadFile(registryPath())
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
	return nil
}
