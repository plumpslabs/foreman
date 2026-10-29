package foreman

import "strings"

// toolSpec is the OpenHands import path for one tool a run may load.
type toolSpec struct {
	Module string
	Class  string
	// Optional tools need extra SDK packages; loading falls back gracefully.
	Optional bool
}

// toolRegistry is the single source of truth for which OpenHands tools Foreman
// can wire into a run. Keep the keys in sync with the dashboard catalog.
var toolRegistry = map[string]toolSpec{
	"file_editor":     {Module: "openhands.tools.file_editor", Class: "FileEditorTool"},
	"task_tracker":    {Module: "openhands.tools.task_tracker", Class: "TaskTrackerTool"},
	"terminal":        {Module: "openhands.tools.terminal", Class: "TerminalTool"},
	"glob":            {Module: "openhands.tools.glob", Class: "GlobTool"},
	"grep":            {Module: "openhands.tools.grep", Class: "GrepTool"},
	"apply_patch":     {Module: "openhands.tools.apply_patch", Class: "ApplyPatchTool"},
	"planning_editor": {Module: "openhands.tools.planning_file_editor", Class: "PlanningFileEditorTool"},
	"browser":         {Module: "openhands.tools.browser_use", Class: "BrowserToolSet", Optional: true},
	"subagents":       {Module: "openhands.tools.task", Class: "TaskToolSet", Optional: true},
	"workflow":        {Module: "openhands.tools.workflow", Class: "WorkflowToolSet", Optional: true},
	"ask_oracle":      {Module: "openhands.tools.ask_oracle", Class: "AskOracleTool", Optional: true},
	"tom_consult":     {Module: "openhands.tools.tom_consult", Class: "TomConsultTool", Optional: true},
	"sleeptime":       {Module: "openhands.tools.tom_consult", Class: "SleeptimeComputeTool", Optional: true},
}

// legacyToolAlias maps the short tool names used by the shipped templates to
// canonical registry ids so old workspaces keep working.
var legacyToolAlias = map[string]string{
	"read": "file_editor", "edit": "file_editor", "write_tests": "file_editor",
	"search": "grep", "diff_audit": "apply_patch", "run_tests": "terminal",
	"run_commands": "terminal", "delegate": "subagents", "synthesize": "task_tracker",
	"preview": "browser",
}

// KnownToolIDs lists every wired tool id (used by tests and the API surface).
func KnownToolIDs() []string {
	ids := make([]string, 0, len(toolRegistry))
	for id := range toolRegistry {
		ids = append(ids, id)
	}
	return ids
}

// DefaultToolIDs is the minimal safe set when an agent names no tools.
func DefaultToolIDs() []string {
	return []string{"file_editor", "task_tracker", "grep", "glob"}
}

// NormalizeToolIDs maps legacy names to canonical ids, drops unknowns, and
// de-duplicates while preserving order.
func NormalizeToolIDs(tools []string) []string {
	out := make([]string, 0, len(tools))
	seen := map[string]bool{}
	for _, raw := range tools {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if alias, ok := legacyToolAlias[id]; ok {
			id = alias
		}
		if _, ok := toolRegistry[id]; !ok {
			continue
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// ResolveEngineTools turns agent tool ids into concrete OpenHands imports,
// enforcing the terminal policy and guaranteeing an editor is present.
func ResolveEngineTools(tools []string, allowCommands bool) []EngineTool {
	ids := NormalizeToolIDs(tools)
	if len(ids) == 0 {
		ids = DefaultToolIDs()
	}
	resolved := make([]EngineTool, 0, len(ids)+1)
	seen := map[string]bool{}
	hasEditor := false
	add := func(id string) {
		spec, ok := toolRegistry[id]
		if !ok {
			return
		}
		key := spec.Module + "." + spec.Class
		if seen[key] {
			return
		}
		seen[key] = true
		if id == "file_editor" {
			hasEditor = true
		}
		resolved = append(resolved, EngineTool{Name: id, Module: spec.Module, Class: spec.Class})
	}
	for _, id := range ids {
		if id == "terminal" && !allowCommands {
			continue
		}
		add(id)
	}
	if !hasEditor {
		editor := append([]EngineTool{{Name: "file_editor", Module: toolRegistry["file_editor"].Module, Class: toolRegistry["file_editor"].Class}}, resolved...)
		resolved = editor
	}
	return resolved
}
