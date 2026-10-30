package foreman

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var srcExt = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".mjs": true, ".cjs": true, ".py": true, ".rs": true, ".java": true,
	".kt": true, ".c": true, ".h": true, ".cpp": true, ".hpp": true,
	".cc": true, ".cs": true, ".php": true, ".rb": true, ".swift": true,
}

var (
	reDefLine   = regexp.MustCompile(`^\s*(?:(?:pub|public|private|protected|export|default|async|static|inline|template|final|abstract)\s+)*(?:func|function|fn|fun|class|struct|enum|trait|interface|type|def|impl|const|let|var)\s+([A-Za-z_]\w*)`)
	reGoMethod  = regexp.MustCompile(`^\s*func\s+\([^)]*\)\s+([A-Za-z_]\w*)`)
	reCleanTail = regexp.MustCompile(`[\s{(\[=,:]+$`)
)

func symbolizeLine(line string) string {
	if m := reGoMethod.FindStringSubmatch(line); m != nil {
		return "func " + m[1] + "(…)"
	}
	if m := reDefLine.FindStringSubmatch(line); m != nil {
		s := strings.TrimSpace(line)
		s = reCleanTail.ReplaceAllString(s, "")
		s = strings.Join(strings.Fields(s), " ")
		if len(s) > 120 {
			s = s[:120] + "…"
		}
		return s
	}
	return ""
}

func isSourceFile(rel string) bool {
	return srcExt[strings.ToLower(filepath.Ext(rel))]
}

// buildRepoMap produces a small, token-friendly map of source files and the
// symbols they define (Aider-style repo map, heuristic symbol extraction).
func buildRepoMap(root string, maxChars int) map[string]any {
	limit := maxChars
	if limit <= 0 {
		limit = 2400
	}
	if limit > 12000 {
		limit = 12000
	}
	type fileEntry struct {
		rel     string
		symbols []string
	}
	var entries []fileEntry
	_ = walkProject(root, func(path, rel string, info os.FileInfo) error {
		if !isSourceFile(rel) {
			return nil
		}
		if info.Size() > 512<<10 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		syms := fileSymbols(rel, data, 12)
		if len(syms) > 0 {
			entries = append(entries, fileEntry{rel: rel, symbols: syms})
		}
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	var b strings.Builder
	totalSymbols := 0
	truncated := false
	for _, e := range entries {
		if b.Len() > limit {
			truncated = true
			break
		}
		b.WriteString(e.rel + ":\n")
		for _, s := range e.symbols {
			line := "  " + s + "\n"
			if b.Len()+len(line) > limit {
				truncated = true
				break
			}
			b.WriteString(line)
			totalSymbols++
		}
	}
	return map[string]any{
		"map":       b.String(),
		"files":     len(entries),
		"symbols":   totalSymbols,
		"truncated": truncated,
	}
}
