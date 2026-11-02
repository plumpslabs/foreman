package foreman

import (
	"strings"
	"testing"
	"time"
)

func TestJournalSaveAndRecent(t *testing.T) {
	dir := t.TempDir()
	isolateGlobal(t)
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer st.Close()

	if _, err := st.SaveJournal("room-a", "alice", "auth.go", "GenerateToken", "switched JWT to RS256", ""); err != nil {
		t.Fatalf("SaveJournal: %v", err)
	}
	if _, err := st.SaveJournal("room-a", "alice", "db.go", "", "added busy_timeout pragma", "auth.go"); err != nil {
		t.Fatalf("SaveJournal: %v", err)
	}
	if _, err := st.SaveJournal("room-b", "bob", "ui.tsx", "renderRow", "fixed row key collision", ""); err != nil {
		t.Fatalf("SaveJournal: %v", err)
	}

	recent, err := st.RecentJournal("", 10)
	if err != nil {
		t.Fatalf("RecentJournal: %v", err)
	}
	if len(recent) != 3 {
		t.Fatalf("RecentJournal = %d entries, want 3 (%+v)", len(recent), recent)
	}
	// Newest first (workspace-wide, across rooms).
	if recent[0].File != "ui.tsx" {
		t.Fatalf("expected newest entry first (ui.tsx), got %+v", recent[0])
	}

	// Limit applies.
	top2, _ := st.RecentJournal("", 2)
	if len(top2) != 2 {
		t.Fatalf("RecentJournal(2) = %d entries, want 2", len(top2))
	}
}

func TestJournalDedupeCollapsesIdenticalFacts(t *testing.T) {
	dir := t.TempDir()
	isolateGlobal(t)
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer st.Close()

	a, err := st.SaveJournal("room-a", "alice", "auth.go", "GenerateToken", "switched JWT to RS256", "")
	if err != nil {
		t.Fatalf("SaveJournal: %v", err)
	}
	b, err := st.SaveJournal("room-a", "alice", "auth.go", "GenerateToken", "switched JWT to RS256", "")
	if err != nil {
		t.Fatalf("SaveJournal second: %v", err)
	}
	if a.ID != b.ID {
		t.Fatalf("expected identical facts to collapse onto one entry (%s vs %s)", a.ID, b.ID)
	}
	recent, _ := st.RecentJournal("", 10)
	if len(recent) != 1 {
		t.Fatalf("expected 1 entry after collapse, got %d (%+v)", len(recent), recent)
	}

	// A genuinely different action is a new entry.
	if _, err := st.SaveJournal("room-a", "alice", "auth.go", "GenerateToken", "added refresh token rotation", ""); err != nil {
		t.Fatalf("SaveJournal third: %v", err)
	}
	recent, _ = st.RecentJournal("", 10)
	if len(recent) != 2 {
		t.Fatalf("expected 2 entries after a different action, got %d", len(recent))
	}
}

func TestJournalRequiresAction(t *testing.T) {
	dir := t.TempDir()
	isolateGlobal(t)
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer st.Close()
	if _, err := st.SaveJournal("room-a", "alice", "auth.go", "GenerateToken", "   ", ""); err == nil {
		t.Fatal("expected an error for an empty action")
	}
}

func TestJournalConsolidated(t *testing.T) {
	dir := t.TempDir()
	isolateGlobal(t)
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer st.Close()

	must := func(e JournalEntry, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.SaveJournal("r", "a", "auth.go", "GenerateToken", "switched JWT to RS256", ""))
	must(st.SaveJournal("r", "a", "auth.go", "GenerateToken", "added refresh token rotation", ""))
	must(st.SaveJournal("r", "a", "db.go", "", "added busy_timeout pragma", ""))

	cons := st.JournalConsolidated(200, 900)
	if len(cons) != 2 {
		t.Fatalf("consolidated = %+v, want 2 lines (one per file)", cons)
	}
	// Newest file first, with a change count and the last action.
	joined := strings.Join(cons, "\n")
	if !strings.Contains(joined, "db.go: 1 change(s) — last: added busy_timeout pragma") {
		t.Fatalf("consolidated missing db.go line: %s", joined)
	}
	if !strings.Contains(joined, "auth.go: 2 change(s) — last: added refresh token rotation (GenerateToken)") {
		t.Fatalf("consolidated missing auth.go line: %s", joined)
	}

	// A fresh store has an empty journal → no consolidated lines.
	dir2 := t.TempDir()
	isolateGlobal(t)
	st2, err := OpenStore(dir2)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if got2 := st2.JournalConsolidated(200, 900); got2 != nil {
		t.Fatalf("expected nil consolidated on empty journal, got %+v", got2)
	}
}

func TestJournalCharCap(t *testing.T) {
	dir := t.TempDir()
	isolateGlobal(t)
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer st.Close()
	for i := 0; i < 5; i++ {
		f := "file" + string(rune('a'+i)) + ".go"
		if _, err := st.SaveJournal("r", "a", f, "", "a long action that keeps growing to make the cap meaningful here and beyond", ""); err != nil {
			t.Fatal(err)
		}
	}
	cons := st.JournalConsolidated(200, 200)
	if len(cons) == 0 {
		t.Fatal("expected at least one consolidated line")
	}
	if len(cons) >= 5 {
		t.Fatalf("expected the char budget to stop the digest early, got %d lines", len(cons))
	}
	total := 0
	for _, l := range cons {
		total += len(l)
	}
	if total > 200 {
		t.Fatalf("consolidated exceeds the character budget: %d chars (cap 200)", total)
	}
}

func TestJournalForRoomScoping(t *testing.T) {
	dir := t.TempDir()
	isolateGlobal(t)
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer st.Close()

	must := func(e JournalEntry, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Room A and B both touch auth.go; room A also touches db.go.
	must(st.SaveJournal("room-a", "alice", "auth.go", "GenerateToken", "switched JWT to RS256", ""))
	must(st.SaveJournal("room-a", "alice", "db.go", "", "added busy_timeout", ""))
	must(st.SaveJournal("room-b", "bob", "auth.go", "", "added refresh rotation", ""))

	// Workspace-wide sees all three.
	all, err := st.RecentJournal("", 50)
	if err != nil || len(all) != 3 {
		t.Fatalf("RecentJournal(\"\") = %d, want 3", len(all))
	}
	// Per-room is scoped.
	ra, _ := st.JournalForRoom("room-a", 50)
	if len(ra) != 2 {
		t.Fatalf("JournalForRoom(room-a) = %d, want 2 (%+v)", len(ra), ra)
	}
	rb, _ := st.JournalForRoom("room-b", 50)
	if len(rb) != 1 || rb[0].File != "auth.go" {
		t.Fatalf("JournalForRoom(room-b) = %+v, want exactly the room-b auth.go entry", rb)
	}
	// Consolidated is workspace-wide (shared cross-room context), as intended.
	cons := st.JournalConsolidated(200, 900)
	if len(cons) != 2 { // auth.go + db.go
		t.Fatalf("consolidated = %+v, want 2 files", cons)
	}
}

func TestJournalTTLPrune(t *testing.T) {
	dir := t.TempDir()
	isolateGlobal(t)
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer st.Close()

	must := func(e JournalEntry, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Two old entries on the same file, plus one recent.
	old1, _ := st.SaveJournal("room-a", "alice", "auth.go", "GenerateToken", "first attempt", "")
	old2, _ := st.SaveJournal("room-a", "alice", "auth.go", "GenerateToken", "second attempt", "")
	must(st.SaveJournal("room-a", "alice", "auth.go", "GenerateToken", "latest attempt", ""))

	// Backdate the first two via direct update (simulate age).
	st.db.mu.Lock()
	_, _ = st.db.db.Exec(`UPDATE journal SET created_at = ? WHERE id IN (?, ?)`,
		time.Now().UTC().Add(-200*24*time.Hour).Unix(), old1.ID, old2.ID)
	st.db.mu.Unlock()

	removed, err := st.JournalTTLPrune(90 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("JournalTTLPrune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 old row pruned (newest of the old pair is kept), got %d", removed)
	}
	remaining, _ := st.JournalForRoom("room-a", 50)
	if len(remaining) != 2 { // newest old + latest recent
		t.Fatalf("after prune: %d rows, want 2 (%+v)", len(remaining), remaining)
	}
}
