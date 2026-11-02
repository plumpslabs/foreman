package foreman

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initTestRepo makes a git repo with one commit and returns its path.
func initGitTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v — %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "foreman@test")
	git("config", "user.name", "Foreman")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("commit", "-q", "-m", "initial commit")
	return dir
}

func TestGitSnapshotOnPlainRepo(t *testing.T) {
	dir := initGitTestRepo(t)
	st, err := GitSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsRepo {
		t.Fatal("expected IsRepo = true")
	}
	if st.Branch != "master" && st.Branch != "main" {
		t.Fatalf("branch = %q, want master|main", st.Branch)
	}
	if len(st.RecentCommits) != 1 || st.RecentCommits[0].Subject != "initial commit" {
		t.Fatalf("recent commits = %+v", st.RecentCommits)
	}
	if len(st.Dirty) != 0 {
		t.Fatalf("expected clean tree, got dirty=%v", st.Dirty)
	}
}

func TestGitSnapshotDetectsDirtyAndBranch(t *testing.T) {
	dir := initGitTestRepo(t)
	// Make a change and a new untracked file.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := GitSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Dirty) != 2 {
		t.Fatalf("expected 2 dirty entries, got %v", st.Dirty)
	}

	// Switch to a fresh room branch; snapshot should follow HEAD.
	branch, err := PrepareRoomBranch(dir, "room-123")
	if err != nil {
		t.Fatal(err)
	}
	if branch != "foreman/room-123" {
		t.Fatalf("branch = %q", branch)
	}
	st2, err := GitSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Branch != "foreman/room-123" {
		t.Fatalf("branch after prepare = %q", st2.Branch)
	}
}

func TestPrepareRoomBranchIdempotent(t *testing.T) {
	dir := initGitTestRepo(t)
	b1, err := PrepareRoomBranch(dir, "room-x")
	if err != nil {
		t.Fatal(err)
	}
	// Switch back to main, then prepare again — should re-checkout, not error.
	cmd := exec.Command("git", "checkout", "-q", "main")
	cmd.Dir = dir
	if out, e := cmd.CombinedOutput(); e != nil {
		// some repos default to master
		cmd = exec.Command("git", "checkout", "-q", "master")
		cmd.Dir = dir
		if out2, e2 := cmd.CombinedOutput(); e2 != nil {
			t.Fatalf("checkout back: %v — %s / %v — %s", e, out, e2, out2)
		}
	}
	b2, err := PrepareRoomBranch(dir, "room-x")
	if err != nil {
		t.Fatalf("second prepare failed: %v", err)
	}
	if b1 != b2 {
		t.Fatalf("branch changed between calls: %q vs %q", b1, b2)
	}
}

func TestGitNotARepo(t *testing.T) {
	dir := t.TempDir() // no .git
	st, err := GitSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.IsRepo {
		t.Fatal("expected IsRepo = false for a plain directory")
	}
	if _, err := PrepareRoomBranch(dir, "r1"); err == nil {
		t.Fatal("PrepareRoomBranch should error on a non-repo")
	}
}

func TestIsSafeGitRef(t *testing.T) {
	ok := []string{"abc123", "v1.2.3", "main", "a..b"}
	bad := []string{"", "a b", "a;b", "$(rm)", "a|x", "../escape", "a..b..c"}
	for _, r := range ok {
		if !isSafeGitRef(r) {
			t.Errorf("isSafeGitRef(%q) = false, want true", r)
		}
	}
	for _, r := range bad {
		if isSafeGitRef(r) {
			t.Errorf("isSafeGitRef(%q) = true, want false", r)
		}
	}
}

func TestRunGitCommitRobust(t *testing.T) {
	dir := initGitTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head, err := runGitCommitRobust(dir, "add file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if head == "" {
		t.Fatal("expected a commit sha")
	}
	st, err := GitSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Dirty) != 0 {
		t.Fatalf("commit left dirty files: %v", st.Dirty)
	}
	// RecentCommits are newest-first: the freshly made commit is row 0.
	if st.RecentCommits[0].Subject != "add file.txt" {
		t.Fatalf("commit subject not recorded: %+v", st.RecentCommits)
	}
}

func TestRunGitCommitEmptyMessage(t *testing.T) {
	dir := initGitTestRepo(t)
	if _, err := runGitCommitRobust(dir, "   "); err == nil {
		t.Fatal("empty commit message should error")
	}
}
