package foreman

import (
	"os"
	"path/filepath"
	"testing"
)

// importWorkspace builds a fake TS project on disk and returns (root, byRel).
func importWorkspace(t *testing.T) (string, map[string]bool) {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("backend/src/services/tickets/mutation.service.ts", `import { OPEN } from "../../constants/ticket.constants";
import { repo } from "./store";`)
	write("backend/src/constants/ticket.constants.ts", "export const OPEN = 'open';\n")
	write("backend/src/services/tickets/store.ts", "export const repo = 1;\n")
	byRel := map[string]bool{}
	_ = walkProject(root, func(path, rel string, info os.FileInfo) error {
		if isImportSrcFile(rel) {
			byRel[rel] = true
		}
		return nil
	})
	return root, byRel
}

func TestExtractImportsTSRelative(t *testing.T) {
	root, byRel := importWorkspace(t)
	got := extractImports(root, "backend/src/services/tickets/mutation.service.ts", "", byRel)
	want := map[string]bool{
		"backend/src/constants/ticket.constants.ts": true,
		"backend/src/services/tickets/store.ts":     true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected import %q", g)
		}
	}
}

func TestGoModuleImportResolvesIndependently(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module github.com/owner/app\n\ngo 1.26\n")
	write("main.go", "package main\n\nimport (\n\t\"github.com/owner/app/util/db\"\n\t\"fmt\"\n)\n")
	write("util/db.go", "package db\n")
	byRel := map[string]bool{}
	_ = walkProject(root, func(path, rel string, info os.FileInfo) error {
		if isImportSrcFile(rel) {
			byRel[rel] = true
		}
		return nil
	})
	got := extractImports(root, "main.go", "github.com/owner/app", byRel)
	if len(got) != 1 || got[0] != "util/db.go" {
		t.Fatalf("got %v, want [util/db.go]", got)
	}
}

func TestImportAmbiguityFailsClosed(t *testing.T) {
	// Two files match the same suffix; the extractor must drop the specifier
	// instead of guessing.
	byRel := map[string]bool{
		"a/services/ticket.ts": true,
		"b/services/ticket.ts": true,
	}
	if got := matchBySuffix("services/ticket", byRel); got != "" {
		t.Fatalf("ambiguous suffix resolved to %q, want empty", got)
	}
	if got := matchBySuffix("services/ticket", map[string]bool{"a/services/ticket.ts": true}); got != "a/services/ticket.ts" {
		t.Fatalf("unique suffix should resolve, got %q", got)
	}
}

func TestBareNpmSpecifiersAreDropped(t *testing.T) {
	byRel := map[string]bool{"src/app.ts": true}
	if got := resolveImportSpecifier("src/app.ts", "src", "react", "", byRel); got != "" {
		t.Fatalf("bare package name resolved to %q, want empty", got)
	}
	if got := resolveImportSpecifier("src/app.ts", "src", "@acme/lib", "", byRel); got != "" {
		t.Fatalf("npm scope resolved to %q, want empty", got)
	}
}

func TestReplaceSignalImportsSwapsTheSet(t *testing.T) {
	db := openTestSignalsDB(t)
	one := []SignalEdgeDelta{{Kind: "import", Src: "a.ts", Dst: "b.ts", Weight: signalWeightImport}}
	if err := db.ReplaceSignalImports(one); err != nil {
		t.Fatal(err)
	}
	two := []SignalEdgeDelta{{Kind: "import", Src: "c.ts", Dst: "d.ts", Weight: signalWeightImport}}
	if err := db.ReplaceSignalImports(two); err != nil {
		t.Fatal(err)
	}
	r, err := db.SignalsReport(10)
	if err != nil {
		t.Fatal(err)
	}
	if r.Edges != 1 {
		t.Fatalf("expected exactly 1 ImportError edge after replace, got %d", r.Edges)
	}
	neighbors, err := db.SignalsNeighbors("d.ts", 10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range neighbors {
		if e.Kind == "import" && e.Path == "d.ts" && e.OtherPath == "c.ts" {
			found = true
			if e.Weight != signalWeightImport {
				t.Errorf("import weight = %v", e.Weight)
			}
		}
	}
	if !found {
		t.Fatalf("import edge missing after refresh: %+v", neighbors)
	}
}

func TestSignalTouchBatchIsAtomic(t *testing.T) {
	db := openTestSignalsDB(t)
	before, err := db.SignalsReport(10)
	if err != nil {
		t.Fatal(err)
	}
	// The batch contains one invalid edge in the middle: the whole batch must
	// be rejected and nothing written.
	batch := []SignalEdgeDelta{
		{Kind: "co_touch", Src: "a.ts", Dst: "b.ts", Weight: 1},
		{Kind: "co_touch", Src: "", Dst: "x.ts", Weight: 1}, // invalid
		{Kind: "edit", Src: "a.ts", Dst: "a.ts", Weight: signalWeightEdit},
	}
	if err := db.SignalTouchBatch(batch); err == nil {
		t.Fatal("expected the invalid delta to reject the batch")
	}
	after, err := db.SignalsReport(10)
	if err != nil {
		t.Fatal(err)
	}
	if after.Edges != before.Edges {
		t.Fatalf("batch was not atomic: edges %d → %d", before.Edges, after.Edges)
	}
	// A valid batch writes everything in one shot.
	valid := []SignalEdgeDelta{
		{Kind: "co_touch", Src: "a.ts", Dst: "b.ts", Weight: 1},
		{Kind: "search", Src: "b.ts", Dst: "b.ts", Weight: signalWeightSearch},
	}
	if err := db.SignalTouchBatch(valid); err != nil {
		t.Fatal(err)
	}
	r, err := db.SignalsReport(10)
	if err != nil {
		t.Fatal(err)
	}
	if r.Edges != before.Edges+2 {
		t.Fatalf("expected %d edges after valid batch, got %d", before.Edges+2, r.Edges)
	}
}

func TestSearchMentionBecomesWeakSelfEdgeOnly(t *testing.T) {
	st := testStore(t)
	db := st.DB()
	tr := newSignalTracker("room-mention")
	// grep surfaced both files but the agent never read them: they must show
	// up as search self-edges without co_touch pairs between them.
	tr.mention("hit-a.ts", "hit-b.ts")
	tr.finalize(st)
	neighbors, err := db.SignalsNeighbors("hit-a.ts", 20)
	if err != nil {
		t.Fatal(err)
	}
	var search, couTouch bool
	for _, e := range neighbors {
		if e.Kind == "search" && e.OtherPath == "hit-a.ts" {
			search = true
			if e.Weight != signalWeightSearch {
				t.Errorf("search weight = %v, want %v", e.Weight, signalWeightSearch)
			}
		}
		if e.Kind == "co_touch" {
			couTouch = true
		}
	}
	if !search {
		t.Error("expected a search self-edge for hit-a.ts")
	}
	if couTouch {
		t.Error("mentions must not fabricate co_touch pairs")
	}
}

func TestRepeatViewRaisesPairWeight(t *testing.T) {
	st := testStore(t)
	db := st.DB()
	tr := newSignalTracker("room-repeat")
	// Both files studied (two views each) → each scores 1.0 + 0.5 = 1.5,
	// and the pair weight is the weaker side.
	tr.touch("a.ts", false)
	tr.touch("a.ts", false)
	tr.touch("b.ts", false)
	tr.touch("b.ts", false)
	tr.finalize(st)
	neighbors, err := db.SignalsNeighbors("a.ts", 20)
	if err != nil {
		t.Fatal(err)
	}
	var pair *SignalEdge
	for _, edge := range neighbors {
		if edge.Kind == "co_touch" {
			c := edge
			pair = &c
		}
	}
	if pair == nil {
		t.Fatal("expected a co_touch edge between a.ts and b.ts")
	}
	if pair.Weight < 1.5-0.01 || pair.Weight > 1.5+0.01 {
		t.Errorf("pair weight = %v, want 1.5", pair.Weight)
	}
}

func TestEditsDominatePairWeight(t *testing.T) {
	st := testStore(t)
	db := st.DB()
	tr := newSignalTracker("room-edit")
	tr.touch("bookmark.ts", true)
	tr.touch("b.ts", true)
	tr.finalize(st)
	neighbors, err := db.SignalsNeighbors("bookmark.ts", 20)
	if err != nil {
		t.Fatal(err)
	}
	var pair float64
	for _, edge := range neighbors {
		if edge.Kind == "co_touch" {
			pair = edge.Weight
		}
	}
	if pair < signalWeightEdit-0.01 || pair > signalWeightEdit+0.01 {
		t.Errorf("pair weight = %v, want %v", pair, signalWeightEdit)
	}
}

func TestSignalsDecayThrottlesAtRunEnd(t *testing.T) {
	db := openTestSignalsDB(t)
	ran, err := db.SignalsDecayIfStale(signalDecayInterval)
	if err != nil || !ran {
		t.Fatalf("first decay should run: ran=%v err=%v", ran, err)
	}
	ran, err = db.SignalsDecayIfStale(signalDecayInterval)
	if err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Fatal("second decay within the interval must be skipped")
	}
	ran, err = db.SignalsDecayIfStale(0)
	if err != nil || !ran {
		t.Fatalf("zero interval forces a pass: ran=%v err=%v", ran, err)
	}
}
