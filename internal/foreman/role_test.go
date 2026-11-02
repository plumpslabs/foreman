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

func TestApplyRoleBudgetCoordinatorCapped(t *testing.T) {
	// Coordinator with teammates: the supervisor must not crawl — capped at
	// the native default when no settings value is configured.
	mi, rb, capped := applyRoleBudget(30, 40, coordinatorReadCapFor(0), "coordinator", true)
	if !capped || rb != defaultCoordinatorReadCap {
		t.Errorf("coordinator read budget = %d capped=%v, want %d true", rb, capped, defaultCoordinatorReadCap)
	}
	if mi != 30 {
		t.Errorf("MaxIterations must not change: %d", mi)
	}
	// A settings-configured bigger cap is honored (scalable, not hardcoded).
	_, rb, capped = applyRoleBudget(30, 40, coordinatorReadCapFor(25), "coordinator", true)
	if !capped || rb != 25 {
		t.Errorf("configured cap = %d capped=%v, want 25 true", rb, capped)
	}
	// Explicit smaller profile budget stays as-is (cap never loosens).
	_, rb, capped = applyRoleBudget(30, 6, coordinatorReadCapFor(25), "coordinator", true)
	if capped || rb != 6 {
		t.Errorf("explicit small budget = %d capped=%v, want 6 false", rb, capped)
	}
	// A negative settings value disables the cap entirely (opt-out).
	_, rb, capped = applyRoleBudget(30, 40, coordinatorReadCapFor(-1), "coordinator", true)
	if capped || rb != 40 {
		t.Errorf("disabled cap = %d capped=%v, want 40 false", rb, capped)
	}
	// Specialists are untouched by the cap.
	_, rb, capped = applyRoleBudget(30, 40, coordinatorReadCapFor(0), "specialist", true)
	if capped || rb != 40 {
		t.Errorf("specialist budget = %d capped=%v, want 40 false", rb, capped)
	}
	// A solo coordinator (nobody to delegate to) keeps the full budget.
	mi, rb, capped = applyRoleBudget(30, 40, coordinatorReadCapFor(0), "coordinator", false)
	if capped || rb != 40 || mi != 30 {
		t.Errorf("solo coordinator = %d/%d capped=%v, want 30/40 false", mi, rb, capped)
	}
}

func TestCoordinatorReadCapFor(t *testing.T) {
	if got := coordinatorReadCapFor(0); got != defaultCoordinatorReadCap {
		t.Errorf("unset = %d, want default %d", got, defaultCoordinatorReadCap)
	}
	if got := coordinatorReadCapFor(18); got != 18 {
		t.Errorf("configured = %d, want 18", got)
	}
	if got := coordinatorReadCapFor(-1); got != 0 {
		t.Errorf("negative = %d, want 0 (disabled)", got)
	}
}

// TestResolveRoleTypeFromRoleTitle guards that behaviour follows the free-text
// role title, so the agent profile needs no second "role type" selector.
func TestResolveRoleTypeFromRoleTitle(t *testing.T) {
	team := Team{MainAgent: "Lead", Agents: []Agent{
		{Name: "Lead", Role: "Coordinator"},
		{Name: "Verifier", Role: "Main agent · Coordinator & verifier"},
		{Name: "Gate", Role: "Code reviewer"},
		{Name: "Auditor", Role: "Security auditor"},
		{Name: "Debugger", Role: "Code debugger"},
		{Name: "Planner", Role: "Planner & scoper"},
	}}
	cases := map[string]string{
		"Lead":     "coordinator", // main agent
		"Verifier": "coordinator", // title mentions Coordinator
		"Gate":     "reviewer",    // title mentions reviewer
		"Auditor":  "reviewer",    // title mentions auditor
		"Debugger": "specialist",
		"Planner":  "coordinator", // planner orchestrates
	}
	for name, want := range cases {
		if got := resolveRoleType(team, name, teamAgentByName(team, name)); got != want {
			t.Errorf("resolveRoleType(%s) = %q, want %q", name, got, want)
		}
	}
}
