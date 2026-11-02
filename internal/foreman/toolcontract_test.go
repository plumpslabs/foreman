package foreman

import (
	"sort"
	"strings"
	"testing"
)

// The prompt's "Available Autonomous Tools" list is generated from
// allowedADKToolNames, but the tools an agent can actually call are decided by
// makeADKTools (which honours the project-command gate). When the two drift the
// agent is told about a capability it does not have — the exact cause of a
// coordinator writing "I'm not sure run_checks is available" mid-run.
//
// These tests pin the two sets together.

// registeredToolNames builds the real tool set for a run configuration and
// returns its names.
func registeredToolNames(t *testing.T, allowed []string, allowCmds bool) map[string]bool {
	t.Helper()
	ctx := adkToolContext{
		Store:     testStore(t),
		RoomID:    "room-1",
		TaskID:    "task-1",
		Workspace: t.TempDir(),
		AgentName: "Agent",
		AllowCmds: allowCmds,
	}
	tools := makeADKTools(ctx)
	out := map[string]bool{}
	for _, tl := range tools {
		if named, ok := tl.(interface{ Name() string }); ok {
			out[named.Name()] = true
		}
	}
	return out
}

// TestAllowlistMatchesRegisteredTools is the core consistency guard.
func TestAllowlistMatchesRegisteredTools(t *testing.T) {
	cases := []struct {
		name    string
		allowed []string
		cmds    bool
	}{
		{"editor only", []string{"file_editor"}, false},
		{"editor+grep", []string{"file_editor", "grep"}, false},
		{"coordinator with commands", []string{"file_editor", "grep", "terminal", "task_tracker"}, true},
		{"coordinator without commands", []string{"file_editor", "grep", "terminal", "task_tracker"}, false},
		{"full set with commands", []string{"file_editor", "grep", "terminal", "task_tracker", "apply_patch", "planning_editor"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			registered := registeredToolNames(t, c.allowed, c.cmds)
			allowed := allowedADKToolNames(c.allowed, c.cmds)

			var missing, phantom []string
			for name := range allowed {
				if !registered[name] {
					phantom = append(phantom, name)
				}
			}
			// The reverse direction is expected to differ: makeADKTools registers
			// many tools the allowlist narrows. Only assert on phantom promises,
			// because those are what mislead the model.
			sort.Strings(phantom)
			sort.Strings(missing)
			if len(phantom) > 0 {
				t.Errorf("prompt would advertise tools that are not registered: %v", phantom)
			}
		})
	}
}

// TestProjectCommandToolsAreGated: run_checks/diagnose must not be advertised
// when project commands are disabled, because they are not registered then.
func TestProjectCommandToolsAreGated(t *testing.T) {
	withCmds := allowedADKToolNames([]string{"terminal"}, true)
	withoutCmds := allowedADKToolNames([]string{"terminal"}, false)
	for _, name := range []string{"run_checks", "diagnose"} {
		if !withCmds[name] {
			t.Errorf("%q should be allowed when project commands are enabled", name)
		}
		if withoutCmds[name] {
			t.Errorf("%q must not be advertised when project commands are disabled", name)
		}
	}
}

// TestPromptDoesNotAdvertiseGatedTools is the user-visible half of the rule: the
// rendered prompt itself must stay silent about gated tools.
func TestPromptDoesNotAdvertiseGatedTools(t *testing.T) {
	room := Room{Name: "fix", Goal: "perbaiki bug"}
	task := Task{Title: "fix status", Description: "perbaiki status tiket"}
	tools := []string{"file_editor", "grep", "terminal"}

	disabled := buildRunPrompt(room, task, nil, "", tools, false)
	if strings.Contains(disabled, "- run_checks:") || strings.Contains(disabled, "- diagnose:") {
		t.Errorf("prompt advertises gated tools while project commands are off:\n%s", disabled)
	}

	enabled := buildRunPrompt(room, task, nil, "", tools, true)
	if !strings.Contains(enabled, "- run_checks:") {
		t.Error("prompt should advertise run_checks when project commands are on")
	}
	if !strings.Contains(enabled, "- diagnose:") {
		t.Error("prompt should advertise diagnose when project commands are on")
	}
}

// TestAdvertisedToolsMatchAllowlist: every tool the prompt lists must be in the
// allowlist used to build the tool set — no doc entry may bypass the gate.
func TestAdvertisedToolsMatchAllowlist(t *testing.T) {
	allowed := allowedADKToolNames([]string{"file_editor", "grep", "terminal", "task_tracker"}, false)
	section := advertisedToolsSection([]string{"file_editor", "grep", "terminal", "task_tracker"}, false)
	for _, doc := range autonomousToolDocs {
		listed := strings.Contains(section, "- "+doc.name+":")
		if listed && !allowed[doc.name] {
			t.Errorf("prompt lists %q but the allowlist does not permit it", doc.name)
		}
		if allowed[doc.name] && !listed {
			t.Errorf("%q is permitted but missing from the prompt's tool list", doc.name)
		}
	}
}
