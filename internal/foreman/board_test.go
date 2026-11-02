package foreman

import (
	"strings"
	"testing"
)

func TestBoardSnapshotSection(t *testing.T) {
	room := "room1"
	tasks := []Task{
		{RoomID: room, Title: "Fix login 500", Status: "in_progress", AgentName: "Dev"},
		{RoomID: room, Title: "Review API patch", Status: "review", AgentName: "QA", Severity: "warning"},
		{RoomID: room, Title: "Hotfix config", Status: "todo", Severity: "blocker", AgentName: "Lead"},
		// excluded: other room, done, hidden
		{RoomID: "other", Title: "Elsewhere", Status: "todo", AgentName: "Dev"},
		{RoomID: room, Title: "Completed work", Status: "done", AgentName: "Dev"},
		{RoomID: room, Title: "Hidden chat run", Status: "todo", AgentName: "Lead", Hidden: true},
	}
	got := boardSnapshotSection(room, tasks)
	if got == "" {
		t.Fatal("expected a non-empty board section")
	}
	for _, want := range []string{
		"## Team board",
		"- [in_progress] Fix login 500 — @Dev",
		"- [review] 🟡 Review API patch — @QA",
		"- [todo] 🔴 Hotfix config — @Lead",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("board section missing %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{"Elsewhere", "Completed work", "Hidden chat run"} {
		if strings.Contains(got, banned) {
			t.Errorf("board section leaked %q:\n%s", banned, got)
		}
	}
	// Unassigned cards show explicitly.
	tasks = append(tasks, Task{RoomID: room, Title: "Nobody owns this", Status: "todo"})
	if !strings.Contains(boardSnapshotSection(room, tasks), "@(unassigned)") {
		t.Errorf("unassigned card must render")
	}
}

func TestBoardSnapshotTruncationAndEmpty(t *testing.T) {
	if got := boardSnapshotSection("room1", nil); got != "" {
		t.Errorf("empty board must be omitted, got %q", got)
	}
	room := "room1"
	var tasks []Task
	for i := 0; i < 20; i++ {
		tasks = append(tasks, Task{RoomID: room, Title: strings.Repeat("t", 3), Status: "todo"})
	}
	got := boardSnapshotSection(room, tasks)
	if n := strings.Count(got, "- [todo]"); n != 12 {
		t.Errorf("board cap = %d entries, want 12", n)
	}
	if !strings.Contains(got, "list_tasks") {
		t.Errorf("truncated board must point to list_tasks")
	}
}
