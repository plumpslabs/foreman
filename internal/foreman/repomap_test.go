package foreman

import "testing"

func TestSymbolizeLine(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"func (s *Store) CreateTask(task Task) (Task, error) {", "func CreateTask(…)"},
		{"type Store struct {", "type Store struct"},
		{"export function mergeFiles(files) {", "export function mergeFiles(files)"},
		{"pub fn build_repo_map(root: &str) {", "pub fn build_repo_map(root: &str)"},
		{"def handle_event(event):", "def handle_event(event)"},
		{"const x = 1;", "const x = 1;"},
		{"// just a comment", ""},
		{"import os", ""},
	}
	for _, c := range cases {
		if got := symbolizeLine(c.line); got != c.want {
			t.Errorf("symbolizeLine(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}
