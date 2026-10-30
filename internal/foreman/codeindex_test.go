package foreman

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCodeIndexIndexAndSearch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {\n\tprintln(\"hello world\")\n}\n")
	writeFile(t, dir, "util.go", "package main\n\n// does nothing useful\nfunc helper() {}\n")
	writeFile(t, dir, "greet.go", "package main\n\nconst greeting = \"hello world\"\n")

	st, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer st.Close()

	if st.db == nil || !st.db.fts5 {
		t.Skip("FTS5 unavailable in this build")
	}

	indexed, removed, err := st.IndexWorkspace(dir)
	if err != nil {
		t.Fatalf("IndexWorkspace: %v", err)
	}
	if indexed == 0 {
		t.Fatalf("expected files to be indexed, got %d", indexed)
	}
	if removed != 0 {
		t.Fatalf("expected no removals on first index, got %d", removed)
	}

	hits, usedIndex, err := st.SearchWorkspace(dir, "helper", 30)
	if err != nil {
		t.Fatalf("SearchWorkspace: %v", err)
	}
	if !usedIndex {
		t.Fatalf("expected the FTS5 index to be used")
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit for 'helper', got %d (%+v)", len(hits), hits)
	}
	if hits[0].Path != "util.go" || hits[0].Line != 4 {
		t.Fatalf("unexpected hit: %+v", hits[0])
	}

	// Multi-term AND search.
	hits, _, _ = st.SearchWorkspace(dir, "hello world", 30)
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits for 'hello world', got %d (%+v)", len(hits), hits)
	}
}

func TestCodeIndexIncremental(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package a\nfunc one() {}\n")

	st, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer st.Close()
	if st.db == nil || !st.db.fts5 {
		t.Skip("FTS5 unavailable in this build")
	}

	if _, _, err := st.IndexWorkspace(dir); err != nil {
		t.Fatal(err)
	}
	// Second pass with no changes must index nothing and remove nothing.
	indexed, removed, err := st.IndexWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if indexed != 0 || removed != 0 {
		t.Fatalf("unchanged re-index should be a no-op, got indexed=%d removed=%d", indexed, removed)
	}

	// Modify the file; it must be re-indexed and searchable with the new line.
	writeFile(t, dir, "a.go", "package a\nfunc one() {}\nfunc two() {}\n")
	indexed, _, err = st.IndexWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if indexed != 1 {
		t.Fatalf("expected 1 re-indexed file, got %d", indexed)
	}
	hits, _, _ := st.SearchWorkspace(dir, "two", 30)
	if len(hits) != 1 || hits[0].Line != 3 {
		t.Fatalf("expected new symbol at line 3, got %+v", hits)
	}

	// Deleting the file removes it from the index.
	if err := os.Remove(filepath.Join(dir, "a.go")); err != nil {
		t.Fatal(err)
	}
	_, removed, err = st.IndexWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 removal, got %d", removed)
	}
	hits, _, _ = st.SearchWorkspace(dir, "two", 30)
	if len(hits) != 0 {
		t.Fatalf("expected no hits after delete, got %+v", hits)
	}
}
