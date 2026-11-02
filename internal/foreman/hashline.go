package foreman

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// hashline editing: the model references line ranges by a short content hash
// instead of retyping the text it wants to replace. This is the edit format
// popularized by oh-my-pi (https://github.com/can1357/oh-my-pi): edits land on
// the first attempt, "string not found" loops disappear, and output tokens
// collapse because the anchor replaces the old content entirely.
//
// A stale file (edited since view_file) fails hash validation and is rejected
// BEFORE anything is written, so a drifted anchor never corrupts a file.

const hashlineHexLen = 12

// hashlineEdit is one anchored change. Op semantics:
//   - "replace" (default): lines start..end become the lines of NewText
//     (NewText must be non-empty; use op "delete" to remove lines).
//   - "delete": lines start..end are removed.
//   - "insert_after": NewText is inserted after line end.
type hashlineEdit struct {
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line,omitempty"`
	Hash      string `json:"hash"`
	NewText   string `json:"new_text,omitempty"`
	Op        string `json:"op,omitempty"`
}

// hashlineArgsJSON is the tool argument: ops are passed as a JSON string of
// []hashlineEdit, following the repo's nodes_json/edges_json pattern (flat
// tool schemas for weak models).
type hashlineArgs struct {
	Path    string `json:"path"`
	OpsJSON string `json:"ops_json"`
}

// anchorHash is the short content hash of lines[start-1..end-1] joined with
// newlines. Hashes are content-derived (not position-derived), so inserting
// lines above does not invalidate unaffected anchors.
func anchorHash(lines []string, start, end int) (string, error) {
	if err := checkRange(len(lines), start, end); err != nil {
		return "", err
	}
	content := strings.Join(lines[start-1:end], "\n")
	sum := sha1.Sum([]byte(content))
	return hex.EncodeToString(sum[:])[:hashlineHexLen], nil
}

func checkRange(total, start, end int) error {
	if start < 1 || end < start || end > total {
		return fmt.Errorf("line range %d-%d is out of bounds (file has %d lines)", start, end, total)
	}
	return nil
}

// validateHashlineEdits checks every edit against the ORIGINAL content: range
// bounds, op, hash, and pairwise overlap. All-or-nothing, so one bad edit
// never leaves a file half-edited.
func validateHashlineEdits(lines []string, edits []hashlineEdit) ([]hashlineEdit, error) {
	if len(edits) == 0 {
		return nil, fmt.Errorf("no edits supplied")
	}
	type span struct{ start, end int }
	spans := make([]span, 0, len(edits))
	for i := range edits {
		e := &edits[i]
		if e.StartLine <= 0 {
			return nil, fmt.Errorf("edit %d: start_line is required", i+1)
		}
		if e.EndLine < e.StartLine {
			e.EndLine = e.StartLine
		}
		e.Hash = strings.ToLower(strings.TrimSpace(e.Hash))
		if len(e.Hash) != hashlineHexLen {
			return nil, fmt.Errorf("edit %d: hash must be %d hex characters (copy the anchor from view_file with_hashes=true)", i+1, hashlineHexLen)
		}
		switch e.Op {
		case "", "replace":
			e.Op = "replace"
			if strings.TrimSpace(e.NewText) == "" {
				return nil, fmt.Errorf("edit %d: op=replace needs non-empty new_text; use op=delete to remove lines", i+1)
			}
		case "delete", "insert_after":
			// ok
		default:
			return nil, fmt.Errorf("edit %d: unknown op %q (replace|delete|insert_after)", i+1, e.Op)
		}
		want, err := anchorHash(lines, e.StartLine, e.EndLine)
		if err != nil {
			return nil, fmt.Errorf("edit %d: %v", i+1, err)
		}
		if want != e.Hash {
			found := e.EndLine - e.StartLine + 1
			if found > 3 {
				found = 3
			}
			peek := strings.Join(lines[e.StartLine-1:e.StartLine-1+found], " ⏎ ")
			return nil, fmt.Errorf("edit %d: stale anchor at lines %d-%d (hash mismatch; current text starts with %q). View the file again (view_file with_hashes=true) and retry", i+1, e.StartLine, e.EndLine, peek)
		}
		s := span{e.StartLine, e.EndLine}
		for _, prev := range spans {
			if e.Op == "insert_after" && prev.end == e.EndLine && prev.start == e.StartLine {
				return nil, fmt.Errorf("edit %d: another edit already inserts after line %d", i+1, e.EndLine)
			}
			if s.start <= prev.end && prev.start <= s.end {
				return nil, fmt.Errorf("edit %d: line range %d-%d overlaps another edit (%d-%d)", i+1, s.start, s.end, prev.start, prev.end)
			}
		}
		spans = append(spans, s)
	}
	return edits, nil
}

// applyHashlineEdits validates against the current content, applies bottom-up
// so line numbers stay valid, and returns the new content with per-edit
// summaries for the tool result.
func applyHashlineEdits(content string, edits []hashlineEdit) (string, []string, error) {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	trailingNewline := strings.HasSuffix(content, "\n")
	valid, err := validateHashlineEdits(lines, edits)
	if err != nil {
		return "", nil, err
	}
	sort.SliceStable(valid, func(i, j int) bool { return valid[i].StartLine > valid[j].StartLine })
	summaries := make([]string, 0, len(valid))
	for _, e := range valid {
		newLines := []string{}
		switch e.Op {
		case "delete":
			newLines = nil
		case "insert_after":
			newLines = strings.Split(e.NewText, "\n")
		default: // replace
			newLines = strings.Split(e.NewText, "\n")
		}
		out := []string{}
		switch e.Op {
		case "delete":
			// Drop lines start..end.
			out = append(out, lines[:e.StartLine-1]...)
			out = append(out, lines[e.EndLine:]...)
		case "insert_after":
			// Keep lines 1..end, then new text, then the rest.
			out = append(out, lines[:e.EndLine]...)
			out = append(out, newLines...)
			out = append(out, lines[e.EndLine:]...)
		default: // replace
			// Keep lines 1..start-1, then new text, then lines after end.
			out = append(out, lines[:e.StartLine-1]...)
			out = append(out, newLines...)
			out = append(out, lines[e.EndLine:]...)
		}
		switch e.Op {
		case "delete":
			summaries = append(summaries, fmt.Sprintf("deleted lines %d-%d", e.StartLine, e.EndLine))
		case "insert_after":
			summaries = append(summaries, fmt.Sprintf("inserted after line %d", e.EndLine))
		default:
			summaries = append(summaries, fmt.Sprintf("replaced lines %d-%d", e.StartLine, e.EndLine))
		}
		lines = out
	}
	result := strings.Join(lines, "\n")
	if trailingNewline {
		result += "\n"
	}
	return result, summaries, nil
}

// hashlineViewLine renders one line with its anchor, for view_file
// with_hashes=true output: "42#abc123def456: content".
func hashlineViewLine(lines []string, lineNo int) string {
	h, err := anchorHash(lines, lineNo, lineNo)
	if err != nil {
		return fmt.Sprintf("%d: %s", lineNo, lines[lineNo-1])
	}
	return fmt.Sprintf("%d#%s: %s", lineNo, h, lines[lineNo-1])
}
