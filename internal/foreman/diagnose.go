package foreman

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// diagnoseCheck is one static/diagnostic command for the detected project.
type diagnoseCheck struct {
	name     string
	command  string
	language string
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// diagnoseChecks picks language-appropriate read-only checks for a workspace.
// The optional scope narrows to a single kind ("build", "vet", "test").
func diagnoseChecks(workspace, scope string) []diagnoseCheck {
	scope = strings.ToLower(strings.TrimSpace(scope))
	var checks []diagnoseCheck
	add := func(name, command, language string) {
		if scope != "" && scope != "all" && scope != name {
			return
		}
		checks = append(checks, diagnoseCheck{name: name, command: command, language: language})
	}

	switch {
	case exists(workspace, "go.mod"):
		add("build", "go build ./...", "go")
		add("vet", "go vet ./...", "go")
		add("test", "go test ./...", "go")
	case exists(workspace, "Cargo.toml"):
		add("build", "cargo check --quiet", "rust")
		add("test", "cargo test --quiet", "rust")
	case exists(workspace, "tsconfig.json") || exists(workspace, "package.json"):
		if exists(workspace, "tsconfig.json") {
			add("vet", "npx --no-install tsc --noEmit", "typescript")
		}
		for _, script := range []string{"lint", "build", "test"} {
			if hasNpmScript(workspace, script) {
				add(script, "npm run --silent "+script, "javascript")
			}
		}
	case exists(workspace, "pyproject.toml") || exists(workspace, "setup.py") || exists(workspace, "requirements.txt"):
		add("build", "python3 -m compileall -q .", "python")
		add("test", "python3 -m pytest -q", "python")
	default:
		// no recognized project: at least report the working-tree state
		add("workspace", "git status --short", "")
	}

	if len(checks) == 0 {
		checks = append(checks, diagnoseCheck{name: "workspace", command: "git status --short"})
	}
	if len(checks) > 6 {
		checks = checks[:6]
	}
	return checks
}

// hasNpmScript reports whether package.json defines the given script.
func hasNpmScript(dir, script string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return false
	}
	_, ok := pkg.Scripts[script]
	return ok
}
