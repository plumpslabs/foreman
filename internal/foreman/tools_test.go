package foreman

import "testing"

func TestNormalizeToolIDsMapsLegacyAndDropsUnknown(t *testing.T) {
	got := NormalizeToolIDs([]string{"read", "search", "edit", "nonsense", "grep", "read"})
	want := []string{"file_editor", "grep"}
	if len(got) != len(want) {
		t.Fatalf("normalized = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalized = %#v, want %#v", got, want)
		}
	}
}

func TestResolveEngineToolsEnforcesTerminalPolicy(t *testing.T) {
	withTerminal := ResolveEngineTools([]string{"file_editor", "terminal", "grep"}, true)
	if !containsTool(withTerminal, "TerminalTool") {
		t.Fatalf("terminal should be present when allowed: %#v", withTerminal)
	}
	withoutTerminal := ResolveEngineTools([]string{"file_editor", "terminal", "grep"}, false)
	if containsTool(withoutTerminal, "TerminalTool") {
		t.Fatalf("terminal must be dropped when policy forbids it: %#v", withoutTerminal)
	}
	if !containsTool(withoutTerminal, "FileEditorTool") {
		t.Fatalf("an editor must always be present: %#v", withoutTerminal)
	}
}

func TestResolveEngineToolsDefaultsAndGuaranteesEditor(t *testing.T) {
	def := ResolveEngineTools(nil, false)
	if len(def) == 0 || !containsTool(def, "FileEditorTool") {
		t.Fatalf("default tools must include the editor: %#v", def)
	}
	noEditor := ResolveEngineTools([]string{"grep"}, false)
	if !containsTool(noEditor, "FileEditorTool") {
		t.Fatalf("editor must be auto-added: %#v", noEditor)
	}
}

func containsTool(tools []EngineTool, class string) bool {
	for _, tool := range tools {
		if tool.Class == class {
			return true
		}
	}
	return false
}
