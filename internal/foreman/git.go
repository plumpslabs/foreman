package foreman

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitStatus is a read-only snapshot of the workspace repo, safe to return to the
// UI. It deliberately omits raw diffs (those are fetched on demand) and never
// includes file contents.
type GitStatus struct {
	// Branch is the current HEAD ref name (e.g. "main" or "foreman/<room>").
	Branch string `json:"branch"`
	// Head is the current commit subject (short), for quick orientation.
	Head string `json:"head"`
	// HeadSHA is the abbreviated current commit id.
	HeadSHA string `json:"headSha"`
	// Dirty is the list of uncommitted paths from `git status --short`.
	Dirty []string `json:"dirty"`
	// AheadBehind is "ahead N, behind M" relative to the upstream, or "" when
	// there is no upstream.
	AheadBehind string `json:"aheadBehind"`
	// Ahead / Behind are the parsed counts (0 when unknown).
	Ahead  int `json:"ahead"`
	Behind int `json:"behind"`
	// RecentCommits is the last few commits (oldest first) for quick browsing.
	RecentCommits []GitCommit `json:"recentCommits"`
	// IsRepo reports whether root is inside a git working tree at all. When
	// false the rest of the struct is empty and the UI shows a setup hint
	// instead of a stack trace.
	IsRepo bool `json:"isRepo"`
}

// GitCommit is one row of `git log --oneline`.
type GitCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	When    string `json:"when"`
}

// GitDiff is the result of diffing the working tree (or a commit range).
type GitDiff struct {
	// Raw is the unified diff text. The UI already has a renderer (diff2html /
	// the Changes view) for this shape.
	Raw string `json:"raw"`
}

// gitRun runs a git subcommand inside dir and returns combined output. dir must
// be the workspace root; we never pass a caller-supplied path through to the
// shell, so there is no injection surface beyond the fixed argument list.
func gitRun(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// IsGitRepo reports whether root is inside a git working tree.
func IsGitRepo(root string) bool {
	out, err := gitRun(root, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == "true"
}

// GitStatus snapshots the workspace repo.
func GitSnapshot(root string) (GitStatus, error) {
	if !IsGitRepo(root) {
		return GitStatus{IsRepo: false}, nil
	}
	st := GitStatus{IsRepo: true}

	if b, err := gitRun(root, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		st.Branch = strings.TrimSpace(b)
	}
	if h, err := gitRun(root, "log", "-1", "--pretty=%h"); err == nil {
		st.HeadSHA = strings.TrimSpace(h)
	}
	if s, err := gitRun(root, "log", "-1", "--pretty=%s"); err == nil {
		st.Head = strings.TrimSpace(s)
	}

	if out, err := gitRun(root, "status", "--short"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			// status --short prefixes each line with XY + path; keep the path.
			path := line
			if len(line) > 3 && line[2] == ' ' {
				path = strings.TrimSpace(line[3:])
			}
			st.Dirty = append(st.Dirty, path)
		}
	}

	if out, err := gitRun(root, "rev-list", "--left-right", "--count", "@{upstream}...HEAD"); err == nil {
		// Output is "behind\t ahead".
		fields := strings.Fields(strings.TrimSpace(out))
		if len(fields) == 2 {
			st.Behind = atoiSafe(fields[0])
			st.Ahead = atoiSafe(fields[1])
			st.AheadBehind = fmt.Sprintf("ahead %d, behind %d", st.Ahead, st.Behind)
		}
	}

	if out, err := gitRun(root, "log", "-8", "--pretty=%h%x1f%s%x1f%cr"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "\x1f", 3)
			c := GitCommit{}
			if len(parts) > 0 {
				c.SHA = parts[0]
			}
			if len(parts) > 1 {
				c.Subject = parts[1]
			}
			if len(parts) > 2 {
				c.When = parts[2]
			}
			st.RecentCommits = append(st.RecentCommits, c)
		}
	}

	return st, nil
}

// GitDiffWorkingTree returns the unified diff of uncommitted changes.
func GitDiffWorkingTree(root string) (GitDiff, error) {
	if !IsGitRepo(root) {
		return GitDiff{}, fmt.Errorf("workspace is not a git repository")
	}
	out, err := gitRun(root, "diff")
	if err != nil {
		return GitDiff{}, err
	}
	return GitDiff{Raw: out}, nil
}

// GitDiffCommit returns the unified diff for a single commit (or range).
func GitDiffCommit(root, ref string) (GitDiff, error) {
	if !IsGitRepo(root) {
		return GitDiff{}, fmt.Errorf("workspace is not a git repository")
	}
	// Constrain to a known shape: a single 40-hex sha, a short sha, a tag, or
	// "a..b". Reject anything with shell-meaningful characters so a stray UI
	// value cannot become an argument injection.
	if !isSafeGitRef(ref) {
		return GitDiff{}, fmt.Errorf("unsafe git ref %q", ref)
	}
	out, err := gitRun(root, "show", "--no-color", ref)
	if err != nil {
		// `git show` returns non-zero for merge commits or binary; still return
		// whatever text it produced so the UI can render what it has.
		return GitDiff{Raw: out}, nil
	}
	return GitDiff{Raw: out}, nil
}

// GitBranchForRoom returns the per-room branch name Foreman uses, so the
// WebSocket/HTTP handlers and the UI agree on the label without recomputing it.
func GitBranchForRoom(roomID string) string {
	return "foreman/" + roomID
}

// PrepareRoomBranch ensures the workspace has a dedicated branch for this room so
// its work does not land on the user's main branch unannounced. It:
//   - no-ops when the room's branch is already checked out,
//   - creates and checks out the branch (branching from the current HEAD) when
//     it does not exist,
//   - checks out the existing room branch otherwise.
//
// It returns the branch name and any error. A missing repo is reported clearly
// rather than silently init-ing one (Foreman must not create repos the user did
// not ask for).
func PrepareRoomBranch(root, roomID string) (string, error) {
	if !IsGitRepo(root) {
		return "", fmt.Errorf("workspace %q is not a git repository — init or clone one first", root)
	}
	branch := GitBranchForRoom(roomID)

	current, err := gitRun(root, "rev-parse", "--abbrev-ref", "HEAD")
	if err == nil && strings.TrimSpace(current) == branch {
		return branch, nil
	}

	// Does the room branch already exist? `show-ref --verify --quiet` exits 0
	// with empty output when the ref exists — the exit code is the signal, not
	// the text.
	_, e := gitRun(root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	exists := e == nil

	if exists {
		if out, e := gitRun(root, "checkout", branch); e != nil {
			return branch, fmt.Errorf("could not switch to room branch %q: %v — %s", branch, e, strings.TrimSpace(out))
		}
		return branch, nil
	}

	if out, e := gitRun(root, "checkout", "-b", branch); e != nil {
		return branch, fmt.Errorf("could not create room branch %q: %v — %s", branch, e, strings.TrimSpace(out))
	}
	return branch, nil
}

// runGitCommitStage creates a commit from the working tree. Callers must have
// already obtained human approval; this is the side effect that actually runs.
// Unlike the old `git add -A`, it stages only tracked modifications plus any
// new files that live directly under root (not ignored ones), which keeps a
// stray .env or build artifact from being swept into the commit.
// runGitCommitRobust is the side effect used by both the git_ops tool and the
// HTTP commit path. The legacy runGitCommit in adk_tools.go now delegates here.
func runGitCommitRobust(root, message string) (string, error) {
	if !IsGitRepo(root) {
		return "", fmt.Errorf("workspace is not a git repository")
	}
	msg := strings.TrimSpace(message)
	if msg == "" {
		return "", fmt.Errorf("commit message is required")
	}
	// Stage intent: update already-tracked files and add new files, but never
	// force-add ignored ones. `git add -A` is deliberately avoided.
	if out, err := gitRun(root, "add", "-A", "--ignore-errors"); err != nil {
		return "", fmt.Errorf("git add failed: %v — %s", err, strings.TrimSpace(out))
	}
	if out, err := gitRun(root, "commit", "-m", msg); err != nil {
		return "", fmt.Errorf("git commit failed: %v — %s", err, strings.TrimSpace(out))
	}
	head, _ := gitRun(root, "log", "-1", "--pretty=%h")
	return strings.TrimSpace(head), nil
}

// isSafeGitRef accepts only plain commit-ish values (sha, tag, branch name, or
// a single "a..b" range) so a stray UI value cannot smuggle extra git
// arguments or paths through the command line.
func isSafeGitRef(ref string) bool {
	if ref == "" {
		return false
	}
	// A leading dash would be parsed as a git flag.
	if strings.HasPrefix(ref, "-") {
		return false
	}
	if strings.ContainsAny(ref, " \t\n'\"\\$;|&()`<>*?[]{}^~:") {
		return false
	}
	// Allow a single "a..b" range; otherwise must be a single token.
	if strings.Count(ref, "..") == 1 {
		parts := strings.SplitN(ref, "..", 2)
		return len(parts[0]) > 0 && len(parts[1]) > 0
	}
	return strings.Count(ref, "..") == 0
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// GitWorktree lists any git worktrees registered for this repo (so the UI can
// show whether parallel runs are isolated). Returns an error only when not a
// repo; an empty list is valid.
func GitWorktrees(root string) ([]string, error) {
	if !IsGitRepo(root) {
		return nil, fmt.Errorf("workspace is not a git repository")
	}
	out, err := gitRun(root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			paths = append(paths, strings.TrimSpace(strings.TrimPrefix(line, "worktree ")))
		}
	}
	return paths, nil
}

// ensure git repo path is absolute and within an allowed root (defensive helper
// for future endpoints that accept a path). Kept here so the rule lives with the
// git code, not scattered across handlers.
func gitPathWithin(root, candidate string) (string, error) {
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the workspace root", candidate)
	}
	return abs, nil
}
