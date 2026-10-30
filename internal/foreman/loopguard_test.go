package foreman

import "testing"

func TestLoopGuardBlocksAfterReadBudget(t *testing.T) {
	g := newLoopGuard(3, 60)
	// Three read-only rounds are allowed.
	for i := 0; i < 3; i++ {
		if blocked, _ := g.decide("grep_files"); blocked {
			t.Fatalf("round %d: expected read within budget to be allowed", i+1)
		}
	}
	// The fourth read exceeds the budget and must be blocked.
	blocked, msg := g.decide("view_file")
	if !blocked {
		t.Fatal("expected read past budget to be blocked")
	}
	if msg == "" {
		t.Fatal("expected a non-empty guidance message")
	}
	if !g.hasForced() {
		t.Fatal("expected governor to be forced after exceeding budget")
	}
	// Once forced, even a write action is blocked so the model must summarize
	// instead of silently resuming churn.
	if b, _ := g.decide("create_file"); !b {
		t.Fatal("expected all tools blocked after the governor forced a stop")
	}
}

func TestLoopGuardActionResetsReadBudget(t *testing.T) {
	g := newLoopGuard(3, 60)
	for i := 0; i < 3; i++ {
		g.decide("grep_files")
	}
	// A state-changing action refills the read budget.
	if blocked, _ := g.decide("create_plan"); blocked {
		t.Fatal("action tool must never be blocked before the budget is spent")
	}
	if g.hasForced() {
		t.Fatal("action should have reset the read-only streak")
	}
	for i := 0; i < 3; i++ {
		if blocked, _ := g.decide("view_file"); blocked {
			t.Fatalf("read %d after reset should be allowed", i+1)
		}
	}
}

func TestLoopGuardUnknownToolCountsAsAction(t *testing.T) {
	g := newLoopGuard(2, 60)
	g.decide("grep_files")
	g.decide("grep_files")
	g.decide("grep_files") // exceeds budget -> forced
	// unknown tool is treated as an action, but a forced guard still blocks it.
	if g.forcedReason() == "" {
		t.Fatal("expected a forced reason")
	}
}

func TestIsReadOnlyTool(t *testing.T) {
	reads := []string{"view_file", "grep_files", "repo_map", "mcp__kuma_kuma_context", "mcp__context7_query-docs"}
	for _, n := range reads {
		if !isReadOnlyTool(n) {
			t.Errorf("expected %q to be read-only", n)
		}
	}
	actions := []string{"create_file", "replace_string", "create_task", "spawn_agent", "finish_task", "create_plan", "mcp__fennec_db_query"}
	for _, n := range actions {
		if isReadOnlyTool(n) {
			t.Errorf("expected %q to be an action, not read-only", n)
		}
	}
}

func TestBatchSignatureStable(t *testing.T) {
	a := batchSignature([]string{"view_file", "grep_files"})
	b := batchSignature([]string{"grep_files", "view_file"})
	if a != b {
		t.Fatalf("batch signature must be order-independent: %q vs %q", a, b)
	}
	if a == batchSignature([]string{"view_file"}) {
		t.Fatal("different batches must produce different signatures")
	}
}

func TestEffectiveBudgets(t *testing.T) {
	settings := Settings{MaxIterations: 30, ReadOnlyBudget: 40}
	// Inherit when the agent sets nothing.
	mi, rb := effectiveBudgets(nil, settings)
	if mi != 30 || rb != 40 {
		t.Fatalf("expected inherit (30,40), got (%d,%d)", mi, rb)
	}
	// Per-agent override wins.
	agent := &Agent{MaxIterations: 80, ReadOnlyBudget: 120}
	mi, rb = effectiveBudgets(agent, settings)
	if mi != 80 || rb != 120 {
		t.Fatalf("expected override (80,120), got (%d,%d)", mi, rb)
	}
	// Missing defaults fall back to the built-in values.
	mi, rb = effectiveBudgets(nil, Settings{})
	if mi != 30 || rb != 40 {
		t.Fatalf("expected built-in fallback (30,40), got (%d,%d)", mi, rb)
	}
}
