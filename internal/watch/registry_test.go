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

	reg := NewRegistry()

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

	reg2 := NewRegistry()
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

	reg := NewRegistry()
	_, found := reg.Lookup("slack:nonexistent")
	if found {
		t.Error("should not find unregistered context")
	}
}

func TestRegistryLoadEmpty(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	reg := NewRegistry()
	if err := reg.Load(); err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if len(reg.entries) != 0 {
		t.Errorf("should be empty, got %d entries", len(reg.entries))
	}
}
