package foreman

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Engine is retained for CLI/backwards compatibility. Foreman now runs agents
// natively through ADKEngine (Google ADK Go); this Engine no longer spawns a
// Python bridge.
type Engine struct {
	python string

	probeMu         sync.Mutex
	cachedInstalled bool
	cachedProbe     string
	cachedAt        time.Time
}

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
	BudgetUSD            float64      `json:"budgetUsd"`
	AllowProjectCommands bool         `json:"allowProjectCommands"`
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

// EngineDiag describes the engine environment for the dashboard.
type EngineDiag struct {
	PythonVersion string
	PipAvailable  bool
	Installed     bool
	ProbeDetail   string
}

func NewEngine() *Engine {
	if py := strings.TrimSpace(os.Getenv("FOREMAN_PYTHON")); py != "" {
		return &Engine{python: py}
	}
	return &Engine{}
}

func (e *Engine) sdkInstalled() (bool, string) { return true, "adk go native" }

func (e *Engine) sdkInstalledCached() (bool, string) { return true, "adk go native" }

func (e *Engine) pythonVersion() string { return "go (adk)" }

func (e *Engine) diagnostics() EngineDiag {
	return EngineDiag{PythonVersion: "go (adk)", PipAvailable: false, Installed: true, ProbeDetail: "adk go native"}
}

func (e *Engine) installSDK(ctx context.Context) (string, error) { return "", nil }

func (e *Engine) Run(ctx context.Context, req EngineRequest, emit func(EngineEvent)) (EngineResult, error) {
	return EngineResult{Status: "completed", Message: "ADK Go engine (native)"}, nil
}

func (e *Engine) RunControlled(ctx context.Context, req EngineRequest, emit func(EngineEvent), control *RunControl) (EngineResult, error) {
	return e.Run(ctx, req, emit)
}

// Exported compatibility helpers.
func (e *Engine) SDKInstalled() (bool, string) { return true, "adk go native" }
func (e *Engine) PythonVersion() string        { return "go (adk)" }
func (e *Engine) PipAvailable() bool           { return false }
func (e *Engine) InstallSDK(ctx context.Context) (string, error) {
	return "", nil
}

func classifyInstallError(err error, out, pyVer string) (reason, hint string) {
	low := strings.ToLower(out + "\n" + err.Error())
	switch {
	case strings.Contains(low, "externally-managed"):
		return "Python environment ini dikelola sistem (PEP 668).", "Gunakan Google ADK Go (native) — tidak perlu Python."
	case strings.Contains(low, "requires-python"):
		return "SDK butuh Python >= 3.12.", "Gunakan Google ADK Go (native) — tidak perlu Python."
	case strings.Contains(low, "no module named pip"):
		return "pip tidak tersedia.", "Gunakan Google ADK Go (native) — tidak perlu Python."
	case strings.Contains(low, "permission denied") || strings.Contains(low, "not permitted"):
		return "Izin kurang.", "Gunakan Google ADK Go (native) — tidak perlu Python."
	}
	return "", ""
}

var _ = exec.Command
var _ = filepath.Join
var _ = errors.New
var _ = time.Second
var _ = sync.Mutex{}
var _ = fmt.Sprintf

func statusFromContext(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timed_out"
	}
	return "cancelled"
}
