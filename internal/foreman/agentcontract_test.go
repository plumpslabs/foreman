package foreman

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectProjectContractPrefersAGENTS(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# readme"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# agents"), 0644); err != nil {
		t.Fatal(err)
	}
	name, _, ok := detectProjectContract(dir)
	if !ok || name != "AGENTS.md" {
		t.Errorf("expected AGENTS.md, got %q ok=%v", name, ok)
	}
}

func TestDetectProjectContractFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# readme"), 0644); err != nil {
		t.Fatal(err)
	}
	name, _, ok := detectProjectContract(dir)
	if !ok || name != "README.md" {
		t.Errorf("expected README.md, got %q ok=%v", name, ok)
	}
}

func TestDetectProjectContractEmpty(t *testing.T) {
	dir := t.TempDir()
	if _, _, ok := detectProjectContract(dir); ok {
		t.Error("expected no contract in empty dir")
	}
	if _, _, ok := detectProjectContract(""); ok {
		t.Error("expected no contract for empty root")
	}
}

func TestProjectContractSection(t *testing.T) {
	dir := t.TempDir()
	body := "# Contributions\n- write tests\n- keep it simple\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	sec := projectContractSection(dir)
	if sec == "" {
		t.Fatal("expected non-empty contract section")
	}
	if !strings.Contains(sec, "write tests") {
		t.Errorf("contract section missing body: %q", sec)
	}
	if strings.Contains(projectContractSection(filepath.Join(t.TempDir())), "contract") {
		t.Error("expected empty section when no contract file")
	}
}

func TestDrillOptions(t *testing.T) {
	cases := []struct {
		action string
		want   int
		has    string
	}{
		{"commit this", 3, "commit_only"},
		{"push changes", 3, "commit_and_push"},
		{"delete everything", 3, "delete_all"},
		{"remove file", 3, "delete_one"},
		{"spawn a reviewer", 3, "spawn_scoped"},
		{"do something weird", 2, "proceed"},
	}
	for _, c := range cases {
		got := drillOptions(c.action, "")
		if len(got) != c.want {
			t.Errorf("action %q: got %d options, want %d", c.action, len(got), c.want)
		}
		found := false
		for _, o := range got {
			if o["option"] == c.has {
				found = true
			}
		}
		if !found {
			t.Errorf("action %q: missing option %q in %#v", c.action, c.has, got)
		}
	}
}

func TestInitProjectFromContract(t *testing.T) {
	st := testStore(t)
	db := st.DB()
	if db == nil {
		t.Fatal("no db")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Hello\n- be kind\n"), 0644); err != nil {
		t.Fatal(err)
	}
	room, err := st.CreateRoom("contract room", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	facts, tasks, err := initProjectFromContract(st, room.ID, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if facts < 1 {
		t.Errorf("expected >=1 fact retained, got %d", facts)
	}
	if tasks < 1 {
		t.Errorf("expected >=1 task created, got %d", tasks)
	}
}
