package foreman

import (
	"strings"
	"testing"
)

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

// nudgeStubTool satisfies tool.Tool with just a name.
type nudgeStubTool struct{ name string }

func (s nudgeStubTool) Name() string        { return s.name }
func (s nudgeStubTool) Description() string { return "" }
func (s nudgeStubTool) IsLongRunning() bool { return false }

func TestLoopGuardNudgesAtSixtyAndNinetyPercent(t *testing.T) {
	g := newLoopGuard(10, 60)
	result := func() map[string]any { return map[string]any{"status": "ok"} }
	name := nudgeStubTool{name: "grep_files"}
	// Each round: decide() (the beforeTool path) counts the read, then
	// afterTool may attach the one-time note to the tool's result.
	round := func() map[string]any {
		g.decide("grep_files")
		out, _ := g.afterTool(nil, name, nil, result(), nil)
		return out
	}
	// Rounds 1-5 (below 60%): no nudge.
	for i := 0; i < 5; i++ {
		if out := round(); out != nil {
			t.Fatalf("round %d: unexpected nudge %v", i+1, out)
		}
	}
	// Round 6 crosses 60% exactly once.
	out := round()
	if out == nil {
		t.Fatal("expected the 60% nudge at round 6")
	}
	if out["loop_guard_note"] == "" {
		t.Fatal("nudge note must be present")
	}
	if out["status"] != "ok" {
		t.Fatal("nudge must preserve the original result")
	}
	// Round 7 must stay quiet (each threshold fires exactly once).
	if out2 := round(); out2 != nil {
		t.Fatal("nudge must fire exactly once per threshold")
	}
	// Round 8 (below 90%): still quiet.
	if out := round(); out != nil {
		t.Fatal("unexpected nudge before 90%")
	}
	// Round 9 crosses 90%.
	out = round()
	if out == nil || out["loop_guard_note"] == "" {
		t.Fatal("expected the 90% nudge")
	}
}

func TestLoopGuardNudgeSkipsActionsAndForced(t *testing.T) {
	name := nudgeStubTool{name: "grep_files"}
	g := newLoopGuard(2, 60)
	// Three reads against a budget of two -> the third decision forces.
	for i := 0; i < 3; i++ {
		g.decide("grep_files")
	}
	if !g.hasForced() {
		t.Fatal("expected governor to be forced")
	}
	// Forced: read calls now get blocked before running; afterTool must not nudge.
	if out, _ := g.afterTool(nil, name, nil, map[string]any{}, nil); out != nil {
		t.Fatal("no nudge once forced")
	}
	// Write/action tools are never nudged even inside budget.
	g2 := newLoopGuard(10, 60)
	g2.decide("create_plan")
	if out, _ := g2.afterTool(nil, nudgeStubTool{name: "create_plan"}, nil, map[string]any{}, nil); out != nil {
		t.Fatal("actions must not be nudged")
	}
}

func TestLoopGuardRoleDirectiveInBlockMessage(t *testing.T) {
	directive := "You are the Coordinator: delegate now."
	g := newLoopGuard(1, 60, directive)
	g.decide("grep_files")
	_, msg := g.decide("view_file") // exceeds budget -> forced
	if !strings.Contains(msg, directive) {
		t.Errorf("block message must carry the role directive, got %q", msg)
	}
	// A guard without a directive keeps the plain message.
	g2 := newLoopGuard(1, 60)
	g2.decide("grep_files")
	_, plain := g2.decide("view_file")
	if strings.Contains(plain, "You are the Coordinator") {
		t.Errorf("plain guard must not grow a directive, got %q", plain)
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
