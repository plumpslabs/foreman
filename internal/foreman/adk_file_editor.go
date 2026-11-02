package foreman

import (
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool/functiontool"
)

type fileEditorArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Content   string `json:"content,omitempty"`
	OldStr    string `json:"old_str,omitempty"`
	NewStr    string `json:"new_str,omitempty"`
	Line      int    `json:"line,omitempty"`
	Text      string `json:"text,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	// WithHashes renders each view_file line with a content-hash anchor
	// (line#hash) that edit_file consumes. Off by default to keep view
	// output cheap; agents request it when they intend to edit.
	WithHashes bool `json:"with_hashes,omitempty"`
}

func safePath(workspace, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("path is required")
	}
	abs := p
	if !filepath.IsAbs(p) {
		abs = filepath.Join(workspace, p)
	}
	clean := filepath.Clean(abs)
	if workspace != "" {
		rel, err := filepath.Rel(workspace, clean)
		if err != nil {
			return "", fmt.Errorf("path escapes workspace: %s", p)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("path escapes workspace: %s", p)
		}
	}
	return clean, nil
}

// fileLocks serializes read-modify-write per absolute path across rooms so two
// concurrent agents editing the same file cannot silently overwrite each other.
var fileLocks sync.Map // path string -> *sync.Mutex

func lockPath(path string) func() {
	mu, _ := fileLocks.LoadOrStore(path, &sync.Mutex{})
	m := mu.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

// writeFileWithDiff writes content and emits a diff event when the tool
// context has an emit callback. Diffs are cumulative: the first edit of a
// file records the pre-run content, later edits diff against that origin so
// the room's Changes tab shows one up-to-date diff per file.
//
// After writing, it normalizes the file's formatting when a language-specific
// formatter is available (Go via go/format, zero-dependency; other languages
// via an optional external formatter if present). This is the "feedback loop"
// that makes edits land cleanly: the agent edits a region, and Foreman leaves
// the file gofmt-clean so the next read (and the build) is not polluted by
// stray formatting. A format failure never rejects the edit — the original
// write stands.
func writeFileWithDiff(tc adkToolContext, workspace, path, content string) (bool, error) {
	unlock := lockPath(path)
	defer unlock()
	old := ""
	if data, err := os.ReadFile(path); err == nil {
		old = string(data)
	}
	if old == content {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return false, err
	}
	// Normalize formatting in place when possible, best-effort.
	if formatted, ferr := formatOnWrite(path, content); ferr == nil && formatted != content {
		_ = os.WriteFile(path, []byte(formatted), 0644)
		content = formatted
	}
	rel := relToWorkspace(workspace, path)
	var origin string
	if tc.diff != nil {
		origin = tc.diff.originOf(rel, old)
	} else {
		origin = old
	}
	tc.emitDiff(rel, unifiedDiff(rel, origin, content))
	// A real modification is the strongest signal the graph learns from.
	tc.signals.touch(rel, true)
	return true, nil
}

// formatOnWrite returns gofmt-normalized Go source, or the input unchanged for
// other languages (external formatters are intentionally not spawned here to
// keep the write path dependency-free and fast; diagnose/vet covers them).
func formatOnWrite(path, content string) (string, error) {
	if !strings.HasSuffix(path, ".go") {
		return content, nil
	}
	formatted, err := format.Source([]byte(content))
	if err != nil {
		// Invalid syntax at this intermediate step — leave the raw write as-is.
		return content, err
	}
	return string(formatted), nil
}

func relToWorkspace(workspace, path string) string {
	if workspace == "" {
		return filepath.Base(path)
	}
	if rel, err := filepath.Rel(workspace, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

func makeViewFileTool(tc adkToolContext) (any, error) {
	return functiontool.New[fileEditorArgs, map[string]any](functiontool.Config{
		Name:        "view_file",
		Description: "View a file's contents, optionally a line range. Large files are capped (default 800 lines per call) — use start_line/limit to page through. Set with_hashes=true when you intend to edit: each line is rendered as `line#hash: content`, and those anchors are what edit_file consumes.",
	}, func(ctx agent.Context, args fileEditorArgs) (map[string]any, error) {
		path, err := safePath(tc.Workspace, args.Path)
		if err != nil {
			return nil, err
		}
		const maxViewBytes = 2 << 20
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(data) > maxViewBytes {
			return nil, fmt.Errorf("file is too large to view at once (%d bytes > 2 MiB); use start_line/limit to page through it", len(data))
		}
		lines := strings.Split(string(data), "\n")
		total := len(lines)
		start := 1
		if args.StartLine > 0 {
			start = args.StartLine
		}
		end := total
		if args.EndLine > 0 && args.EndLine < end {
			end = args.EndLine
		}
		limit := args.Limit
		if limit <= 0 {
			limit = 800
		}
		if limit > 5000 {
			limit = 5000
		}
		truncated := false
		if end-start+1 > limit {
			end = start + limit - 1
			truncated = true
		}
		if start > total {
			start = total
		}
		if start < 1 {
			start = 1
		}
		if end < start {
			end = start
		}
		var b strings.Builder
		for i := start; i <= end && i <= total; i++ {
			if args.WithHashes {
				fmt.Fprintf(&b, "%s\n", hashlineViewLine(lines, i))
			} else {
				fmt.Fprintf(&b, "%d: %s\n", i, lines[i-1])
			}
		}
		tc.signals.touch(relToWorkspace(tc.Workspace, path), false)
		return map[string]any{
			"content": b.String(), "lines": end - start + 1, "total_lines": total,
			"truncated": truncated, "offset": start, "limit": limit,
		}, nil
	})
}

func makeCreateFileTool(tc adkToolContext) (any, error) {
	return functiontool.New[fileEditorArgs, map[string]any](functiontool.Config{
		Name:        "create_file",
		Description: "Create a new file with the given content.",
	}, func(ctx agent.Context, args fileEditorArgs) (map[string]any, error) {
		path, err := safePath(tc.Workspace, args.Path)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return nil, err
		}
		changed, err := writeFileWithDiff(tc, tc.Workspace, path, args.Content)
		if err != nil {
			return nil, err
		}
		return map[string]any{"status": "created", "path": path, "changed": changed}, nil
	})
}

func makeReplaceStringTool(tc adkToolContext) (any, error) {
	return functiontool.New[fileEditorArgs, map[string]any](functiontool.Config{
		Name:        "replace_string",
		Description: "Replace an exact string occurrence in a file.",
	}, func(ctx agent.Context, args fileEditorArgs) (map[string]any, error) {
		path, err := safePath(tc.Workspace, args.Path)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		content := string(data)
		if !strings.Contains(content, args.OldStr) {
			return nil, fmt.Errorf("old_str not found in %s", path)
		}
		content = strings.Replace(content, args.OldStr, args.NewStr, 1)
		changed, err := writeFileWithDiff(tc, tc.Workspace, path, content)
		if err != nil {
			return nil, err
		}
		return map[string]any{"status": "replaced", "path": path, "changed": changed}, nil
	})
}

func makeInsertLinesTool(tc adkToolContext) (any, error) {
	return functiontool.New[fileEditorArgs, map[string]any](functiontool.Config{
		Name:        "insert_lines",
		Description: "Insert text at a given line number.",
	}, func(ctx agent.Context, args fileEditorArgs) (map[string]any, error) {
		path, err := safePath(tc.Workspace, args.Path)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(string(data), "\n")
		line := args.Line
		if line < 0 {
			line = 0
		}
		if line > len(lines) {
			line = len(lines)
		}
		out := append([]string{}, lines[:line]...)
		out = append(out, args.Text)
		out = append(out, lines[line:]...)
		changed, err := writeFileWithDiff(tc, tc.Workspace, path, strings.Join(out, "\n"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"status": "inserted", "path": path, "changed": changed}, nil
	})
}

// makeHashlineEditTool adds the anchored multi-edit tool (oh-my-pi style
// hashline): edits reference line ranges by content hash instead of retyping
// the old text, so edits land on the first attempt and output tokens collapse.
// Anchors are validated against the CURRENT content before anything is
// written; a stale hash is rejected without touching the file.
func makeHashlineEditTool(tc adkToolContext) (any, error) {
	return functiontool.New[hashlineArgs, map[string]any](functiontool.Config{
		Name:        "edit_file",
		Description: "Edit a file by line-hash anchors (preferred for targeted changes — token-cheap and immune to 'old_str not found'). First view_file with_hashes=true, then send ops_json as a JSON array: [{\"start_line\":42,\"end_line\":45,\"hash\":\"<anchor>\",\"new_text\":\"...\",\"op\":\"replace\"}]. op: replace (default), delete, insert_after. Multiple ops apply atomically — a stale or overlapping anchor rejects the whole batch and nothing is written.",
	}, func(ctx agent.Context, args hashlineArgs) (map[string]any, error) {
		path, err := safePath(tc.Workspace, args.Path)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var edits []hashlineEdit
		if err := json.Unmarshal([]byte(strings.TrimSpace(args.OpsJSON)), &edits); err != nil {
			return nil, fmt.Errorf("ops_json is not valid JSON: %v", err)
		}
		newContent, summaries, err := applyHashlineEdits(string(data), edits)
		if err != nil {
			return nil, err
		}
		changed, err := writeFileWithDiff(tc, tc.Workspace, path, newContent)
		if err != nil {
			return nil, err
		}
		return map[string]any{"status": "edited", "path": path, "applied": len(summaries), "changes": summaries, "changed": changed}, nil
	})
}
