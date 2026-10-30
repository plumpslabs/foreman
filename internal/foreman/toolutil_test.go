package foreman

import (
	"testing"

	gitignore "github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

func TestGitignoreMatcher(t *testing.T) {
	ig := &projectIgnores{matcher: gitignore.NewMatcher([]gitignore.Pattern{
		gitignore.ParsePattern("node_modules", nil),
		gitignore.ParsePattern("/dist", nil),
		gitignore.ParsePattern("*.log", nil),
		gitignore.ParsePattern("build/", nil),
		gitignore.ParsePattern("src/**", nil),
	})}
	cases := []struct {
		rel  string
		dir  bool
		want bool
	}{
		{"node_modules/x", false, true},
		{"packages/a/node_modules/x", false, true},
		{"dist/x", false, true},
		{"a/dist/x", false, false},
		{"logs/app.log", false, true},
		{"app.txt", false, false},
		{"build/out/x", false, true},
		{"src", true, true},
		{"src/a/b.ts", false, true},
		{"src/main.go", false, true},
	}
	for _, c := range cases {
		if got := ig.match(c.rel, c.dir); got != c.want {
			t.Errorf("match(%q, dir=%v) = %v, want %v", c.rel, c.dir, got, c.want)
		}
	}
}

func TestGlobRegex(t *testing.T) {
	cases := []struct {
		pattern string
		rel     string
		want    bool
	}{
		{"**/*.ts", "a/b/c.ts", true},
		{"**/*.ts", "x.ts", true},
		{"**/*.ts", "a/b.js", false},
		{"src/**", "src", true},
		{"src/**", "src/deep/file.go", true},
		{"frontend/src/**/*.tsx", "frontend/src/pages/dashboard/tickets/components/TicketItem.tsx", true},
		{"*.go", "cmd/main.go", false}, // top-level only, no slash
	}
	for _, c := range cases {
		if got := globRegex(c.pattern).MatchString(c.rel); got != c.want {
			t.Errorf("glob %q on %q = %v, want %v", c.pattern, c.rel, got, c.want)
		}
	}
}
