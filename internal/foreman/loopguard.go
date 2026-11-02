package foreman

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/tool"
)

// readOnlyTools are tool names that gather information without changing any
// state (files, tasks, the board). An agent may call these freely up to its
// read-only budget; past it, the governor forces it to act.
var readOnlyTools = map[string]bool{
	"view_file":    true,
	"read_file":    true,
	"grep_files":   true,
	"glob_files":   true,
	"search_index": true,
	"repo_map":     true,
	"related":      true,
	"list_tasks":   true,
	"list_rooms":   true,
	"diagnose":     true,
}

// editTools are the native tools that change workspace files. They reset the
// read-only budget and feed the rework tripwire (an edit followed by a still
// failing diagnose counts as one failed fix attempt).
var editTools = map[string]bool{
	"edit_file":      true,
	"replace_string": true,
	"insert_lines":   true,
	"create_file":    true,
}

func isEditTool(name string) bool { return editTools[name] }

// isReadOnlyTool reports whether a tool only reads state. MCP tools are treated
// as read-only only for known context servers (kuma/context7) so an unknown MCP
// server with side-effecting tools is never blocked.
func isReadOnlyTool(name string) bool {
	if readOnlyTools[name] {
		return true
	}
	name = strings.ToLower(name)
	return strings.HasPrefix(name, "mcp__kuma_") || strings.HasPrefix(name, "mcp__context7_")
}

// loopGuard is the per-run exploration governor. It is a soft guardrail: it
// counts consecutive read-only rounds and, once the budget is spent, blocks
// further reads with a synthetic tool result that tells the model to act or
// summarize. Write/action tools are never blocked, so the agent can always
// finish — this keeps the guard from over-constraining capable agents.
//
// Exactly one loopGuard instance is created per run and attached to the runner
// as a plugin, so its state is scoped to that run.
type loopGuard struct {
	mu         sync.Mutex
	readBudget int
	hardRounds int
	toolRounds int
	readRounds int
	forced     bool
	reason     string
	toolCounts map[string]int
	// nudged records which soft-injection thresholds already fired; each
	// nudge costs context exactly once (time-travel rules, after oh-my-pi).
	nudged map[int]bool
	// roleDirective carries the run's role mandate (e.g. the coordinator's
	// "delegate, do not explore"); appended to block messages so native
	// enforcement speaks the same language as the prompt.
	roleDirective string

	// Rework tripwire: after maxRework consecutive edit→still-failing
	// diagnose cycles the governor forces a stop and points at rollback or a
	// human decision instead of letting the agent burn tokens re-fixing the
	// same file in place.
	maxRework        int
	reworkAttempts   int
	editedSinceCheck bool
	reworkForced     bool
	reworkReason     string
}

func newLoopGuard(readBudget, hardRounds int, roleDirective ...string) *loopGuard {
	if readBudget <= 0 {
		readBudget = 40
	}
	if hardRounds <= 0 {
		hardRounds = 60
	}
	return &loopGuard{
		readBudget: readBudget, hardRounds: hardRounds,
		toolCounts: map[string]int{}, nudged: map[int]bool{},
		roleDirective: strings.Join(roleDirective, " "),
		maxRework:     3,
	}
}

// decide is the pure state machine: given the next tool name it reports whether
// the call must be blocked (with a message for the model). Split out so it can
// be unit-tested without any LLM or ADK plumbing.
func (g *loopGuard) decide(toolName string) (bool, string) {
	g.toolRounds++
	g.toolCounts[toolName]++

	if g.forced || g.reworkForced {
		// Once the budget is spent the model should answer; keep blocking tools
		// so it cannot silently resume churning.
		return true, g.blockMessage(toolName)
	}
	if !isReadOnlyTool(toolName) {
		// Any state-changing (or unknown) tool counts as progress and refills
		// the read-only budget.
		g.readRounds = 0
		return false, ""
	}
	g.readRounds++
	if g.readRounds > g.readBudget {
		g.forced = true
		g.reason = fmt.Sprintf(
			"Read-only exploration budget reached (%d rounds without planning, delegating, or editing).",
			g.readBudget)
		return true, g.blockMessage(toolName)
	}
	return false, ""
}

func (g *loopGuard) blockMessage(toolName string) string {
	if g.reworkForced {
		return fmt.Sprintf(
			"Blocked %q: %s Stop fixing in place. Roll back your latest changes with git_ops (status/diff) or ask the human — do not keep retrying the same fix. Then finish with finish_task (summary of what failed) or ask_human.",
			toolName, g.reworkReason)
	}
	msg := fmt.Sprintf(
		"Blocked %q: %s Stop reading. Now choose one: create a plan (create_plan), delegate (spawn_agent / create_task), make the change, ask the human (ask_human / escalate), or finish with finish_task — or reply with a summary of what you found and what you recommend.",
		toolName, g.reasonOrDefault())
	if g.roleDirective != "" {
		msg += "\n" + g.roleDirective
	}
	return msg
}

func (g *loopGuard) reasonOrDefault() string {
	if g.reworkForced {
		return g.reworkReason
	}
	if g.reason != "" {
		return g.reason
	}
	return "The loop governor stopped further read-only exploration."
}

// observeTool feeds the rework tripwire from observed tool calls. It is a pure
// state transition on (tool name, args, result) so it can be unit-tested
// without ADK plumbing:
//   - an edit tool marks that a fix was attempted since the last check;
//   - a passing diagnose/run_checks resets the counter (progress made);
//   - a failing one after an edit counts one failed fix attempt; maxRework of
//     those in a row force a stop with a rollback-oriented message.
func (g *loopGuard) observeTool(name string, args, result map[string]any) {
	if isEditTool(name) {
		g.editedSinceCheck = true
		return
	}
	if name != "diagnose" && name != "run_checks" {
		return
	}
	if toolCheckPassed(name, result) {
		g.reworkAttempts = 0
		return
	}
	if !g.editedSinceCheck {
		return // failing baseline check before any edit is not a failed fix
	}
	g.editedSinceCheck = false
	g.reworkAttempts++
	if g.reworkAttempts >= g.maxRework {
		g.reworkForced = true
		g.reworkReason = fmt.Sprintf(
			"Rework loop detected: %d consecutive edit→still-failing diagnose cycles (%s).",
			g.reworkAttempts, g.stats())
	}
}

// toolCheckPassed reports whether a diagnose/run_checks result means the
// workspace checks passed. Defensive about result shapes from the JSON layer.
func toolCheckPassed(name string, result map[string]any) bool {
	if name == "run_checks" {
		status, _ := result["status"].(string)
		return status == "passed"
	}
	switch v := result["failed"].(type) {
	case int:
		return v == 0
	case int64:
		return v == 0
	case float64:
		return v == 0
	}
	return true // unknown shape: assume a pass so the guard never false-trips
}

// beforeTool adapts decide to the ADK BeforeToolCallback contract: a non-nil
// result is used as the tool result and the real call is skipped.
func (g *loopGuard) beforeTool(_ agent.Context, t tool.Tool, _ map[string]any) (map[string]any, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if t == nil {
		return nil, nil
	}
	blocked, msg := g.decide(t.Name())
	if !blocked {
		return nil, nil
	}
	return map[string]any{
		"status":  "blocked_by_loop_guard",
		"success": false,
		"message": msg,
	}, nil
}

// afterTool injects time-travel-style nudges (the oh-my-pi pattern): instead
// of staying silent until the budget is blown, the guard appends a short note
// to a read-only tool result once at 60% and once at 90% of the read budget —
// course correction that costs context exactly twice, not every turn. The
// underlying result is otherwise unmodified. It also feeds the rework tripwire
// from edit/diagnose results.
func (g *loopGuard) afterTool(_ agent.Context, t tool.Tool, args map[string]any, result map[string]any, _ error) (map[string]any, error) {
	if t == nil || result == nil {
		return nil, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.observeTool(t.Name(), args, result)
	if g.forced || g.reworkForced || !isReadOnlyTool(t.Name()) {
		return nil, nil
	}
	const loopGuardNoteKey = "loop_guard_note"
	thresholds := []struct {
		pct int
		msg string
	}{
		{60, fmt.Sprintf("Loop guard: %d of %d read-only rounds used. Wrap up gathering evidence soon — move to create_plan, an edit, spawn_agent/create_task, or ask_human.", g.readRounds, g.readBudget)},
		{90, fmt.Sprintf("Loop guard: %d of %d read-only rounds — further read-only calls will soon be blocked. Decide and act now.", g.readRounds, g.readBudget)},
	}
	for i, th := range thresholds {
		if float64(g.readRounds) < float64(th.pct)/100*float64(g.readBudget) {
			continue
		}
		if g.nudged[i] {
			continue
		}
		g.nudged[i] = true
		out := make(map[string]any, len(result)+1)
		for k, v := range result {
			out[k] = v
		}
		out[loopGuardNoteKey] = th.msg
		return out, nil
	}
	return nil, nil
}

// plugin wraps the governor as an ADK plugin attached to the runner.
func (g *loopGuard) plugin() (*plugin.Plugin, error) {
	return plugin.New(plugin.Config{
		Name:               "foreman_loop_guard",
		BeforeToolCallback: g.beforeTool,
		AfterToolCallback:  g.afterTool,
	})
}

// pluginConfig returns the runner plugin configuration for this governor.
func (g *loopGuard) pluginConfig() (runner.PluginConfig, error) {
	p, err := g.plugin()
	if err != nil {
		return runner.PluginConfig{}, err
	}
	return runner.PluginConfig{Plugins: []*plugin.Plugin{p}}, nil
}

// stats returns a short human-readable summary of tool usage, used to make a
// hard-stop message informative instead of empty.
func (g *loopGuard) stats() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.toolCounts) == 0 {
		return ""
	}
	type kv struct {
		name string
		n    int
	}
	items := make([]kv, 0, len(g.toolCounts))
	for name, n := range g.toolCounts {
		items = append(items, kv{name, n})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].n != items[j].n {
			return items[i].n > items[j].n
		}
		return items[i].name < items[j].name
	})
	parts := make([]string, 0, 4)
	for i, it := range items {
		if i == 4 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s×%d", it.name, it.n))
	}
	return strings.Join(parts, ", ")
}

// hasForced reports whether the governor has stopped the run (read budget or
// the rework tripwire).
func (g *loopGuard) hasForced() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.forced || g.reworkForced
}

// forcedReason returns the governor's stop reason, or "" if it never fired.
func (g *loopGuard) forcedReason() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.forced && !g.reworkForced {
		return ""
	}
	return g.reasonOrDefault()
}
