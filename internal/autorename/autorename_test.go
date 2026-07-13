package autorename

import (
	"testing"

	"jarvis/internal/model"
)

func TestFindCandidates(t *testing.T) {
	sessions := []*model.Session{
		{ID: "a", Name: "(untitled chat)", Status: model.StatusActive, ClaudeSessionID: "csid-a"},
		{ID: "b", Name: "(untitled chat)", Status: model.StatusSuspended, ClaudeSessionID: "csid-b"},
		{ID: "c", Name: "Real Name", Status: model.StatusActive, ClaudeSessionID: "csid-c"},         // named
		{ID: "d", Name: "(untitled chat)", Status: model.StatusDone, ClaudeSessionID: "csid-d"},     // done
		{ID: "e", Name: "(untitled chat)", Status: model.StatusArchived, ClaudeSessionID: "csid-e"}, // archived
		{ID: "f", Name: "(untitled chat)", Status: model.StatusActive, ClaudeSessionID: ""},         // no claude session yet
	}

	got := FindCandidates(sessions)

	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	want := []string{"a", "b"}
	if len(ids) != len(want) {
		t.Fatalf("candidates = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", ids, want)
		}
	}
}
