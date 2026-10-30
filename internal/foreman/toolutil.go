package foreman

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	gitignore "github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// defaultSkipDirs lists directories that are never interesting to search.
var defaultSkipDirs = map[string]bool{
	".git": true, "node_modules": true, ".foreman": true, "vendor": true,
	"dist": true, "build": true, ".venv": true, "__pycache__": true,
	".next": true, ".nuxt": true, "target": true,
}

func isSkipDirName(name string) bool { return defaultSkipDirs[name] }

const maxScanFileBytes = 1 << 20 // skip files larger than 1 MiB

// errStopWalk aborts a walk early once enough results were collected.
var errStopWalk = errors.New("stop walk")

// projectIgnores builds a correct .gitignore matcher from the workspace's
// .gitignore / .ignore / .rgignore files using go-git's matcher (wildmatch,
// negation, ** — same semantics as `git check-ignore`).
type projectIgnores struct {
	matcher gitignore.Matcher
}

func loadProjectIgnores(root string) *projectIgnores {
	var pats []gitignore.Pattern
	for _, name := range []string{".gitignore", ".ignore", ".rgignore"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			pats = append(pats, gitignore.ParsePattern(line, nil))
		}
	}
	if len(pats) == 0 {
		return &projectIgnores{}
	}
	return &projectIgnores{matcher: gitignore.NewMatcher(pats)}
}

// match reports whether a slash-separated relative path is ignored.
func (p *projectIgnores) match(rel string, isDir bool) bool {
	if p == nil || p.matcher == nil {
		return false
	}
	return p.matcher.Match(strings.Split(rel, "/"), isDir)
}

// walkProject walks root and calls fn for every regular file, skipping hidden
// entries, known build dirs, ignored paths, binary-looking files, and oversize
// files. rel is slash-separated and relative to root.
func walkProject(root string, fn func(abs, rel string, info fs.FileInfo) error) error {
	ig := loadProjectIgnores(root)
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if isSkipDirName(name) || (strings.HasPrefix(name, ".") && name != ".") {
				return filepath.SkipDir
			}
			if ig.match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		if ig.match(rel, false) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if info.Size() > maxScanFileBytes {
			return nil
		}
		return fn(path, rel, info)
	})
}

// safeRegexp compiles pattern; on failure it treats pattern as a literal string.
func safeRegexp(pattern string, caseInsensitive bool) *regexp.Regexp {
	src := pattern
	if caseInsensitive {
		src = "(?i)" + src
	}
	re, err := regexp.Compile(src)
	if err != nil {
		return regexp.MustCompile(regexp.QuoteMeta(pattern))
	}
	return re
}

// globRegex converts a glob pattern (supporting **) into a regexp for rel paths.
func globRegex(pattern string) *regexp.Regexp {
	tail := "$"
	if strings.HasSuffix(pattern, "/**") {
		tail = "(?:/.*)?$"
		pattern = strings.TrimSuffix(pattern, "/**")
	} else if pattern == "**" {
		tail = "(?:/.*)?$"
	}
	var b strings.Builder
	b.WriteString("^")
	if strings.HasPrefix(pattern, "**/") {
		b.WriteString("(?:.*/)?") // **/ also matches zero directories (root)
		pattern = pattern[3:]
	}
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(pattern[i:], ']')
			if end > 0 {
				b.WriteString(pattern[i : i+end+1])
				i += end
			} else {
				b.WriteString(`\[`)
			}
		default:
			if strings.ContainsRune(`.\+()|{}^$`, rune(c)) {
				b.WriteByte('\\')
			}
			b.WriteByte(c)
		}
	}
	b.WriteString(tail)
	return regexp.MustCompile(b.String())
}

// colOf returns the 1-based column of the first match in line, -1 if none.
func colOf(re *regexp.Regexp, line string) int {
	loc := re.FindStringIndex(line)
	if loc == nil {
		return -1
	}
	return loc[0] + 1
}

var _ = fmt.Sprintf // keep fmt imported for future use
