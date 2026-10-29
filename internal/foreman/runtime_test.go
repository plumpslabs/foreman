package foreman

import (
	"strings"
	"testing"
)

func TestSkillsContextInjectsActiveAndListsPassive(t *testing.T) {
	skills := []Skill{
		{ID: "s1", Name: "Refactor", Mode: SkillModeActive, Instructions: "Follow the refactor checklist.\n\n1. Keep API stable."},
		{ID: "s2", Name: "Docs", Mode: SkillModePassive, Description: "Writing guide"},
	}
	team := &Team{
		MainAgent: "M",
		Agents: []Agent{
			{Name: "M", Skills: []string{"s1", "s2"}},
		},
	}
	got := skillsContext(team, Task{}, skills)
	if !strings.Contains(got, "## Active skills") {
		t.Fatalf("active skills section missing:\n%s", got)
	}
	if !strings.Contains(got, "### Refactor") || !strings.Contains(got, "refactor checklist") {
		t.Fatalf("active skill body missing:\n%s", got)
	}
	if !strings.Contains(got, "## Reference skills (passive)") || !strings.Contains(got, "Docs: Writing guide") {
		t.Fatalf("passive skill listing missing:\n%s", got)
	}
}

func TestSkillsContextResolvesByNameAndDeduplicates(t *testing.T) {
	skills := []Skill{
		{ID: "s1", Name: "Refactor", Mode: SkillModeActive, Instructions: "ABC"},
	}
	team := &Team{
		MainAgent: "M",
		Agents: []Agent{
			{Name: "M", Skills: []string{"Refactor", "s1", "missing"}},
		},
	}
	got := skillsContext(team, Task{}, skills)
	if count := strings.Count(got, "### Refactor"); count != 1 {
		t.Fatalf("expected exactly one active skill block, got %d:\n%s", count, got)
	}
	if strings.Contains(got, "missing") {
		t.Fatalf("unrecognized skill reference leaked into context:\n%s", got)
	}
}

func TestAgentToolIDsFallsBackToMainAgent(t *testing.T) {
	team := &Team{
		MainAgent: "M",
		Agents: []Agent{
			{Name: "M", Tools: []string{"grep", "file_editor"}},
			{Name: "Other", Tools: []string{"terminal"}},
		},
	}
	if got := agentToolIDs(team, Task{AgentName: "nobody"}); len(got) != 2 || got[0] != "grep" {
		t.Fatalf("expected main agent tools fallback, got %#v", got)
	}
	if got := agentToolIDs(team, Task{AgentName: "Other"}); len(got) != 1 || got[0] != "terminal" {
		t.Fatalf("expected assigned agent tools, got %#v", got)
	}
	if got := agentToolIDs(nil, Task{}); got != nil {
		t.Fatalf("expected nil tools for missing team, got %#v", got)
	}
}
