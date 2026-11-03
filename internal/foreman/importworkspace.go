package foreman

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Monorepo support.
//
// The single-root assumption (one go.mod, one "@/" alias, one crate) is true
// for a standalone repo and false for every monorepo. importContext is built
// once per index refresh by scanning for the manifests that define module
// boundaries, then resolution consults it:
//
//   - Go: every go.mod in the tree, plus go.work `use` directives. A specifier
//     resolves against the module whose path is its longest prefix, so
//     github.com/acme/worker/pkg/job lands in services/worker/ even though the
//     workspace root has no go.mod at all.
//   - JS/TS: package.json `name` fields map a bare specifier (@acme/ui) to a
//     workspace package directory, and each package's own tsconfig `paths`
//     alias is scoped to that package — apps/web and apps/admin can both define
//     "@/*" pointing at different directories without stealing each other's
//     imports.
//   - Rust: Cargo.toml package names make `use mycrate::foo` resolvable in a
//     cargo workspace.
//   - Python: absolute imports are first tried inside the importer's own
//     package root, so two apps with app/models.py no longer make each other
//     ambiguous.
//
// Everything still fails closed: a specifier that does not pin to exactly one
// file is dropped.

// goModule is one Go module boundary.
type goModule struct {
	Path string // module directive, e.g. github.com/acme/worker
	Dir  string // workspace-relative directory containing go.mod ("" for root)
}

// jsPackage maps a package.json `name` to its workspace-relative directory,
// plus the entry points it declares.
type jsPackage struct {
	Name    string
	Dir     string
	Entries []string // main/module/types targets, workspace-relative
}

// tsAlias is one tsconfig `paths` entry, scoped to the tsconfig that declared
// it. Pattern may contain a single "*" wildcard.
type tsAlias struct {
	Dir     string   // workspace-relative dir of the tsconfig ("" for root)
	Pattern string   // e.g. "@/*"
	Targets []string // e.g. ["./src/*"]
	BaseURL string   // workspace-relative baseUrl, when set
}

// rustCrate is one Cargo.toml package.
type rustCrate struct {
	Name string
	Dir  string
}

// importContext is the per-refresh resolution environment.
type importContext struct {
	byRel      map[string]bool
	goModules  []goModule  // longest Path first
	jsPackages []jsPackage // longest Name first
	tsAliases  []tsAlias   // deepest Dir first
	rustCrates []rustCrate // longest Name first
}

// newImportContext scans root for module manifests. Best-effort: a malformed
// manifest contributes nothing rather than failing the refresh.
func newImportContext(root string, byRel map[string]bool) *importContext {
	ctx := &importContext{byRel: byRel}

	// Collect manifest paths first; walkProject already honours ignores.
	var goMods, pkgJSONs, tsConfigs, cargoTomls []string
	_ = walkProject(root, func(abs, rel string, info os.FileInfo) error {
		base := filepath.Base(rel)
		switch {
		case base == "go.mod":
			goMods = append(goMods, rel)
		case base == "package.json":
			pkgJSONs = append(pkgJSONs, rel)
		case base == "Cargo.toml":
			cargoTomls = append(cargoTomls, rel)
		case isTSConfigName(base):
			// Vite and similar toolchains split config into
			// tsconfig.app.json / tsconfig.node.json, so matching only
			// "tsconfig.json" silently loses the "@/*" alias.
			tsConfigs = append(tsConfigs, rel)
		}
		return nil
	})

	for _, rel := range goMods {
		if mod := readGoModAt(root, rel); mod.Path != "" {
			ctx.goModules = append(ctx.goModules, mod)
		}
	}
	// go.work can list modules that live outside the walked tree, but its
	// `use` entries also tell us about nested modules that do have go.mod.
	for _, extra := range readGoWorkModules(root) {
		found := false
		for _, m := range ctx.goModules {
			if m.Path == extra.Path {
				found = true
				break
			}
		}
		if !found {
			ctx.goModules = append(ctx.goModules, extra)
		}
	}
	sort.SliceStable(ctx.goModules, func(i, j int) bool {
		return len(ctx.goModules[i].Path) > len(ctx.goModules[j].Path)
	})

	for _, rel := range pkgJSONs {
		pkg := readJSPackage(filepath.Join(root, rel), filepath.ToSlash(filepath.Dir(rel)))
		if pkg.Name != "" {
			ctx.jsPackages = append(ctx.jsPackages, pkg)
		}
	}
	sort.SliceStable(ctx.jsPackages, func(i, j int) bool {
		return len(ctx.jsPackages[i].Name) > len(ctx.jsPackages[j].Name)
	})

	for _, rel := range tsConfigs {
		ctx.tsAliases = append(ctx.tsAliases, readTSConfigAliases(root, rel)...)
	}
	// Deepest tsconfig wins, so a package-local alias shadows a root one.
	sort.SliceStable(ctx.tsAliases, func(i, j int) bool {
		return pathDepth(ctx.tsAliases[i].Dir) > pathDepth(ctx.tsAliases[j].Dir)
	})

	for _, rel := range cargoTomls {
		if name := readCargoPackageName(filepath.Join(root, rel)); name != "" {
			ctx.rustCrates = append(ctx.rustCrates, rustCrate{
				Name: name,
				Dir:  filepath.ToSlash(filepath.Dir(rel)),
			})
		}
	}
	sort.SliceStable(ctx.rustCrates, func(i, j int) bool {
		return len(ctx.rustCrates[i].Name) > len(ctx.rustCrates[j].Name)
	})

	return ctx
}

// isTSConfigName reports whether base declares TypeScript/JavaScript path
// aliases. It covers tsconfig.json plus the split configs (tsconfig.app.json,
// tsconfig.node.json, tsconfig.base.json) and jsconfig.json, which plain
// JavaScript projects use for the same "paths" mapping. Missing jsconfig
// silently drops every "@/" import in a JS backend.
func isTSConfigName(base string) bool {
	if !strings.HasSuffix(base, ".json") {
		return false
	}
	return strings.HasPrefix(base, "tsconfig") || strings.HasPrefix(base, "jsconfig")
}

// pathDepth counts path separators; "" and "." are depth 0.
func pathDepth(rel string) int {
	rel = strings.Trim(rel, "/")
	if rel == "" || rel == "." {
		return 0
	}
	return strings.Count(rel, "/") + 1
}

// joinDir joins a workspace-relative directory with a relative fragment,
// treating "" as the workspace root.
func joinDir(dir, frag string) string {
	if dir == "" || dir == "." {
		return filepath.ToSlash(filepath.Clean(frag))
	}
	return filepath.ToSlash(filepath.Clean(filepath.Join(dir, frag)))
}

// readGoModAt reads the module directive of one go.mod.
func readGoModAt(root, rel string) goModule {
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return goModule{}
	}
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." {
		dir = ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) >= 2 && f[0] == "module" {
			return goModule{Path: f[1], Dir: dir}
		}
	}
	return goModule{}
}

// readGoWorkModules reads the `use` block of a root go.work and returns the
// modules declared there (each resolved via its own go.mod when present).
func readGoWorkModules(root string) []goModule {
	data, err := os.ReadFile(filepath.Join(root, "go.work"))
	if err != nil {
		return nil
	}
	var out []goModule
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		// Accept `use ./a`, `use ( ./a )` and bare entries inside a use block.
		line = strings.TrimPrefix(line, "use")
		line = strings.TrimSpace(strings.Trim(line, "()"))
		if line == "" || line == "(" || line == ")" || strings.HasPrefix(line, "go ") {
			continue
		}
		dir := strings.Trim(strings.TrimSpace(line), `"`)
		if dir == "" || strings.HasPrefix(dir, ".") && dir == "." {
			continue
		}
		rel := filepath.ToSlash(filepath.Clean(dir))
		if rel == "." {
			rel = ""
		}
		if mod := readGoModAt(root, joinDir(rel, "go.mod")); mod.Path != "" {
			out = append(out, mod)
		}
	}
	return out
}

// readJSPackage reads the name and declared entry points of one package.json.
func readJSPackage(abs, dir string) jsPackage {
	data, err := os.ReadFile(abs)
	if err != nil {
		return jsPackage{}
	}
	var doc struct {
		Name    string          `json:"name"`
		Main    string          `json:"main"`
		Module  string          `json:"module"`
		Types   string          `json:"types"`
		Typings string          `json:"typings"`
		Exports json.RawMessage `json:"exports"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return jsPackage{}
	}
	pkg := jsPackage{Name: strings.TrimSpace(doc.Name), Dir: dir}
	seen := map[string]bool{}
	addEntry := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		pkg.Entries = append(pkg.Entries, joinDir(dir, strings.TrimPrefix(p, "./")))
	}
	// exports may be a string, an object of conditions, or a map of subpaths.
	var asString string
	if err := json.Unmarshal(doc.Exports, &asString); err == nil {
		addEntry(asString)
	} else {
		var asMap map[string]json.RawMessage
		if err := json.Unmarshal(doc.Exports, &asMap); err == nil {
			// Prefer the "." subpath; fall back to any string value.
			if dot, ok := asMap["."]; ok {
				var s string
				if err := json.Unmarshal(dot, &s); err == nil {
					addEntry(s)
				} else {
					var cond map[string]string
					if err := json.Unmarshal(dot, &cond); err == nil {
						for _, k := range []string{"import", "require", "default", "module", "types"} {
							addEntry(cond[k])
						}
					}
				}
			}
		}
	}
	for _, p := range []string{doc.Module, doc.Main, doc.Types, doc.Typings} {
		addEntry(p)
	}
	return pkg
}

// readJSONStringField returns one top-level string field of a JSON file, or ""
// when the file is absent or unparseable.
func readJSONStringField(abs, field string) string {
	data, err := os.ReadFile(abs)
	if err != nil {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	if s, ok := m[field].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// jsoncComment matches // and /* */ comments so tsconfig.json (which permits
// them) can be decoded with encoding/json.
var jsoncComment = regexp.MustCompile(`(?m)(^|[^:])//.*$|/\*[\s\S]*?\*/`)

// readTSConfigAliases extracts compilerOptions.paths from one tsconfig.json.
func readTSConfigAliases(root, rel string) []tsAlias {
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return nil
	}
	cleaned := jsoncComment.ReplaceAll(data, []byte("$1"))
	// Trailing commas are also legal in tsconfig.
	cleaned = regexp.MustCompile(`,\s*([}\]])`).ReplaceAll(cleaned, []byte("$1"))

	var doc struct {
		CompilerOptions struct {
			BaseURL string              `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if err := json.Unmarshal(cleaned, &doc); err != nil {
		return nil
	}
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." {
		dir = ""
	}
	baseURL := ""
	if doc.CompilerOptions.BaseURL != "" {
		baseURL = joinDir(dir, doc.CompilerOptions.BaseURL)
	}
	var out []tsAlias
	for pattern, targets := range doc.CompilerOptions.Paths {
		if pattern == "" || len(targets) == 0 {
			continue
		}
		out = append(out, tsAlias{Dir: dir, Pattern: pattern, Targets: targets, BaseURL: baseURL})
	}
	return out
}

// readCargoPackageName reads [package] name from a Cargo.toml without pulling
// in a TOML dependency: the field is a single line in every real manifest.
func readCargoPackageName(abs string) string {
	data, err := os.ReadFile(abs)
	if err != nil {
		return ""
	}
	inPackage := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inPackage = strings.TrimSpace(trimmed) == "[package]"
			continue
		}
		if !inPackage || !strings.HasPrefix(trimmed, "name") {
			continue
		}
		if _, val, ok := strings.Cut(trimmed, "="); ok {
			return strings.Trim(strings.TrimSpace(val), `"'`)
		}
	}
	return ""
}

// matchTSPattern applies one tsconfig `paths` pattern to a specifier,
// returning the captured wildcard (and whether it matched).
func matchTSPattern(pattern, spec string) (string, bool) {
	star := strings.IndexByte(pattern, '*')
	if star < 0 {
		return "", pattern == spec
	}
	prefix, suffix := pattern[:star], pattern[star+1:]
	if !strings.HasPrefix(spec, prefix) || !strings.HasSuffix(spec, suffix) {
		return "", false
	}
	if len(spec) < len(prefix)+len(suffix) {
		return "", false
	}
	return spec[len(prefix) : len(spec)-len(suffix)], true
}

// isAncestorDir reports whether dir is ancestor (or equal) of rel's directory.
func isAncestorDir(dir, rel string) bool {
	if dir == "" {
		return true // workspace root
	}
	return rel == dir || strings.HasPrefix(rel, dir+"/")
}
