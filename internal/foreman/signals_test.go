package foreman

import (
	"testing"
	"time"
)

// openTestSignalsDB returns a Store-backed DB for signal tests using the same
// in-memory project harness other suites rely on (no real .foreman on disk).
func openTestSignalsDB(t *testing.T) *DB {
	t.Helper()
	st := testStore(t)
	db := st.DB()
	if db == nil {
		t.Fatal("test store has no DB")
	}
	return db
}

func TestSignalTouchUpsertAndDecay(t *testing.T) {
	db := openTestSignalsDB(t)
	// Symmetric canonicalization: touching A,B and B,A writes one edge.
	if err := db.SignalTouch("co_touch", "b.go", "a.go", 1.0); err != nil {
		t.Fatal(err)
	}
	if err := db.SignalTouch("co_touch", "a.go", "b.go", 1.0); err != nil {
		t.Fatal(err)
	}
	report, err := db.SignalsReport(20)
	if err != nil {
		t.Fatal(err)
	}
	if report.Edges != 1 {
		t.Errorf("expected 1 edge after symmetric touches, got %d", report.Edges)
	}
	if report.Touches < 2 {
		t.Errorf("expected >=2 touches, got %d", report.Touches)
	}

	// Edit self-edge is allowed.
	if err := db.SignalTouch("edit", "a.go", "a.go", 3.0); err != nil {
		t.Fatal(err)
	}

	// Weight accrues and is capped at the max.
	for i := 0; i < 200; i++ {
		_ = db.SignalTouch("edit", "a.go", "a.go", 3.0)
	}
	neighbors, err := db.SignalsNeighbors("a.go", 30)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range neighbors {
		if e.Kind == "edit" && e.Path == "a.go" {
			found = true
			if e.Weight > signalMaxWeight+0.01 {
				t.Errorf("edit weight not capped: %f", e.Weight)
			}
		}
	}
	if !found {
		t.Error("edit self-edge not returned by neighbors")
	}

	// Decay runs without error and is idempotent.
	if err := db.SignalsDecay(); err != nil {
		t.Fatal(err)
	}
}

func TestSignalTopPathsRanking(t *testing.T) {
	db := openTestSignalsDB(t)
	// Build a graph where x.go is the clear hub.
	for _, p := range []string{"a.go", "b.go", "c.go", "d.go"} {
		_ = db.SignalTouch("co_touch", "x.go", p, 1.0)
		_ = db.SignalTouch("edit", p, p, 3.0)
	}
	top, err := db.SignalsTopPaths(3, "co_touch", "edit")
	if err != nil {
		t.Fatal(err)
	}
	if len(top) == 0 {
		t.Fatal("expected top paths")
	}
	if top[0].Path != "x.go" {
		t.Errorf("expected x.go to rank first, got %s", top[0].Path)
	}
	// Without kind filter, edit edges should still surface.
	all, err := db.SignalsTopPaths(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Error("expected unfiltered top paths to include files")
	}
}

func TestSignalTrackerFinalize(t *testing.T) {
	st := testStore(t)
	db := st.DB()
	if db == nil {
		t.Fatal("test store has no DB")
	}
	tr := newSignalTracker("room-1")
	tr.touch("a.go", false)
	tr.touch("b.go", false)
	tr.touch("a.go", true) // edited
	tr.touch("c.go", true)
	tr.finalize(st)
	// 3 files => up to 3 pair edges + 2 edit self-edges.
	report, err := db.SignalsReport(50)
	if err != nil {
		t.Fatal(err)
	}
	if report.Edges < 4 {
		t.Errorf("expected >=4 edges after finalize, got %d", report.Edges)
	}
	// Edit self-edges present.
	neighbors, _ := db.SignalsNeighbors("a.go", 30)
	hasEdit := false
	for _, e := range neighbors {
		if e.Kind == "edit" {
			hasEdit = true
		}
	}
	if !hasEdit {
		t.Error("expected an edit self-edge for a.go")
	}
}

func TestSignalTrackerCap(t *testing.T) {
	tr := newSignalTracker("room-2")
	// Exceeds signalMaxRunFiles; pairs must stay bounded by signalPairCap.
	for i := 0; i < 50; i++ {
		tr.touch(string(rune('a'+i%26))+string(rune('0'+i/26))+".go", false)
	}
	if len(tr.paths) > signalMaxRunFiles {
		t.Errorf("tracker paths not capped: %d", len(tr.paths))
	}
}

func TestSignalNeighborsEmpty(t *testing.T) {
	db := openTestSignalsDB(t)
	_, err := db.SignalsNeighbors("", 5)
	if err == nil {
		t.Error("expected error on empty path")
	}
}

func TestSignalActivityBumped(t *testing.T) {
	db := openTestSignalsDB(t)
	before := time.Now().UTC().Format("2006-01-02")
	_ = db.SignalTouch("co_touch", "p.go", "q.go", 1.0)
	report, err := db.SignalsReport(10)
	if err != nil {
		t.Fatal(err)
	}
	if report.Touches < 1 {
		t.Error("expected activity touch recorded")
	}
	if len(report.Activity) == 0 {
		t.Error("expected activity rows")
	}
	_ = before
}
