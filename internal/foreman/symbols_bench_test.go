//go:build ts_symbols

package foreman

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadSampleFiles reads a handful of real source files from the repo for
// side-by-side extraction benchmarks.
func loadSampleFiles(tb testing.TB) map[string][]byte {
	tb.Helper()
	root := "."
	files := map[string][]byte{}
	patterns := []string{
		filepath.Join(root, "*.go"),
		filepath.Join(root, "..", "..", "web", "src", "*.jsx"),
	}
	for _, pat := range patterns {
		matches, _ := filepath.Glob(pat)
		for _, m := range matches {
			data, err := os.ReadFile(m)
			if err != nil {
				continue
			}
			if len(data) > 512<<10 {
				continue
			}
			files[m] = data
		}
	}
	if len(files) == 0 {
		tb.Fatal("no sample files found")
	}
	return files
}

func TestTreeSitterExtractsKnownGoSymbols(t *testing.T) {
	files := loadSampleFiles(t)
	src, ok := files[filepath.Join(".", "repomap.go")]
	if !ok {
		t.Skip("repomap.go not found")
	}
	syms := treeSitterSymbols{}.Symbols("repomap.go", src, 50)
	joined := strings.Join(syms, "\n")
	for _, want := range []string{"func buildRepoMap", "func symbolizeLine", "func isSourceFile"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in extracted symbols:\n%s", want, joined)
		}
	}
}

func benchExtractor(b *testing.B, s symbolSource) {
	files := loadSampleFiles(b)
	// warm caches (grammar blobs, page cache)
	for rel, src := range files {
		_ = s.Symbols(rel, src, 12)
	}
	b.ReportAllocs()
	b.ResetTimer()
	symbols := 0
	for i := 0; i < b.N; i++ {
		for rel, src := range files {
			symbols += len(s.Symbols(rel, src, 12))
		}
	}
	b.ReportMetric(float64(symbols)/float64(b.N), "symbols/op")
}

func BenchmarkSymbolsHeuristic(b *testing.B) { benchExtractor(b, heuristicSymbols{}) }
func BenchmarkSymbolsTreeSitter(b *testing.B) {
	benchExtractor(b, treeSitterSymbols{})
}

// TestSymbolExtractorDelta reports how many symbols each extractor finds and
// how they differ, for a human-readable comparison.
func TestSymbolExtractorDelta(t *testing.T) {
	files := loadSampleFiles(t)
	var hTotal, tsTotal int
	for rel, src := range files {
		h := heuristicSymbols{}.Symbols(rel, src, 12)
		ts := treeSitterSymbols{}.Symbols(rel, src, 12)
		hTotal += len(h)
		tsTotal += len(ts)
		if testing.Verbose() {
			t.Logf("%-52s heuristic=%-8v tree-sitter=%v", rel, h, ts)
		}
	}
	t.Logf("totals over %d files: heuristic=%d tree-sitter=%d", len(files), hTotal, tsTotal)
}
