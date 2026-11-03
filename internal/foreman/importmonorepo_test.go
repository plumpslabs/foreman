package foreman

import (
	"os"
	"path/filepath"
	"testing"
)

// Monorepo fixtures.
//
// A monorepo breaks every single-root assumption at once: no root go.mod, two
// apps that each declare their own "@/*" tsconfig alias, workspace packages
// addressed by package.json name, and a cargo workspace with several crates.
// Each test below pins one of those boundaries.

// monoWorkspace writes a realistic monorepo and returns (root, ctx).
func monoWorkspace(t *testing.T) (string, *importContext) {
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

	// --- pnpm workspace with two apps and two shared packages ---
	write("pnpm-workspace.yaml", "packages:\n  - 'apps/*'\n  - 'packages/*'\n")
	write("packages/ui/package.json", `{"name":"@acme/ui","main":"./src/index.ts"}`)
	write("packages/ui/src/index.ts", "export * from \"./button\";\n")
	write("packages/ui/src/button.ts", "export const Button = 1;\n")
	write("packages/core/package.json", `{"name":"@acme/core","exports":{".":{"import":"./src/index.ts"}}}`)
	write("packages/core/src/index.ts", "export * from \"./api\";\n")
	write("packages/core/src/api.ts", "export const api = 1;\n")

	write("apps/web/package.json", `{"name":"@acme/web"}`)
	write("apps/web/tsconfig.json", `{"compilerOptions":{"baseUrl":".","paths":{"@/*":["./src/*"]}}}`)
	write("apps/web/src/main.ts", `import { Button } from "@acme/ui";
import { api } from "@acme/core";
import { helper } from "@/lib/helper";
`)
	write("apps/web/src/lib/helper.ts", "export const helper = 1;\n")

	// A second app with its OWN @/ alias pointing at a different directory.
	// This is the trap: a single global alias would resolve both apps to
	// whichever file the suffix scan found first.
	write("apps/admin/package.json", `{"name":"@acme/admin"}`)
	write("apps/admin/tsconfig.json", `{"compilerOptions":{"baseUrl":".","paths":{"@/*":["./src/*"]}}}`)
	write("apps/admin/src/main.ts", `import { helper } from "@/lib/helper";
`)
	write("apps/admin/src/lib/helper.ts", "export const helper = 2;\n")

	// --- Go workspace: two modules, no root go.mod ---
	write("go.work", "go 1.22\n\nuse (\n\t./services/api\n\t./services/worker\n)\n")
	write("services/api/go.mod", "module github.com/acme/api\n\ngo 1.22\n")
	write("services/api/main.go", "package main\n\nimport \"github.com/acme/worker/pkg/job\"\n")
	write("services/worker/go.mod", "module github.com/acme/worker\n\ngo 1.22\n")
	write("services/worker/pkg/job/job.go", "package job\n")
	write("services/worker/pkg/job/job_test.go", "package job\n")

	// --- cargo workspace: two crates ---
	write("Cargo.toml", "[workspace]\nmembers = [\"crates/*\"]\n")
	write("crates/engine/Cargo.toml", "[package]\nname = \"engine\"\nversion = \"0.1.0\"\n")
	write("crates/engine/src/lib.rs", "mod rules;\n")
	write("crates/engine/src/rules.rs", "pub fn check() {}\n")
	write("crates/server/Cargo.toml", "[package]\nname = \"server\"\nversion = \"0.1.0\"\n")
	write("crates/server/src/main.rs", "use engine::rules;\n")

	byRel := map[string]bool{}
	if err := walkProject(root, func(_, rel string, _ os.FileInfo) error {
		if isImportSrcFile(rel) {
			byRel[rel] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return root, newImportContext(root, byRel)
}

// TestMonorepoWorkspacePackagesResolve checks bare specifiers that name a
// workspace package (@acme/ui) resolve through package.json, including the
// `exports` map when there is no `main`.
func TestMonorepoWorkspacePackagesResolve(t *testing.T) {
	root, ctx := monoWorkspace(t)
	got := extractImports(root, "apps/web/src/main.ts", ctx)
	want := map[string]bool{
		"packages/ui/src/index.ts":   true, // via "main"
		"packages/core/src/index.ts": true, // via exports["."].import
		"apps/web/src/lib/helper.ts": true, // via local "@/*" alias
	}
	for w := range want {
		if !contains(got, w) {
			t.Errorf("missing %q in %v", w, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %v, want exactly %d entries", got, len(want))
	}
}

// TestMonorepoAliasesAreScopedPerPackage is the regression test for the subtle
// bug: two apps declaring the same "@/*" pattern must each resolve into their
// own directory, never into each other's.
func TestMonorepoAliasesAreScopedPerPackage(t *testing.T) {
	root, ctx := monoWorkspace(t)
	web := extractImports(root, "apps/web/src/main.ts", ctx)
	admin := extractImports(root, "apps/admin/src/main.ts", ctx)

	if !contains(web, "apps/web/src/lib/helper.ts") {
		t.Errorf("web should resolve its own alias, got %v", web)
	}
	if contains(web, "apps/admin/src/lib/helper.ts") {
		t.Errorf("web must not resolve into admin's package: %v", web)
	}
	if !contains(admin, "apps/admin/src/lib/helper.ts") {
		t.Errorf("admin should resolve its own alias, got %v", admin)
	}
	if contains(admin, "apps/web/src/lib/helper.ts") {
		t.Errorf("admin must not resolve into web's package: %v", admin)
	}
}

// TestMonorepoGoWorkspaceModules checks nested Go modules resolve through
// go.work/go.mod even though the workspace root has no go.mod at all.
func TestMonorepoGoWorkspaceModules(t *testing.T) {
	root, ctx := monoWorkspace(t)
	got := extractImports(root, "services/api/main.go", ctx)
	// A Go import names a package directory, so the edge points at a
	// representative file inside it — and never at a _test.go file.
	if !contains(got, "services/worker/pkg/job/job.go") {
		t.Fatalf("want services/worker/pkg/job/job.go, got %v", got)
	}
	if contains(got, "services/worker/pkg/job/job_test.go") {
		t.Errorf("edge must not point at a test file: %v", got)
	}
}

// TestMonorepoRustWorkspaceCrates checks `use engine::rules` resolves across
// crates in a cargo workspace, and that `mod rules;` stays local.
func TestMonorepoRustWorkspaceCrates(t *testing.T) {
	root, ctx := monoWorkspace(t)
	server := extractImports(root, "crates/server/src/main.rs", ctx)
	if !contains(server, "crates/engine/src/rules.rs") {
		t.Errorf("cross-crate use should resolve, got %v", server)
	}
	engine := extractImports(root, "crates/engine/src/lib.rs", ctx)
	if !contains(engine, "crates/engine/src/rules.rs") {
		t.Errorf("mod rules; should resolve next to lib.rs, got %v", engine)
	}
}

// TestMonorepoContextDiscoversManifests pins the discovery half: the context
// must see every manifest, and order longest-prefix-first so nested modules
// win over their parents.
func TestMonorepoContextDiscoversManifests(t *testing.T) {
	_, ctx := monoWorkspace(t)

	if len(ctx.goModules) != 2 {
		t.Fatalf("want 2 go modules, got %+v", ctx.goModules)
	}
	for i := 1; i < len(ctx.goModules); i++ {
		if len(ctx.goModules[i-1].Path) < len(ctx.goModules[i].Path) {
			t.Errorf("go modules not sorted longest-first: %+v", ctx.goModules)
		}
	}
	if len(ctx.jsPackages) != 4 {
		t.Errorf("want 4 js packages, got %+v", ctx.jsPackages)
	}
	if len(ctx.tsAliases) != 2 {
		t.Errorf("want 2 ts aliases (one per app), got %+v", ctx.tsAliases)
	}
	if len(ctx.rustCrates) != 2 {
		t.Errorf("want 2 rust crates, got %+v", ctx.rustCrates)
	}
}

// TestNestedModuleBeatsParentModule: a module declared inside another module's
// directory must claim its own imports.
func TestNestedModuleBeatsParentModule(t *testing.T) {
	ctx := &importContext{
		byRel: map[string]bool{
			"internal/tool/tool.go": true,
			"pkg/x/x.go":            true,
		},
		goModules: []goModule{ // already longest-first
			{Path: "github.com/acme/app/internal/tool", Dir: "internal/tool"},
			{Path: "github.com/acme/app", Dir: ""},
		},
	}
	got, ok := ctx.resolveGoSpecifier("github.com/acme/app/internal/tool/tool")
	if !ok || got != "internal/tool/tool.go" {
		t.Fatalf("nested module should claim the import, got %q ok=%v", got, ok)
	}
}

// TestMonorepoAmbiguityStillFailsClosed: monorepo support must not turn into
// guessing. An alias with no matching file resolves to nothing.
func TestMonorepoAmbiguityStillFailsClosed(t *testing.T) {
	root, ctx := monoWorkspace(t)
	p := filepath.Join(root, "apps/web/src/other.ts")
	if err := os.WriteFile(p, []byte("import { x } from \"@/missing/thing\";\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx.byRel["apps/web/src/other.ts"] = true
	if got := extractImports(root, "apps/web/src/other.ts", ctx); len(got) != 0 {
		t.Fatalf("unresolvable alias must produce no edge, got %v", got)
	}
}

// TestSingleRepoUnaffected guards the non-monorepo path: a plain repo with one
// root go.mod and a root tsconfig alias must behave exactly as before.
func TestSingleRepoUnaffected(t *testing.T) {
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
	write("go.mod", "module github.com/owner/app\n\ngo 1.22\n")
	write("main.go", "package main\n\nimport \"github.com/owner/app/internal/util\"\n")
	write("internal/util/util.go", "package util\n")
	write("tsconfig.json", `{"compilerOptions":{"baseUrl":".","paths":{"@/*":["./src/*"]}}}`)
	write("src/app.ts", "import { h } from \"@/lib/h\";\n")
	write("src/lib/h.ts", "export const h = 1;\n")

	byRel := map[string]bool{}
	_ = walkProject(root, func(_, rel string, _ os.FileInfo) error {
		if isImportSrcFile(rel) {
			byRel[rel] = true
		}
		return nil
	})
	ctx := newImportContext(root, byRel)

	if got := extractImports(root, "main.go", ctx); !contains(got, "internal/util/util.go") {
		t.Errorf("root go module should still resolve, got %v", got)
	}
	if got := extractImports(root, "src/app.ts", ctx); !contains(got, "src/lib/h.ts") {
		t.Errorf("root tsconfig alias should still resolve, got %v", got)
	}
}

// TestJSONCAndTrailingCommas: real tsconfig.json files contain comments and
// trailing commas, which encoding/json rejects outright.
func TestJSONCAndTrailingCommas(t *testing.T) {
	root := t.TempDir()
	cfg := `{
  // base config
  "compilerOptions": {
    "baseUrl": ".",
    /* path aliases */
    "paths": {
      "@/*": ["./src/*"],
    },
  },
}`
	if err := os.WriteFile(filepath.Join(root, "tsconfig.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	aliases := readTSConfigAliases(root, "tsconfig.json")
	if len(aliases) != 1 || aliases[0].Pattern != "@/*" {
		t.Fatalf("want the @/* alias, got %+v", aliases)
	}
}

// TestBarePackageWithNoWorkspaceEntryIsDropped: a third-party package that
// merely shares a scope with a workspace package must not be guessed.
func TestBarePackageWithNoWorkspaceEntryIsDropped(t *testing.T) {
	root, ctx := monoWorkspace(t)
	got := extractImports(root, "apps/web/src/main.ts", ctx)
	for _, g := range got {
		if g == "" {
			t.Errorf("empty destination in %v", got)
		}
	}
	// @acme/unknown is not a workspace package and has no file to point at.
	if dst, ok := ctx.resolveJSPackage("@acme/unknown"); ok || dst != "" {
		t.Fatalf("@acme/unknown should be dropped, got %q ok=%v", dst, ok)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
