package foreman

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression tests for config shapes found in real repositories. Each one is a
// bug that silently dropped import edges, discovered by running the resolver
// against a 1697-file monorepo.

// TestViteSplitTSConfigAlias: Vite puts the alias in tsconfig.app.json, so
// matching only "tsconfig.json" loses it entirely.
func TestViteSplitTSConfigAlias(t *testing.T) {
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
	write("tsconfig.json", `{"files":[],"references":[{"path":"./tsconfig.app.json"}]}`)
	write("tsconfig.app.json", `{"compilerOptions":{"paths":{"@/*":["./src/*"]}}}`)
	write("tsconfig.node.json", `{"compilerOptions":{}}`)
	write("src/app.ts", "import { h } from \"@/lib/h\";\n")
	write("src/lib/h.ts", "export const h = 1;\n")

	byRel := map[string]bool{"src/app.ts": true, "src/lib/h.ts": true}
	ctx := newImportContext(root, byRel)
	if got := extractImports(root, "src/app.ts", ctx); !contains(got, "src/lib/h.ts") {
		t.Fatalf("alias from tsconfig.app.json not honoured, got %v", got)
	}
}

// TestJSConfigAlias: plain JavaScript projects declare "paths" in
// jsconfig.json. Ignoring it dropped every "@/" import in a JS backend.
func TestJSConfigAlias(t *testing.T) {
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
	write("jsconfig.json", `{"compilerOptions":{"baseUrl":".","paths":{"@/*":["src/*"],"@libs/*":["src/libs/*"]}}}`)
	write("src/schemas/LoginSchema.js", "const Base = require(\"@/libs/http/BaseSchema\");\n")
	write("src/libs/http/BaseSchema.js", "module.exports = {};\n")

	byRel := map[string]bool{
		"src/schemas/LoginSchema.js":  true,
		"src/libs/http/BaseSchema.js": true,
	}
	ctx := newImportContext(root, byRel)
	got := extractImports(root, "src/schemas/LoginSchema.js", ctx)
	if !contains(got, "src/libs/http/BaseSchema.js") {
		t.Fatalf("jsconfig alias not honoured, got %v", got)
	}
}

// TestDeclarationFileResolves: "@/ui/table/types" pointing at types.d.ts is a
// real edge (17 occurrences in one repo). Stripping ".d.ts" must remove the
// whole extension, never leave a dangling ".d".
func TestDeclarationFileResolves(t *testing.T) {
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
	write("tsconfig.json", `{"compilerOptions":{"paths":{"@/*":["./src/*"]}}}`)
	write("src/pages/Customers.tsx", "import type { TColumn } from \"@/components/ui/table/types\";\n")
	write("src/components/ui/table/types.d.ts", "export type TColumn = {};\n")

	byRel := map[string]bool{
		"src/pages/Customers.tsx":            true,
		"src/components/ui/table/types.d.ts": true,
	}
	ctx := newImportContext(root, byRel)
	if got := extractImports(root, "src/pages/Customers.tsx", ctx); !contains(got, "src/components/ui/table/types.d.ts") {
		t.Fatalf("declaration file not resolved, got %v", got)
	}
	if withoutTSExt("a/types.d.ts") != "a/types" {
		t.Errorf("withoutTSExt left a dangling extension: %q", withoutTSExt("a/types.d.ts"))
	}
}

// TestExplicitJSExtensionFindsTS: NodeNext ESM requires the emitted extension,
// so `import "./x.js"` names x.ts. Without this the edge is lost in every
// modern TS project.
func TestExplicitJSExtensionFindsTS(t *testing.T) {
	byRel := map[string]bool{"src/x.ts": true, "src/y.js": true}
	if got := expandCandidate("src/x.js", byRel); got != "src/x.ts" {
		t.Fatalf("got %q, want src/x.ts", got)
	}
	// A genuine .js must still win over a same-named .ts sibling check.
	if got := expandCandidate("src/y.js", byRel); got != "src/y.js" {
		t.Fatalf("got %q, want src/y.js", got)
	}
}

// TestTSBeatsDeclarationWhenBothExist: TypeScript resolves .ts before .d.ts,
// and the extension list order must preserve that.
func TestTSBeatsDeclarationWhenBothExist(t *testing.T) {
	byRel := map[string]bool{"src/x.ts": true, "src/x.d.ts": true}
	if got := expandCandidate("src/x", byRel); got != "src/x.ts" {
		t.Fatalf("got %q, want src/x.ts (implementation before declaration)", got)
	}
}

// TestViteQuerySuffixIsNotAFile: "@/workers/x.worker?worker" is a bundler
// directive, not a path. It must stay unresolved rather than resolving to the
// file without the query, which would invent an edge the bundler does not make.
func TestViteQuerySuffixIsNotAFile(t *testing.T) {
	byRel := map[string]bool{"src/workers/videoTranscode.worker.ts": true}
	if got := expandCandidate("src/workers/videoTranscode.worker?worker", byRel); got != "" {
		t.Fatalf("query suffix should not resolve, got %q", got)
	}
}
