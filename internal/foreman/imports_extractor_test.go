package foreman

import (
	"os"
	"path/filepath"
	"testing"
)

// importCase is one file plus the imports it should resolve to, used by the
// shared extractor tests below. Keeping the fixtures in one place means the
// line scanner and the tree-sitter extractor are held to the same answers.
type importCase struct {
	name string
	rel  string
	body string
	want []string // workspace-relative destinations, order-insensitive
	// fallbackOK reports whether the dependency-free line scanner is expected
	// to find this shape. Cases with fallbackOK=false are the known blind
	// spots that justify the opt-in tree-sitter extractor; the fallback test
	// asserts they are still missed so the gap stays documented instead of
	// silently rotting.
	fallbackOK bool
}

func importCases() []importCase {
	return []importCase{
		{
			name:       "ts single-line named import",
			rel:        "src/services/mutation.ts",
			body:       "import { OPEN } from \"../constants/ticket.constants\";\n",
			want:       []string{"src/constants/ticket.constants.ts"},
			fallbackOK: true,
		},
		{
			name:       "ts default import",
			rel:        "src/services/mutation.ts",
			body:       "import repo from \"../constants/ticket.constants\";\n",
			want:       []string{"src/constants/ticket.constants.ts"},
			fallbackOK: true,
		},
		{
			name:       "ts namespace import",
			rel:        "src/services/mutation.ts",
			body:       "import * as c from \"../constants/ticket.constants\";\n",
			want:       []string{"src/constants/ticket.constants.ts"},
			fallbackOK: true,
		},
		{
			name:       "ts type-only import",
			rel:        "src/services/mutation.ts",
			body:       "import type { Ticket } from \"../constants/ticket.constants\";\n",
			want:       []string{"src/constants/ticket.constants.ts"},
			fallbackOK: true,
		},
		{
			name: "ts multiline named import",
			rel:  "src/services/mutation.ts",
			body: "import {\n  OPEN,\n  CLOSED,\n} from \"../constants/ticket.constants\";\n",
			want: []string{"src/constants/ticket.constants.ts"},
		},
		{
			name:       "ts export from",
			rel:        "src/services/mutation.ts",
			body:       "export { helper } from \"../constants/ticket.constants\";\n",
			want:       []string{"src/constants/ticket.constants.ts"},
			fallbackOK: true,
		},
		{
			name:       "ts export star",
			rel:        "src/services/mutation.ts",
			body:       "export * from \"../constants/ticket.constants\";\n",
			want:       []string{"src/constants/ticket.constants.ts"},
			fallbackOK: true,
		},
		{
			name:       "ts require",
			rel:        "src/services/mutation.ts",
			body:       "const c = require(\"../constants/ticket.constants\");\n",
			want:       []string{"src/constants/ticket.constants.ts"},
			fallbackOK: true,
		},
		{
			name: "ts dynamic import",
			rel:  "src/services/mutation.ts",
			body: "const c = await import(\"../constants/ticket.constants\");\n",
			want: []string{"src/constants/ticket.constants.ts"},
		},
		{
			name:       "python from relative",
			rel:        "app/routes/users.py",
			body:       "from ..models import User\n",
			want:       []string{"app/models.py"},
			fallbackOK: true,
		},
		{
			name:       "python import dotted",
			rel:        "app/routes/users.py",
			body:       "import app.models\n",
			want:       []string{"app/models.py"},
			fallbackOK: true,
		},
		{
			name:       "rust mod declaration",
			rel:        "src/lib.rs",
			body:       "mod tickets;\n",
			want:       []string{"src/tickets.rs"},
			fallbackOK: true, // resolved by the .rs sibling rule, not by regex joining
		},
		{
			name:       "rust use crate path",
			rel:        "src/lib.rs",
			body:       "use crate::tickets::mutation;\n",
			want:       []string{"src/tickets/mutation.rs"},
			fallbackOK: true,
		},
	}
}

// importFixtureWorkspace materialises a small multi-language workspace with
// every file the cases above can resolve to.
func importFixtureWorkspace(t *testing.T) (string, map[string]bool) {
	t.Helper()
	root := t.TempDir()
	files := []string{
		"src/constants/ticket.constants.ts",
		"src/services/mutation.ts",
		"app/models.py",
		"app/routes/users.py",
		"src/lib.rs",
		"src/tickets.rs",
		"src/tickets/mutation.rs",
	}
	for _, rel := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("// fixture\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	byRel := map[string]bool{}
	for _, rel := range files {
		byRel[rel] = true
	}
	return root, byRel
}

// runImportCases drives one extractor directly, so a failure names the
// extractor that got it wrong instead of hiding behind the seam. With
// onlyFallbackOK it asserts the shapes the dependency-free scanner is expected
// to miss really are missed.
func runImportCases(t *testing.T, src importSource, onlyFallbackOK bool) {
	t.Helper()
	for _, c := range importCases() {
		if onlyFallbackOK && !c.fallbackOK {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			specs, _ := src.Specifiers(c.rel, []byte(c.body))
			_, byRel := importFixtureWorkspace(t)
			dir := filepath.Dir(c.rel)
			got := map[string]bool{}
			for _, spec := range specs {
				if dst := resolveImportSpecifier(c.rel, dir, spec, "", byRel); dst != "" {
					got[dst] = true
				}
			}
			for _, w := range c.want {
				if !got[w] {
					t.Errorf("missing import %q (specifiers=%v)", w, specs)
				}
			}
			for dst := range got {
				found := false
				for _, w := range c.want {
					if dst == w {
						found = true
					}
				}
				if !found {
					t.Errorf("unexpected import %q (specifiers=%v)", dst, specs)
				}
			}
		})
	}
}

// TestLineImportsHandlesCommonShapes pins the default, dependency-free path on
// the shapes it is expected to handle. This test runs in every build,
// including ones without the ts_symbols tag, and is the proof that dropping
// tree-sitter never breaks the common cases.
func TestLineImportsHandlesCommonShapes(t *testing.T) {
	runImportCases(t, lineImports{}, true)
}

// TestConfiguredImportExtractorFindsCommonShapes runs the fixtures through
// whatever extractor this build is configured with, asserting every shape when
// the tree-sitter build is active and only the supported shapes otherwise.
func TestConfiguredImportExtractorFindsCommonShapes(t *testing.T) {
	runImportCases(t, fileImports, !importsUseTreeSitter)
}

// TestLineImportsBlindSpots documents the shapes the line scanner cannot see.
// If it ever learns to join wrapped statements these assertions fail, which is
// the signal to revisit the importSource comment and the opt-in rationale.
func TestLineImportsBlindSpots(t *testing.T) {
	for _, c := range importCases() {
		if c.fallbackOK {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			specs, _ := lineImports{}.Specifiers(c.rel, []byte(c.body))
			_, byRel := importFixtureWorkspace(t)
			dir := filepath.Dir(c.rel)
			for _, spec := range specs {
				if dst := resolveImportSpecifier(c.rel, dir, spec, "", byRel); dst != "" {
					t.Fatalf("line scanner now resolves %q; update the documented blind spots", dst)
				}
			}
		})
	}
}

// TestExtractImportsFallsBackOnUnparseableFile ensures a file with syntax
// errors still contributes edges: the tree-sitter extractor declines it and
// extractImports falls back to the line scanner.
func TestExtractImportsFallsBackOnUnparseableFile(t *testing.T) {
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
	write("src/broken.ts", "import { a } from \"./dep\";\nfunction ((( {\n")
	write("src/dep.ts", "export const a = 1;\n")
	byRel := map[string]bool{"src/broken.ts": true, "src/dep.ts": true}

	got := extractImports(root, "src/broken.ts", &importContext{byRel: byRel})
	if len(got) != 1 || got[0] != "src/dep.ts" {
		t.Fatalf("got %v, want [src/dep.ts]", got)
	}
}

// TestPythonRelativeImportDepth covers the dot-depth arithmetic directly, since
// getting it wrong silently points an edge at the wrong file.
func TestPythonRelativeImportDepth(t *testing.T) {
	byRel := map[string]bool{
		"app/models.py":        true,
		"app/routes/users.py":  true,
		"app/routes/models.py": true,
	}
	cases := []struct{ spec, want string }{
		{".models", "app/routes/models.py"}, // same package
		{"..models", "app/models.py"},       // one level up
		{"...models", ""},                   // above the workspace root
		{".models.user", ""},                // unresolvable, must not guess
	}
	for _, c := range cases {
		got := resolveImportSpecifier("app/routes/users.py", "app/routes", c.spec, "", byRel)
		if got != c.want {
			t.Errorf("resolve(%q) = %q, want %q", c.spec, got, c.want)
		}
	}
}
