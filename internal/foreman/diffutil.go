package foreman

import (
	"fmt"
	"strings"
)

// maxDiffLines caps emitted diffs so a huge edit doesn't flood events/rows.
const maxDiffLines = 300

const diffContext = 3

// unifiedDiff builds a compact unified diff between old and new file content.
// It trims common prefix/suffix lines and emits a single hunk, which matches
// the targeted edits agents perform (replace/insert/create).
func unifiedDiff(path, oldContent, newContent string) string {
	if oldContent == newContent {
		return ""
	}
	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)

	// common prefix length
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	// common suffix length
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	oldLen := len(oldLines) - prefix - suffix
	newLen := len(newLines) - prefix - suffix
	if oldLen == 0 && newLen == 0 {
		return ""
	}

	var b strings.Builder
	if path == "" {
		path = "file"
	}
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", path, path)
	fmt.Fprintf(&b, "--- a/%s\n", path)
	fmt.Fprintf(&b, "+++ b/%s\n", path)

	// context before the change
	ctxStart := prefix - diffContext
	if ctxStart < 0 {
		ctxStart = 0
	}
	for i := ctxStart; i < prefix; i++ {
		fmt.Fprintf(&b, " %s\n", newLines[i])
	}
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", prefix+1, oldLen, prefix+1, newLen)
	for i := prefix; i < len(oldLines)-suffix; i++ {
		if strings.Count(b.String(), "\n") > maxDiffLines {
			b.WriteString("... (diff truncated)\n")
			return b.String()
		}
		fmt.Fprintf(&b, "-%s\n", oldLines[i])
	}
	for i := prefix; i < len(newLines)-suffix; i++ {
		if strings.Count(b.String(), "\n") > maxDiffLines {
			b.WriteString("... (diff truncated)\n")
			return b.String()
		}
		fmt.Fprintf(&b, "+%s\n", newLines[i])
	}
	// context after the change
	ctxEnd := len(newLines) - suffix + diffContext
	for i := len(newLines) - suffix; i < ctxEnd && i < len(newLines); i++ {
		fmt.Fprintf(&b, " %s\n", newLines[i])
	}
	return b.String()
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// emitDiff sends a diff event to the run events stream (rendered in the
// Changes tab) if an emit callback is available.
func (tc adkToolContext) emitDiff(relPath, diff string) {
	if tc.Emit == nil || diff == "" {
		return
	}
	tc.Emit(EngineEvent{Type: "diff", Message: diff})
}
