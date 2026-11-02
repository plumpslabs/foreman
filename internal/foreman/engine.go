package foreman

import (
	"context"
	"errors"
	"sync"
)

// RunControl lets the runtime ask a live bridge run to pause gracefully.
type RunControl struct {
	mu    sync.Mutex
	pause func()
}

func (c *RunControl) setPause(fn func()) {
	c.mu.Lock()
	c.pause = fn
	c.mu.Unlock()
}

func (c *RunControl) Pause() {
	c.mu.Lock()
	fn := c.pause
	c.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// EngineTool is retained for compatibility with older callers.
type EngineTool struct {
	Name   string `json:"name"`
	Module string `json:"module"`
	Class  string `json:"class"`
}

// EngineRequest describes one bounded agent run.
type EngineRequest struct {
	Workspace            string       `json:"workspace"`
	RoomID               string       `json:"roomId,omitempty"`
	TaskID               string       `json:"taskId,omitempty"`
	AgentName            string       `json:"agentName,omitempty"`
	TriageTier           string       `json:"triageTier,omitempty"`
	StepTimeout          int          `json:"stepTimeout,omitempty"`
	Prompt               string       `json:"prompt"`
	Protocol             string       `json:"protocol"`
	BaseURL              string       `json:"baseUrl"`
	APIKey               string       `json:"apiKey"`
	Model                string       `json:"model"`
	FallbackModels       []string     `json:"fallbackModels,omitempty"`
	Tools                []EngineTool `json:"tools"`
	AllowedTools         []string     `json:"allowedTools,omitempty"`
	MCPServerIDs         []string     `json:"mcpServerIds,omitempty"`
	PersistenceDir       string       `json:"persistenceDir,omitempty"`
	ConversationID       string       `json:"conversationId,omitempty"`
	MaxIterations        int          `json:"maxIterations"`
	ReadOnlyBudget       int          `json:"readOnlyBudget,omitempty"`
	ContextTokenLimit    int          `json:"contextTokenLimit,omitempty"`
	ContextRetention     int          `json:"contextRetention,omitempty"`
	BudgetUSD            float64      `json:"budgetUsd"`
	AllowProjectCommands bool         `json:"allowProjectCommands"`
	// RoleDirective, when set, is appended to loop-guard cut-off messages so
	// native enforcement speaks with the same mandate the prompt carries
	// (e.g. the coordinator's "delegate, do not explore" rule).
	RoleDirective string `json:"roleDirective,omitempty"`
}

// EngineEvent is one streamed update from a run.
type EngineEvent struct {
	Type    string  `json:"type"`
	Message string  `json:"message"`
	Status  string  `json:"status"`
	Tokens  int     `json:"tokens"`
	Cost    float64 `json:"cost"`
}

// EngineResult is the terminal outcome of a run.
type EngineResult struct {
	Status  string
	Message string
	Tokens  int
	CostUSD float64
}

func statusFromContext(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timed_out"
	}
	return "cancelled"
}
