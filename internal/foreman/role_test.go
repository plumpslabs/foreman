package foreman

import "testing"

func TestResolveRoleType(t *testing.T) {
	team := Team{MainAgent: "Lead", Agents: []Agent{
		{Name: "Lead", Role: "Coordinator"},
		{Name: "Dev", Role: "Backend engineer"},
		{Name: "QA", Role: "Quality reviewer"},
		{Name: "Custom", Role: "Anything", RoleType: "specialist"},
	}}
	cases := []struct {
		name  string
		agent *Agent
		want  string
	}{
		{"Lead", teamAgentByName(team, "Lead"), "coordinator"},
		{"Dev", teamAgentByName(team, "Dev"), "specialist"},
		{"QA", teamAgentByName(team, "QA"), "reviewer"},
		// Explicit RoleType wins even for a role named like a reviewer.
		{"Custom", teamAgentByName(team, "Custom"), "specialist"},
		// Unknown agent with no pointer falls back to specialist.
		{"Ghost", nil, "specialist"},
	}
	for _, tc := range cases {
		if got := resolveRoleType(team, tc.name, tc.agent); got != tc.want {
			t.Errorf("resolveRoleType(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestResolveRoleTypeExplicitReviewer(t *testing.T) {
	team := Team{MainAgent: "Lead", Agents: []Agent{
		{Name: "Lead"},
		{Name: "Gate", RoleType: "reviewer"},
	}}
	if got := resolveRoleType(team, "Gate", teamAgentByName(team, "Gate")); got != "reviewer" {
		t.Errorf("explicit reviewer RoleType not honored: %q", got)
	}
}

func TestEffectiveBudgetsPerAgentOverride(t *testing.T) {
	settings := Settings{MaxIterations: 30, ReadOnlyBudget: 40}
	agent := &Agent{MaxIterations: 120, ReadOnlyBudget: 0}
	mi, rb := effectiveBudgets(agent, settings)
	if mi != 120 {
		t.Errorf("MaxIterations = %d, want 120 (agent override)", mi)
	}
	if rb != 40 {
		t.Errorf("ReadOnlyBudget = %d, want 40 (inherited)", rb)
	}
}
