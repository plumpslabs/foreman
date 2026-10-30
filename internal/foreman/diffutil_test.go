package foreman

import (
	"strings"
	"testing"
)

func TestUnifiedDiffReplace(t *testing.T) {
	old := "alpha\nbeta\ngamma\ndelta\nepsilon\nzeta\neta\ntheta\n"
	nu := "alpha\nbeta\nGAMMA\ndelta\nepsilon\nzeta\neta\ntheta\n"
	d := unifiedDiff("src/app.ts", old, nu)
	if d == "" {
		t.Fatal("expected non-empty diff")
	}
	for _, want := range []string{"diff --git a/src/app.ts b/src/app.ts", "--- a/src/app.ts", "+++ b/src/app.ts", "@@ ", "-gamma", "+GAMMA", " beta"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff missing %q:\n%s", want, d)
		}
	}
}

func TestUnifiedDiffCreateAndDestroy(t *testing.T) {
	d := unifiedDiff("new.txt", "", "hello\nworld\n")
	if !strings.Contains(d, "+hello") || strings.Contains(d, "\n-a") {
		t.Errorf("unexpected create diff:\n%s", d)
	}
	d2 := unifiedDiff("gone.txt", "a\nb\n", "")
	if !strings.Contains(d2, "-a") {
		t.Errorf("unexpected delete diff:\n%s", d2)
	}
	// no change → empty
	if got := unifiedDiff("same.txt", "a\nb\n", "a\nb\n"); got != "" {
		t.Errorf("expected empty diff, got %s", got)
	}
}

func TestUnifiedDiffHugeTruncation(t *testing.T) {
	old := strings.Repeat("x\n", 2000)
	nu := strings.Repeat("y\n", 2000)
	d := unifiedDiff("big.txt", old, nu)
	if !strings.Contains(d, "truncated") {
		t.Error("expected truncation marker for huge diff")
	}
}
