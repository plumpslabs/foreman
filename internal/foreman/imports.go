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

// tsImportExts lists the extensions tried, in order, when resolving a
// relative TypeScript/JavaScript specifier.
var tsImportExts = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs",
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
// `module` is the go.mod module name ("" when unknown); specifiers that
// cannot be resolved inside the workspace are dropped.
func extractImports(root, rel, module string, byRel map[string]bool) []string {
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil || len(data) > 512<<10 {
		return nil
	}
	var spans []string
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".go":
		spans = goImportSpecifiers(string(data))
	default:
		spans = lineImportSpecifiers(string(data))
	}
	dir := filepath.Dir(rel)
	seen := map[string]bool{}
	var out []string
	for _, spec := range spans {
		dst := resolveImportSpecifier(rel, dir, spec, module, byRel)
		if dst == "" || seen[dst] {
			continue
		}
		seen[dst] = true
		out = append(out, dst)
	}
	return out
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
	switch {
	case strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../"):
		cand := filepath.ToSlash(filepath.Clean(filepath.Join(dir, spec)))
		return expandCandidate(withoutTSExt(cand), byRel)
	case strings.HasPrefix(spec, "@/") || strings.HasPrefix(spec, "~/"):
		return matchBySuffix(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(spec, "@/"), "~/"), ""), byRel)
	case strings.HasPrefix(spec, "@"):
		return "" // npm scope package: external
	case strings.HasPrefix(spec, "crate::") || strings.HasPrefix(spec, "self::") || strings.HasPrefix(spec, "super::"):
		p := strings.TrimPrefix(spec, "crate::")
		p = strings.TrimPrefix(strings.TrimPrefix(p, "self::"), "super::")
		return matchBySuffix(strings.ReplaceAll(p, "::", "/"), byRel)
	case module != "" && strings.HasPrefix(spec, module+"/"):
		return matchBySuffix(strings.TrimPrefix(spec, module+"/"), byRel)
	case module != "" && spec == module:
		return ""
	case strings.Contains(spec, "::"):
		return matchBySuffix(strings.ReplaceAll(spec, "::", "/"), byRel)
	case strings.HasPrefix(spec, "crate_"):
		return ""
	default:
		clean := strings.ReplaceAll(spec, ".", "/")
		// A bare single token is a package name, not a file; do not guess.
		if !strings.Contains(clean, "/") {
			return ""
		}
		return matchBySuffix(clean, byRel)
	}
}

// withoutTSExt drops a trailing TS/JS/Py/Rs extension if present. A dot in the
// basename (ticket.constants) is NOT an extension, so unknown suffixes are
// returned untouched — expandCandidate then tries the real file set.
func withoutTSExt(p string) string {
	for _, e := range tsImportExts {
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
	module := readGoModuleName(root)
	edges := make([]SignalEdgeDelta, 0, 64)
	for _, rel := range names {
		if len(edges) >= signalMaxImportEdges {
			break
		}
		for _, dst := range extractImports(root, rel, module, byRel) {
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
