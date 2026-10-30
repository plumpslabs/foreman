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
	"list_tasks":   true,
	"list_rooms":   true,
	"diagnose":     true,
}

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
}

func newLoopGuard(readBudget, hardRounds int) *loopGuard {
	if readBudget <= 0 {
		readBudget = 40
	}
	if hardRounds <= 0 {
		hardRounds = 60
	}
	return &loopGuard{readBudget: readBudget, hardRounds: hardRounds, toolCounts: map[string]int{}}
}

// decide is the pure state machine: given the next tool name it reports whether
// the call must be blocked (with a message for the model). Split out so it can
// be unit-tested without any LLM or ADK plumbing.
func (g *loopGuard) decide(toolName string) (bool, string) {
	g.toolRounds++
	g.toolCounts[toolName]++

	if g.forced {
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
	return fmt.Sprintf(
		"Blocked %q: %s Stop reading. Now choose one: create a plan (create_plan), delegate (spawn_agent / create_task), make the change, ask the human (ask_human / escalate), or finish with finish_task — or reply with a summary of what you found and what you recommend.",
		toolName, g.reasonOrDefault())
}

func (g *loopGuard) reasonOrDefault() string {
	if g.reason != "" {
		return g.reason
	}
	return "The loop governor stopped further read-only exploration."
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

// plugin wraps the governor as an ADK plugin attached to the runner.
func (g *loopGuard) plugin() (*plugin.Plugin, error) {
	return plugin.New(plugin.Config{
		Name:               "foreman_loop_guard",
		BeforeToolCallback: g.beforeTool,
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

// hasForced reports whether the governor has spent its read-only budget.
func (g *loopGuard) hasForced() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.forced
}

// forcedReason returns the governor's stop reason, or "" if it never fired.
func (g *loopGuard) forcedReason() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.forced {
		return ""
	}
	return g.reasonOrDefault()
}
