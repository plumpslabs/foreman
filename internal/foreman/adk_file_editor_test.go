package foreman

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSafePathContainment(t *testing.T) {
	ws := "/proj/work"
	cases := []struct {
		path string
		ok   bool
	}{
		{"src/a.ts", true},
		{"src/../b.ts", true},         // cleans inside workspace
		{"/proj/work/src/a.ts", true}, // abs inside workspace
		{"../escape.ts", false},
		{"/etc/passwd", false},
		{"/proj/work/../other/x", false}, // escapes via ..
	}
	for _, c := range cases {
		got, err := safePath(ws, c.path)
		if c.ok && (err != nil || !strings.HasPrefix(filepath.ToSlash(got), filepath.ToSlash(ws))) {
			t.Errorf("expected ok for %q, got %q err=%v", c.path, got, err)
		}
		if !c.ok && err == nil {
			t.Errorf("expected error for %q, got %q", c.path, got)
		}
	}
}
