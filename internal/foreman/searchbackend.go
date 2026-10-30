package foreman

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// rgAvailable reports whether the ripgrep binary is on PATH.
func rgAvailable() bool {
	_, err := exec.LookPath("rg")
	return err == nil
}

// rgGrep searches root with ripgrep when available. It returns the same
// result shape as the Go fallback. Context lines (before/after) are handled
// by the Go backend because rg's context output doesn't carry per-line
// numbers under --no-heading.
func rgGrep(root string, args grepFilesArgs) (map[string]any, error) {
	max := args.MaxMatches
	if max <= 0 {
		max = 100
	}
	if max > 500 {
		max = 500
	}
	rargs := []string{"-n", "--column", "--no-heading", "--color", "never", "--max-count", strconv.Itoa(max)}
	if args.CaseInsensitive {
		rargs = append(rargs, "-i")
	}
	rargs = append(rargs, "-e", args.Pattern)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "rg", rargs...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		// rg exits 1 when no matches; that's fine.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !strings.Contains(err.Error(), "exit status 1") {
			return map[string]any{"matches": []map[string]any{}, "count": 0, "error": err.Error()}, nil
		}
	}
	var matches []map[string]any
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		// path:line:col:text  (path may contain ':' — split from the right)
		idx1 := strings.LastIndexByte(line, ':')
		if idx1 < 0 {
			continue
		}
		idx2 := strings.LastIndexByte(line[:idx1], ':')
		if idx2 < 0 {
			continue
		}
		idx3 := strings.LastIndexByte(line[:idx2], ':')
		if idx3 < 0 {
			continue
		}
		path := line[:idx3]
		ln, err1 := strconv.Atoi(line[idx3+1 : idx2])
		col, err2 := strconv.Atoi(line[idx2+1 : idx1])
		text := line[idx1+1:]
		if err1 != nil || err2 != nil {
			continue
		}
		matches = append(matches, map[string]any{
			"file": path, "line": ln, "column": col,
			"lines": []map[string]any{{"text": text}},
		})
		if len(matches) >= max {
			break
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a["file"] != b["file"] {
			return a["file"].(string) < b["file"].(string)
		}
		return a["line"].(int) < b["line"].(int)
	})
	return map[string]any{"matches": matches, "count": len(matches)}, nil
}

// rgGlob lists files with ripgrep's --files -g when available.
func rgGlob(root, pattern string) (map[string]any, error) {
	if pattern == "" {
		pattern = "**/*"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "rg", "--files", "-g", pattern)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var files []string
	for _, f := range strings.Split(string(out), "\n") {
		if f != "" {
			files = append(files, f)
		}
		if len(files) >= 500 {
			break
		}
	}
	sort.Strings(files)
	return map[string]any{"files": files, "count": len(files)}, nil
}

var _ = fmt.Sprintf
