package foreman

import (
	"strings"
	"testing"
)

const hlSample = "alpha\nbeta\ngamma\ndelta\n"

func hlHash(t *testing.T, content string, start, end int) string {
	t.Helper()
	h, err := anchorHash(strings.Split(strings.TrimSuffix(content, "\n"), "\n"), start, end)
	if err != nil {
		t.Fatalf("anchorHash(%d,%d): %v", start, end, err)
	}
	return h
}

func TestAnchorHashContentDerived(t *testing.T) {
	c1 := "alpha\nbeta\n"
	c2 := "inserted\nalpha\nbeta\n" // lines shifted down by one
	h1 := hlHash(t, c1, 1, 2)
	h2 := hlHash(t, c2, 2, 3)
	if h1 != h2 {
		t.Errorf("same content at shifted positions must hash equal: %s vs %s", h1, h2)
	}
	h3 := hlHash(t, c1, 2, 2)
	if h1 == h3 {
		t.Errorf("different content must hash differently")
	}
	if len(h1) != hashlineHexLen {
		t.Errorf("hash length = %d, want %d", len(h1), hashlineHexLen)
	}
}

func TestApplyHashlineEditsReplace(t *testing.T) {
	h := hlHash(t, hlSample, 2, 3)
	out, summaries, err := applyHashlineEdits(hlSample, []hashlineEdit{{
		StartLine: 2, EndLine: 3, Hash: h, NewText: "BETA2\nGAMMA2", Op: "replace",
	}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := "alpha\nBETA2\nGAMMA2\ndelta\n"
	if out != want {
		t.Errorf("result = %q, want %q", out, want)
	}
	if len(summaries) != 1 || !strings.Contains(summaries[0], "replaced lines 2-3") {
		t.Errorf("summaries = %v", summaries)
	}
}

func TestApplyHashlineEditsDeleteAndInsert(t *testing.T) {
	hDel := hlHash(t, hlSample, 1, 1)
	hIns := hlHash(t, hlSample, 2, 2)
	out, _, err := applyHashlineEdits(hlSample, []hashlineEdit{
		{StartLine: 1, Hash: hDel, Op: "delete"},
		{StartLine: 2, Hash: hIns, NewText: "beta2", Op: "insert_after"},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := "beta\nbeta2\ngamma\ndelta\n"
	if out != want {
		t.Errorf("result = %q, want %q", out, want)
	}
}

func TestApplyHashlineEditsStaleHashRejected(t *testing.T) {
	if strings.HasSuffix(hlSample, "\n") != true {
		t.Fatal("sample sanity")
	}
	h := hlHash(t, "alpha\nbeta\n", 1, 2)
	stale := "alphaX\nbeta\ngamma\ndelta\n" // first line changed since view
	_, _, err := applyHashlineEdits(stale, []hashlineEdit{{StartLine: 1, EndLine: 2, Hash: h, NewText: "x"}})
	if err == nil {
		t.Fatal("stale hash must be rejected")
	}
	if !strings.Contains(err.Error(), "stale anchor") {
		t.Errorf("error should mention stale anchor, got %v", err)
	}
	// The original file content must survive unchanged on rejection.
}

func TestApplyHashlineEditsOverlappingRejected(t *testing.T) {
	h := hlHash(t, hlSample, 2, 3)
	_, _, err := applyHashlineEdits(hlSample, []hashlineEdit{
		{StartLine: 2, EndLine: 3, Hash: h, NewText: "x"},
		{StartLine: 3, EndLine: 3, Hash: hlHash(t, hlSample, 3, 3), NewText: "y"},
	})
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("overlap must be rejected, got %v", err)
	}
}

func TestApplyHashlineEditsReplaceNeedsText(t *testing.T) {
	h := hlHash(t, hlSample, 2, 2)
	_, _, err := applyHashlineEdits(hlSample, []hashlineEdit{{StartLine: 2, Hash: h, Op: "replace", NewText: "  "}})
	if err == nil || !strings.Contains(err.Error(), "op=delete") {
		t.Fatalf("empty replace must point at op=delete, got %v", err)
	}
}

func TestApplyHashlineEditsAllOrNothing(t *testing.T) {
	// Second edit is invalid: nothing must be applied even though the first
	// edit alone would succeed.
	h1 := hlHash(t, hlSample, 1, 1)
	out, _, err := applyHashlineEdits(hlSample, []hashlineEdit{
		{StartLine: 1, Hash: h1, NewText: "ALPHA"},
		{StartLine: 99, Hash: "aaaaaaaaaaaa", NewText: "boom"},
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if out != "" {
		t.Errorf("no output on validation failure, got %q", out)
	}
}

func TestHashlineViewLine(t *testing.T) {
	lines := []string{"alpha", "beta"}
	got := hashlineViewLine(lines, 1)
	want := "1#" + hlHash(t, "alpha\n", 1, 1) + ": alpha"
	if got != want {
		t.Errorf("hashlineViewLine = %q, want %q", got, want)
	}
}
