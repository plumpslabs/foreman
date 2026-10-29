package loop

import (
	"time"
)

// AgentResult represents the standardized outcome of an agent execution turn.
// Both headless CLI and interactive TUI consumers receive AgentResult upon turn completion.
type AgentResult struct {
	Success          bool          `json:"success"`
	Status           string        `json:"status"` // "completed", "max_iterations", "cost_limit", "cancelled", "failed"
	Message          string        `json:"message"`
	ModifiedFiles    []string      `json:"modified_files"`
	Diffs            []string      `json:"diffs"`
	TotalTokens      int           `json:"total_tokens"`
	ProductiveTokens int           `json:"productive_tokens"`
	CostUSD          float64       `json:"cost_usd"`
	Duration         time.Duration `json:"duration"`
	Rounds           int           `json:"rounds"`
	Mode             string        `json:"mode"`
	FallbackModel    string        `json:"fallback_model,omitempty"`
}
