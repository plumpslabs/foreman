package foreman

import (
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Import edges: the structural half of the signal graph. When an index
// refresh runs, every source file in the workspace is scanned for import
// statements and each import that resolves to another workspace file becomes
// a directional edge kind "import" (src imports dst, weight
// signalWeightImport).
//
// Unlike co_touch edges this needs no agent run at all, so even a freshly
// opened project starts with a meaningful graph (there is no cold start for
// structure). Resolution is a heuristic that fails closed: a specifier that
// cannot be pinned to exactly one workspace file is dropped, never guessed.

// importLinePatterns covers the shapes most codebases use. Precision comes
// from resolution failing closed — unmatched specifiers are simply skipped.
var importLinePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^\s*import\s+(?:type\s+)?(?:[^'"]*\sfrom\s+)?['"]([^'"]+)['"]`),
	regexp.MustCompile(`^\s*export\s+(?:type\s+)?[^'"]*from\s+['"]([^'"]+)['"]`),
	regexp.MustCompile(`^\s*(?:const|let|var)\s+\w+\s*=\s*require\(\s*['"]([^'"]+)['"]\s*\)`),
	regexp.MustCompile(`^\s*(?:import|await)\s+[^)]*import\(\s*['"]([^'"]+)['"]\s*\)`),
	regexp.MustCompile(`^\s*import\s+[\w\.\,\s]+\sfrom\s+['"]([^'"]+)['"]`),
	regexp.MustCompile(`^\s*import\s+(?:\w+\s*,\s*)?{?\s*([\w\.]+)\s*}?\s*\{?`), // python: import x
	regexp.MustCompile(`^\s*from\s+([\w\.]+)\s+import\s+`),                      // python: from x import y
	regexp.MustCompile(`^\s*(?:pub\s+)?mod\s+(\w+)\s*;`),                        // rust: mod x;
	regexp.MustCompile(`^\s*(?:pub\s+)?use\s+([\w:]+)\s*;`),                     // rust: use x::y;
}

// iteration order matters little; each file is scanned independently.

// importSource extracts the raw import specifiers of one source file. The
// default implementation scans lines, which is dependency-free but blind to
// import statements that wrap across lines. Builds with `-tags ts_symbols`
// swap in a tree-sitter extractor (see imports_treesitter.go) that reads the
// syntax tree instead.
type importSource interface {
	// Specifiers returns the specifiers found in src. The bool reports
	// whether the implementation understood this file at all; false means
	// "no grammar for this extension, or the file did not parse", and the
	// caller falls back to the line scanner.
	Specifiers(rel string, src []byte) ([]string, bool)
}

// lineImports is the default, dependency-free extractor.
type lineImports struct{}

func (lineImports) Specifiers(_ string, src []byte) ([]string, bool) {
	return lineImportSpecifiers(string(src)), true
}

// fileImports is the configured extractor. Default builds scan lines; the
// ts_symbols build replaces it via init().
var fileImports importSource = lineImports{}

// importsUseTreeSitter reports whether the opt-in syntax-tree extractor is
// compiled in. Tests use it to decide how strong an assertion to make about
// the configured extractor.
var importsUseTreeSitter bool

// tsImportExts lists the extensions tried, in order, when resolving a
// relative TypeScript/JavaScript specifier. The order mirrors TypeScript's own
// resolution: .ts, .tsx, .d.ts, then the JavaScript forms. Declaration files
// matter in practice — a `types.d.ts` imported as "@/ui/table/types" is a real
// edge, not a missing file.
var tsImportExts = []string{".ts", ".tsx", ".d.ts", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs",
	"/index.ts", "/index.tsx", "/index.mts", "/index.js", "/index.jsx",
	".py", ".rs", ".mod.rs"}

// isImportSrcFile reports whether rel can contain import statements worth
// scanning.
func isImportSrcFile(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".go", ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs", ".py", ".rs":
		return true
	}
	return false
}

// extractImports returns the workspace-relative files imported by ONE file.
// ctx carries the workspace's module boundaries (go.mod, package.json,
// tsconfig paths, Cargo.toml); a nil ctx means "single-root, no manifests".
func extractImports(root, rel string, ctx *importContext) []string {
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil || len(data) > 512<<10 {
		return nil
	}
	var spans []string
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".go":
		spans = goImportSpecifiers(string(data))
	default:
		specs, ok := fileImports.Specifiers(rel, data)
		if !ok {
			// The configured extractor has no grammar for this file or could
			// not parse it; the line scanner is always available.
			specs = lineImportSpecifiers(string(data))
		}
		spans = specs
	}
	dir := filepath.Dir(rel)
	seen := map[string]bool{}
	var out []string
	for _, spec := range spans {
		dst := ctx.resolve(rel, dir, spec)
		if dst == "" || seen[dst] {
			continue
		}
		seen[dst] = true
		out = append(out, dst)
	}
	return out
}

// resolve is the nil-safe entry point: a nil context behaves like a workspace
// with no manifests (relative paths and suffix matching only).
func (c *importContext) resolve(rel, dir, spec string) string {
	if c == nil {
		return resolveImportSpecifier(rel, dir, spec, "", nil)
	}
	return c.resolveSpecifier(rel, dir, spec)
}

// goImportSpecifiers uses the stdlib parser and keeps only imports that could
// be internal to the workspace (contain a slash, may be externals otherwise).
func goImportSpecifiers(src string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "src.go", src, parser.ImportsOnly)
	if err != nil {
		// Fall back to line scanning so a syntax error elsewhere in the file
		// does not hide its imports.
		return lineImportSpecifiers(src)
	}
	var out []string
	for _, imp := range f.Imports {
		out = append(out, strings.Trim(imp.Path.Value, `"`))
	}
	return out
}

// lineImportSpecifiers scans each line for an import-shaped pattern and
// returns the extracted specifier. Cheap and language-tolerant.
func lineImportSpecifiers(src string) []string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		for _, re := range importLinePatterns {
			if m := re.FindStringSubmatch(line); m != nil {
				spec := strings.TrimSpace(m[1])
				if spec == "" {
					continue
				}
				out = append(out, strings.Trim(spec, `"'`))
				break
			}
		}
	}
	return out
}

// trimExt drops the file extension (".ts", ".py", …) so suffix matching can
// compare an import path against "src/foo/bar.ts".
func trimExt(rel string) string {
	if i := strings.LastIndexByte(rel, '.'); i > 0 {
		return rel[:i]
	}
	return rel
}

// resolveImportSpecifier maps one specifier to a workspace-relative path (or
// "" when it cannot be an in-repo file):
//
//   - relative ./ ../ → direct file candidates next to the importer
//   - tsconfig-style aliases (@/, ~/) → suffix match against the file set
//   - Go module imports → module-name relative suffix ("mod/a/b" ↘ "a/b")
//   - python rust module paths → dotted/slash suffix match
//   - bare package names (npm/pip) → dropped (never guessed)
func resolveImportSpecifier(rel, dir, spec, module string, byRel map[string]bool) string {
	ctx := &importContext{byRel: byRel}
	if module != "" {
		ctx.goModules = []goModule{{Path: module}}
	}
	return ctx.resolveSpecifier(rel, dir, spec)
}

// resolveSpecifier is the monorepo-aware resolver. Order matters: the most
// specific manifest wins, and anything that stays ambiguous is dropped.
func (c *importContext) resolveSpecifier(rel, dir, spec string) string {
	byRel := c.byRel
	switch {
	case spec == "":
		return ""
	case strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../"):
		cand := filepath.ToSlash(filepath.Clean(filepath.Join(dir, spec)))
		return expandCandidate(withoutTSExt(cand), byRel)

	case strings.HasPrefix(spec, ".") && !strings.Contains(spec, "/"):
		// python relative import: one dot is the current package, each extra
		// dot walks up one directory.
		dots := 0
		for dots < len(spec) && spec[dots] == '.' {
			dots++
		}
		base := dir
		for i := 1; i < dots; i++ {
			base = filepath.Dir(base)
		}
		rest := strings.ReplaceAll(strings.TrimPrefix(spec[dots:], "/"), ".", "/")
		cand := filepath.ToSlash(filepath.Clean(filepath.Join(base, rest)))
		if dst := expandCandidate(cand, byRel); dst != "" {
			return dst
		}
		return matchBySuffix(cand, byRel)

	// Rust: crate/self/super are resolved relative to the declaring crate.
	case strings.HasPrefix(spec, "crate::") || strings.HasPrefix(spec, "self::") || strings.HasPrefix(spec, "super::"):
		return c.resolveRustPath(rel, dir, spec)
	case strings.Contains(spec, "::"):
		// A cargo workspace crate name claims the path before generic suffix
		// matching, so `use engine::rules` finds crates/engine/src/rules.rs
		// instead of the first rules.rs in the tree.
		if dst, ok := c.resolveRustCrate(spec); ok {
			return dst
		}
		return c.resolveRustPath(rel, dir, spec)
	case strings.HasPrefix(spec, "crate_"):
		return ""
	}

	// Rust `mod foo;` declares a sibling module file. The line scanner reports
	// it as a bare identifier, which the generic path would reject as a
	// package name, so resolve it against the importer's directory.
	if strings.EqualFold(filepath.Ext(rel), ".rs") && !strings.Contains(spec, "/") {
		if dst := expandCandidate(joinDir(dir, spec), byRel); dst != "" {
			return dst
		}
		if dst := expandCandidate(joinDir(joinDir(dir, spec), "mod"), byRel); dst != "" {
			return dst
		}
	}

	// Go: longest matching module prefix wins, so nested modules resolve even
	// when the workspace root has no go.mod.
	if dst, ok := c.resolveGoSpecifier(spec); ok {
		return dst
	}

	// JS/TS: a workspace package name (@acme/ui) maps to that package's dir.
	if dst, ok := c.resolveJSPackage(spec); ok {
		return dst
	}

	// tsconfig `paths` aliases, scoped to the importer's own package.
	if dst := c.resolveTSAlias(rel, spec); dst != "" {
		return dst
	}

	// Rust crate names: `use mycrate::foo` inside a cargo workspace.
	if dst, ok := c.resolveRustCrate(spec); ok {
		return dst
	}

	switch {
	case strings.HasPrefix(spec, "@"):
		return "" // npm scope package with no matching workspace package
	case strings.HasPrefix(spec, "@/") || strings.HasPrefix(spec, "~/"):
		return matchBySuffix(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(spec, "@/"), "~/"), ""), byRel)
	default:
		clean := strings.ReplaceAll(spec, ".", "/")
		// A bare single token is a package name, not a file; do not guess.
		if !strings.Contains(clean, "/") {
			return ""
		}
		return matchBySuffix(clean, byRel)
	}
}

// resolveGoSpecifier maps a Go import path through the module table. The bool
// reports whether a module claimed the specifier at all (even when the target
// file is missing), which keeps a nested module from being re-guessed as a
// Python-style dotted path.
func (c *importContext) resolveGoSpecifier(spec string) (string, bool) {
	for _, m := range c.goModules { // longest path first
		if m.Path == "" {
			continue
		}
		if spec == m.Path {
			return "", true // the module root itself: not a file
		}
		if !strings.HasPrefix(spec, m.Path+"/") {
			continue
		}
		rest := strings.TrimPrefix(spec, m.Path+"/")
		base := joinDir(m.Dir, rest)
		if dst := expandCandidate(base, c.byRel); dst != "" {
			return dst, true
		}
		// Go imports name a package DIRECTORY, not a file. Resolve to a
		// deterministic representative file inside it so the edge has a node.
		if dst := c.packageDirFile(base); dst != "" {
			return dst, true
		}
		if dst := matchBySuffix(base, c.byRel); dst != "" {
			return dst, true
		}
		return "", true // claimed by this module, but not a workspace file
	}
	return "", false
}

// packageDirFile picks the file that best represents a package directory.
// Go packages are directories, so an import edge has to point at one member;
// the non-test file with the shortest name is stable across runs and is
// usually the package's primary file (doc.go or the main type).
func (c *importContext) packageDirFile(dir string) string {
	if dir == "" {
		return ""
	}
	best := ""
	for rel := range c.byRel {
		if !strings.HasPrefix(rel, dir+"/") {
			continue
		}
		rest := strings.TrimPrefix(rel, dir+"/")
		if strings.Contains(rest, "/") || !strings.HasSuffix(rest, ".go") {
			continue
		}
		if strings.HasSuffix(rest, "_test.go") {
			continue
		}
		if best == "" || len(rest) < len(strings.TrimPrefix(best, dir+"/")) ||
			(len(rest) == len(strings.TrimPrefix(best, dir+"/")) && rel < best) {
			best = rel
		}
	}
	return best
}

// resolveJSPackage maps a bare specifier to a workspace package by its
// package.json name, longest name first (@acme/ui beats @acme).
func (c *importContext) resolveJSPackage(spec string) (string, bool) {
	for _, p := range c.jsPackages {
		if p.Name == "" {
			continue
		}
		if spec == p.Name {
			// Bare package import: resolve its declared entry point.
			return c.packageEntry(p), true
		}
		if !strings.HasPrefix(spec, p.Name+"/") {
			continue
		}
		rest := strings.TrimPrefix(spec, p.Name+"/")
		if dst := expandCandidate(joinDir(p.Dir, rest), c.byRel); dst != "" {
			return dst, true
		}
		return "", true
	}
	return "", false
}

// packageEntry resolves a package directory to a declared entry point, then
// falls back to conventional targets.
func (c *importContext) packageEntry(pkg jsPackage) string {
	for _, entry := range pkg.Entries {
		if dst := expandCandidate(withoutTSExt(entry), c.byRel); dst != "" {
			return dst
		}
	}
	for _, cand := range []string{"src/index", "index", "src/main", "main"} {
		if dst := expandCandidate(joinDir(pkg.Dir, cand), c.byRel); dst != "" {
			return dst
		}
	}
	return ""
}

// resolveTSAlias applies the nearest tsconfig `paths` entries that are
// ancestors of the importer. Scoping by directory is what keeps two apps from
// stealing each other's "@/*" alias.
func (c *importContext) resolveTSAlias(rel, spec string) string {
	for _, a := range c.tsAliases { // deepest dir first
		if !isAncestorDir(a.Dir, rel) {
			continue
		}
		captured, ok := matchTSPattern(a.Pattern, spec)
		if !ok {
			continue
		}
		for _, target := range a.Targets {
			// Substitute the wildcard, then anchor on baseUrl when present.
			filled := target
			if strings.Contains(target, "*") {
				filled = strings.Replace(target, "*", captured, 1)
			}
			base := a.Dir
			if a.BaseURL != "" {
				base = a.BaseURL
			}
			cand := joinDir(base, strings.TrimPrefix(filled, "./"))
			if dst := expandCandidate(cand, c.byRel); dst != "" {
				return dst
			}
			if dst := matchBySuffix(cand, c.byRel); dst != "" {
				return dst
			}
		}
		return "" // matched an alias but nothing resolved: do not guess further
	}
	return ""
}

// resolveRustPath handles crate::/self::/super:: inside a cargo workspace by
// anchoring on the crate that contains the importer.
func (c *importContext) resolveRustPath(rel, dir, spec string) string {
	crateDir := c.rustCrateDirFor(rel)
	trimmed := spec
	switch {
	case strings.HasPrefix(spec, "crate::"):
		trimmed = strings.TrimPrefix(spec, "crate::")
		// crate:: = the crate's own src/ directory.
		base := joinDir(crateDir, "src")
		p := strings.ReplaceAll(trimmed, "::", "/")
		if dst := expandCandidate(joinDir(base, p), c.byRel); dst != "" {
			return dst
		}
		return matchBySuffix(joinDir(base, p), c.byRel)
	case strings.HasPrefix(spec, "self::"):
		trimmed = strings.TrimPrefix(spec, "self::")
		p := strings.ReplaceAll(trimmed, "::", "/")
		if dst := expandCandidate(joinDir(dir, p), c.byRel); dst != "" {
			return dst
		}
		return matchBySuffix(joinDir(dir, p), c.byRel)
	case strings.HasPrefix(spec, "super::"):
		trimmed = strings.TrimPrefix(spec, "super::")
		p := strings.ReplaceAll(trimmed, "::", "/")
		parent := filepath.ToSlash(filepath.Dir(dir))
		if dst := expandCandidate(joinDir(parent, p), c.byRel); dst != "" {
			return dst
		}
		return matchBySuffix(joinDir(parent, p), c.byRel)
	}
	// Plain path: fall back to suffix matching, but prefer the importer's own
	// crate directory so sibling crates cannot shadow each other.
	p := strings.ReplaceAll(spec, "::", "/")
	if crateDir != "" {
		if dst := matchBySuffix(joinDir(crateDir, "src"), c.byRel); dst != "" && strings.HasSuffix(trimExt(dst), "/"+p) {
			return dst
		}
	}
	return matchBySuffix(p, c.byRel)
}

// rustCrateDirFor returns the directory of the cargo package containing rel.
func (c *importContext) rustCrateDirFor(rel string) string {
	best := ""
	for _, cr := range c.rustCrates {
		if cr.Dir == "" || isAncestorDir(cr.Dir, rel) {
			if len(cr.Dir) >= len(best) {
				best = cr.Dir
			}
		}
	}
	return best
}

// resolveRustCrate maps `use mycrate::foo` to that crate's src directory.
func (c *importContext) resolveRustCrate(spec string) (string, bool) {
	for _, cr := range c.rustCrates { // longest name first
		if cr.Name == "" {
			continue
		}
		if spec == cr.Name {
			return "", true
		}
		if !strings.HasPrefix(spec, cr.Name+"::") {
			continue
		}
		rest := strings.ReplaceAll(strings.TrimPrefix(spec, cr.Name+"::"), "::", "/")
		base := joinDir(joinDir(cr.Dir, "src"), rest)
		if dst := expandCandidate(base, c.byRel); dst != "" {
			return dst, true
		}
		if dst := matchBySuffix(base, c.byRel); dst != "" {
			return dst, true
		}
		return "", true
	}
	return "", false
}

// withoutTSExt drops a trailing TS/JS/Py/Rs extension if present. A dot in the
// basename (ticket.constants) is NOT an extension, so unknown suffixes are
// returned untouched — expandCandidate then tries the real file set.
func withoutTSExt(p string) string {
	// Longest extension first so ".d.ts" is stripped whole, never leaving a
	// dangling ".d".
	exts := append([]string(nil), tsImportExts...)
	sort.SliceStable(exts, func(i, j int) bool { return len(exts[i]) > len(exts[j]) })
	for _, e := range exts {
		if strings.Contains(e, "/index") {
			continue
		}
		if strings.HasSuffix(p, e) {
			return strings.TrimSuffix(p, e)
		}
	}
	return p
}

// expandCandidate tries the exact path plus every candidate extension and
// index form against the real workspace file set.
func expandCandidate(base string, byRel map[string]bool) string {
	if byRel[base] {
		return base
	}
	for _, e := range tsImportExts {
		if strings.Contains(e, "/index") {
			continue
		}
		if byRel[base+e] {
			return base + e
		}
	}
	for _, e := range tsImportExts {
		if !strings.Contains(e, "/index") {
			continue
		}
		if byRel[base+e] {
			return base + e
		}
	}
	// A ".js" specifier may name a TypeScript source: NodeNext ESM requires
	// the emitted extension in the import, so `import "./x.js"` is how real
	// projects reference x.ts. Rewrite the extension and retry once.
	if trimmed := withoutTSExt(base); trimmed != base {
		for _, e := range tsImportExts {
			if strings.Contains(e, "/index") {
				continue
			}
			if byRel[trimmed+e] {
				return trimmed + e
			}
		}
	}
	return ""
}

// matchBySuffix finds the unique source file whose path (sans extension)
// ends with the given suffix ("tickets/mutation" ↔
// backend/src/services/tickets/mutation.ts). Ambiguous matches are "no result"
// rather than a guess.
func matchBySuffix(suffix string, byRel map[string]bool) string {
	suffix = strings.Trim(suffix, "/")
	if suffix == "" {
		return ""
	}
	var matches []string
	for rel := range byRel {
		base := trimExt(rel)
		if base == suffix || strings.HasSuffix(base, "/"+suffix) {
			matches = append(matches, rel)
		}
	}
	// Prefer exact base match over folder/index matches.
	if len(matches) > 1 {
		for _, m := range matches {
			if trimExt(m) == suffix {
				return m
			}
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return "" // zero or ambiguous
}

// readGoModuleName extracts the module directive of root/go.mod ("" when go
// is not a workspace or parsing fails). Cached per refresh by callers.
func readGoModuleName(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) >= 2 && f[0] == "module" {
			return f[1]
		}
	}
	return ""
}

// refreshSignalImports rebuilds the whole "import" edge set from a fresh
// filesystem walk. Full replace (not upsert) keeps deleted/renamed imports
// from lingering. Bounded and best-effort; never fails indexing.
func (s *Store) refreshSignalImports(root string) {
	db := s.DB()
	if db == nil {
		return
	}
	byRel := map[string]bool{}
	names := make([]string, 0, 128)
	_ = walkProject(root, func(path, rel string, _ os.FileInfo) error {
		if isImportSrcFile(rel) {
			byRel[rel] = true
			names = append(names, rel)
		}
		return nil
	})
	sort.Strings(names)
	if len(names) > signalsMaxImportFiles {
		names = names[:signalsMaxImportFiles]
	}
	// Module boundaries (go.mod/go.work, package.json, tsconfig paths,
	// Cargo.toml) are discovered once per refresh, not per file.
	ctx := newImportContext(root, byRel)
	edges := make([]SignalEdgeDelta, 0, 64)
	for _, rel := range names {
		if len(edges) >= signalMaxImportEdges {
			break
		}
		for _, dst := range extractImports(root, rel, ctx) {
			if dst == rel {
				continue
			}
			edges = append(edges, SignalEdgeDelta{Kind: "import", Src: rel, Dst: dst, Weight: signalWeightImport})
			if len(edges) >= signalMaxImportEdges {
				break
			}
		}
	}
	if err := db.ReplaceSignalImports(edges); err != nil {
		slog.Warn("signal import refresh failed", "err", err)
	}
}

// ReplaceSignalImports atomically swaps the "import" edge set for edges. It
// is the graph's only kind derived from disk (usage edges never go here).
func (d *DB) ReplaceSignalImports(edges []SignalEdgeDelta) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM signals_edges WHERE kind = 'import'`); err != nil {
		return err
	}
	if len(edges) > 0 {
		now := time.Now().UTC()
		stmt, err := tx.Prepare(`INSERT OR REPLACE INTO signals_edges (kind, src, dst, weight, hits, last_seen)
			VALUES('import', ?, ?, ?, 1, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, e := range edges {
			if e.Src == "" || e.Dst == "" {
				continue
			}
			if _, err := stmt.Exec(e.Src, e.Dst, e.Weight, now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
