// internal/watch/registry_test.go
package watch

import (
	"os"
	"testing"
)

func TestRegistryRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	reg := NewRegistry("slack")

	reg.Register("slack:C123/p456", "sess-abc")

	sessID, found := reg.Lookup("slack:C123/p456")
	if !found {
		t.Fatal("should find registered context")
	}
	if sessID != "sess-abc" {
		t.Errorf("session ID: got %q, want %q", sessID, "sess-abc")
	}

	if err := reg.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	reg2 := NewRegistry("slack")
	if err := reg2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	sessID2, found2 := reg2.Lookup("slack:C123/p456")
	if !found2 {
		t.Fatal("should find after reload")
	}
	if sessID2 != "sess-abc" {
		t.Errorf("after reload: got %q", sessID2)
	}
}

func TestRegistryLookupMiss(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	reg := NewRegistry("slack")
	_, found := reg.Lookup("slack:nonexistent")
	if found {
		t.Error("should not find unregistered context")
	}
}

func TestRegistryLoadEmpty(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	reg := NewRegistry("slack")
	if err := reg.Load(); err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if len(reg.entries) != 0 {
		t.Errorf("should be empty, got %d entries", len(reg.entries))
	}
}

func TestRegistryCommentTimestamps(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	r := NewRegistry("github")

	// Set and get comment timestamps
	r.SetCommentTS("github:databricks-eng/universe#1234", "2026-04-12T01:00:00Z")
	r.SetCommentTS("github:databricks-eng/universe#5678", "2026-04-12T02:00:00Z")

	ts := r.GetCommentTS("github:databricks-eng/universe#1234")
	if ts != "2026-04-12T01:00:00Z" {
		t.Errorf("comment ts: got %q", ts)
	}

	// Save and reload
	if err := r.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	r2 := NewRegistry("github")
	if err := r2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	ts2 := r2.GetCommentTS("github:databricks-eng/universe#1234")
	if ts2 != "2026-04-12T01:00:00Z" {
		t.Errorf("loaded comment ts: got %q", ts2)
	}

	// Non-existent key returns empty
	ts3 := r2.GetCommentTS("github:databricks-eng/universe#9999")
	if ts3 != "" {
		t.Errorf("missing key should return empty, got %q", ts3)
	}
}
