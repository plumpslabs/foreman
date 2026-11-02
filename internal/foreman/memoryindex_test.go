package foreman

import (
	"strings"
	"testing"
)

func newMemoryTestDB(t *testing.T) *DB {
	t.Helper()
	d := testStore(t).DB() // temp project store; initMemoryIndex runs inside OpenStore
	if err := d.initMemoryIndex(); err != nil {
		t.Fatalf("initMemoryIndex: %v", err)
	}
	return d
}

func TestSaveMemoryFactDedupesIdenticalBody(t *testing.T) {
	d := newMemoryTestDB(t)
	a, err := d.SaveMemoryFact("room1", "ada", "lesson", "run go test before finish_task")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	b, err := d.SaveMemoryFact("room1", "linus", "fact", "run go test before finish_task")
	if err != nil {
		t.Fatalf("save2: %v", err)
	}
	if a.ID != b.ID {
		t.Errorf("identical bodies must be idempotent, got %s vs %s", a.ID, b.ID)
	}
	facts, err := d.RoomMemoryFacts("room1", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("want 1 stored fact, got %d", len(facts))
	}
	if facts[0].Kind != "lesson" {
		t.Errorf("first save wins; kind = %q", facts[0].Kind)
	}
}

func TestSaveMemoryFactNormalizesKind(t *testing.T) {
	d := newMemoryTestDB(t)
	f, err := d.SaveMemoryFact("room1", "ada", " LESSON ", "the router needs a rate limit")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if f.Kind != "lesson" {
		t.Errorf("kind = %q, want lesson", f.Kind)
	}
}

func TestRecallMemoryFactsByText(t *testing.T) {
	d := newMemoryTestDB(t)
	_, err := d.SaveMemoryFact("r", "a", "fact", "the websocket endpoint lives in server.go")
	if err != nil {
		t.Fatalf("save1: %v", err)
	}
	_, err = d.SaveMemoryFact("r", "a", "lesson", "always scope budgets per run")
	if err != nil {
		t.Fatalf("save2: %v", err)
	}
	_, err = d.SaveMemoryFact("other", "a", "fact", "the websocket endpoint lives in server.go other room")
	if err != nil {
		t.Fatalf("save3: %v", err)
	}
	hits, err := d.RecallMemoryFacts("r", "websocket endpoint", 5)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Body, "websocket") {
		t.Errorf("want only the in-room websocket fact, got %+v", hits)
	}
	recent, err := d.RecallMemoryFacts("r", "", 5)
	if err != nil {
		t.Fatalf("recent recall: %v", err)
	}
	if len(recent) != 2 {
		t.Errorf("empty query should return recent facts, got %d", len(recent))
	}
}

func TestRecallMemoryFactsFallbackWithoutFTS5(t *testing.T) {
	d := newMemoryTestDB(t)
	d.fts5 = false // simulate FTS5-less engine; mirror table is not consulted
	_, err := d.SaveMemoryFact("r", "a", "fact", "cache invalidation happens in codeindex.go")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	hits, err := d.RecallMemoryFacts("r", "invalidation cache", 5)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("plain-scan recall failed, got %v", hits)
	}
}

func TestTouchAndForgetMemoryFacts(t *testing.T) {
	d := newMemoryTestDB(t)
	f, _ := d.SaveMemoryFact("r", "a", "fact", "flaky test: TestRetryPicker on cold db")
	d.TouchMemoryFacts([]string{f.ID, "missing-id-is-fine"})
	got, _ := d.RoomMemoryFacts("r", 10)
	if got[0].UsedCount != 1 {
		t.Errorf("used_count = %d, want 1", got[0].UsedCount)
	}
	if got[0].UsedAt.IsZero() {
		t.Errorf("used_at must be set after touch")
	}
	ok, err := d.ForgetMemoryFact(f.ID)
	if err != nil || !ok {
		t.Fatalf("forget: ok=%v err=%v", ok, err)
	}
	if rest, _ := d.RoomMemoryFacts("r", 10); len(rest) != 0 {
		t.Errorf("forget must remove the fact, got %v", rest)
	}
	if ok, _ := d.ForgetMemoryFact("no-such-id"); ok {
		t.Errorf("forget of unknown id must report false")
	}
}
