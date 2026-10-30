package foreman

import (
	"os"
	"path/filepath"
	"testing"
)

func names(checks []diagnoseCheck) []string {
	out := make([]string, 0, len(checks))
	for _, c := range checks {
		out = append(out, c.name)
	}
	return out
}

func has(checks []diagnoseCheck, name string) bool {
	for _, c := range checks {
		if c.name == name {
			return true
		}
	}
	return false
}

func TestDiagnoseChecksGo(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644)
	checks := diagnoseChecks(dir, "")
	if !has(checks, "build") || !has(checks, "vet") || !has(checks, "test") {
		t.Fatalf("expected build/vet/test for Go project, got %v", names(checks))
	}
	for _, c := range checks {
		if c.language != "go" {
			t.Fatalf("expected go language, got %q for %q", c.language, c.name)
		}
	}
}

func TestDiagnoseChecksScopeFilter(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644)
	checks := diagnoseChecks(dir, "vet")
	if len(checks) != 1 || checks[0].name != "vet" {
		t.Fatalf("scope=vet should yield only the vet check, got %v", names(checks))
	}
}

func TestDiagnoseChecksNode(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"lint":"eslint .","test":"vitest"}}`), 0o644)
	checks := diagnoseChecks(dir, "")
	if !has(checks, "vet") || !has(checks, "lint") || !has(checks, "test") {
		t.Fatalf("expected vet/lint/test for TS project, got %v", names(checks))
	}
}

func TestDiagnoseChecksFallback(t *testing.T) {
	checks := diagnoseChecks(t.TempDir(), "")
	if len(checks) == 0 || checks[0].name != "workspace" {
		t.Fatalf("expected workspace fallback, got %v", names(checks))
	}
}
