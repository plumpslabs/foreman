package foreman

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	bcontext "github.com/plumpslabs/bro-code/internal/context"
	"github.com/plumpslabs/bro-code/internal/loop"
	"github.com/plumpslabs/bro-code/internal/provider"
	"github.com/plumpslabs/bro-code/internal/search"
	"github.com/plumpslabs/bro-code/internal/store"
	"github.com/plumpslabs/bro-code/internal/tool"
)

type Runtime struct {
	store     *Store
	mu        sync.Mutex
	active    map[string]context.CancelFunc
	semaphore chan struct{}
}

func NewRuntime(st *Store) *Runtime {
	settings := st.Snapshot().Settings
	if settings.MaxConcurrentRuns < 1 {
		settings.MaxConcurrentRuns = 1
	}
	return &Runtime{store: st, active: map[string]context.CancelFunc{}, semaphore: make(chan struct{}, 8)}
}

func (rt *Runtime) StartTask(parent context.Context, taskID string) (AgentRun, error) {
	task, room, team, providerConfig, secret, settings, err := rt.store.PrepareTaskRun(taskID)
	if err != nil {
		return AgentRun{}, err
	}
	limit := settings.MaxConcurrentRuns
	if limit < 1 {
		limit = 1
	}
	rt.mu.Lock()
	if len(rt.semaphore) >= limit {
		rt.mu.Unlock()
		return AgentRun{}, errors.New("maximum concurrent Foreman runs reached")
	}
	rt.mu.Unlock()
	select {
	case rt.semaphore <- struct{}{}:
	default:
		return AgentRun{}, errors.New("maximum concurrent Foreman runs reached")
	}
	if err := rt.store.MarkTaskRunning(taskID); err != nil {
		<-rt.semaphore
		return AgentRun{}, err
	}
	run := AgentRun{ID: uuid.NewString(), TaskID: task.ID, RoomID: room.ID, Provider: providerConfig.ID, Model: providerConfig.DefaultModel, Status: "running", StartedAt: time.Now().UTC(), Events: []RunEvent{{ID: uuid.NewString(), Type: "status", Message: "Agent runtime started", CreatedAt: time.Now().UTC()}}}
	if err := rt.store.CreateRun(run); err != nil {
		<-rt.semaphore
		_, _ = rt.store.UpdateTaskStatus(taskID, "todo", false)
		return AgentRun{}, err
	}
	runCtx, cancel := context.WithTimeout(parent, time.Duration(settings.MaxExecutionSeconds)*time.Second)
	rt.mu.Lock()
	rt.active[run.ID] = cancel
	rt.mu.Unlock()
	go func() {
		defer func() { cancel(); rt.mu.Lock(); delete(rt.active, run.ID); rt.mu.Unlock(); <-rt.semaphore }()
		finished := rt.execute(runCtx, run, task, room, team, providerConfig, secret, settings)
		_ = rt.store.FinishRun(finished)
	}()
	return run, nil
}

func (rt *Runtime) CancelRun(runID string) error {
	rt.mu.Lock()
	cancel := rt.active[runID]
	rt.mu.Unlock()
	if cancel == nil {
		return errors.New("run is not active")
	}
	cancel()
	return nil
}

func (rt *Runtime) execute(ctx context.Context, run AgentRun, task Task, room Room, team *Team, config ProviderConfig, secret string, settings Settings) AgentRun {
	finish := func(status, message string, err error, tokens int, cost float64) AgentRun {
		run.Status, run.Result, run.EndedAt = status, message, time.Now().UTC()
		if err != nil {
			run.Error = safeRuntimeError(err, secret)
		}
		run.Tokens, run.CostUSD = &tokens, &cost
		run.Events = append(run.Events, RunEvent{ID: uuid.NewString(), Type: status, Message: safeRuntimeError(errors.New(message), secret), CreatedAt: time.Now().UTC()})
		return run
	}
	root, err := filepath.Abs(rt.store.Snapshot().ProjectPath)
	if err != nil {
		return finish("failed", "resolve workspace", err, 0, 0)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return finish("failed", "resolve process directory", err, 0, 0)
	}
	if workingDir != root {
		return finish("failed", "runtime must start from its workspace root", fmt.Errorf("runtime working directory %q does not match workspace root", workingDir), 0, 0)
	}
	db, err := store.NewStore(filepath.Join(root, ".foreman", "agent.db"))
	if err != nil {
		return finish("failed", "open local agent store", err, 0, 0)
	}
	defer db.Close()
	sessionID := uuid.NewString()
	if err := db.CreateSession(sessionID, root); err != nil {
		return finish("failed", "create isolated agent session", err, 0, 0)
	}
	ctxMgr := bcontext.NewManager(sessionID, db, 128000)
	tools := tool.NewRegistry()
	tools.SetRepoRoot(root)
	if err := tools.SetWorkspaceBoundary(root); err != nil {
		return finish("failed", "configure workspace boundary", err, 0, 0)
	}
	sandbox := tool.LoadSandbox(root)
	if sandbox == nil {
		sandbox = &tool.Sandbox{}
	}
	sandbox.Deny = append(sandbox.Deny, "bash", "git", "fetch_url", "web_search", "doc_lookup", "spawn_task", "switch_mode", "review_changes", "ask_user", "delete_file", "undo")
	tools.SetSandbox(sandbox)
	tools.SetExecutionPolicy(false, true)
	// Running the existing file tools safely requires the process to be
	// launched from the exact workspace root. Project shell commands remain
	// blocked: Foreman has no interactive command-approval UI yet.
	adapter := makeAdapter(config, secret)
	engine := loop.NewEngine(adapter, tools, ctxMgr, config.DefaultModel)
	engine.SetMaxIterations(10)
	if settings.BudgetUSD > 0 {
		engine.SetBudgetUSD(settings.BudgetUSD)
	}
	engine.SetProjectContext(search.BuildProjectContext(root).String())
	if team != nil {
		engine.SetAgentPrompt(strings.Join([]string{team.Purpose, team.Instructions, agentInstructions(*team, task.AgentName)}, "\n\n"))
	}
	engine.Subscribe(func(event loop.AgentEvent) {
		msg := eventMessage(event)
		if msg != "" {
			_ = rt.store.AppendRunEvent(run.ID, string(event.Type), safeRuntimeError(errors.New(msg), secret))
		}
	})
	prompt := fmt.Sprintf("Room: %s\nRoom goal: %s\nTask: %s\nDescription: %s\n\nWork only on this task. Keep changes within the active project workspace. Report what changed and what you verified.", room.Name, room.Goal, task.Title, task.Description)
	result, runErr := engine.RunTask(ctx, prompt)
	if runErr != nil {
		status := "failed"
		if errors.Is(ctx.Err(), context.Canceled) {
			status = "cancelled"
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = "timed_out"
		}
		return finish(status, resultMessage(result), runErr, resultTokens(result), resultCost(result))
	}
	if result == nil {
		return finish("failed", "agent returned no result", errors.New("agent returned no result"), 0, 0)
	}
	status := result.Status
	if status == "completed" && result.Success {
		status = "completed"
	} else if status == "blocked" {
		status = "blocked"
	} else {
		status = "failed"
	}
	return finish(status, result.Message, nil, result.TotalTokens, result.CostUSD)
}

func makeAdapter(config ProviderConfig, secret string) provider.ProviderAdapter {
	if config.Protocol == "anthropic" {
		return provider.NewAnthropicAdapter(config.BaseURL, secret)
	}
	return provider.NewOpenAIAdapter(config.BaseURL, secret)
}

func agentInstructions(team Team, name string) string {
	for _, agent := range team.Agents {
		if agent.Name == name {
			return agent.Role + "\n" + agent.Instructions
		}
	}
	for _, agent := range team.Agents {
		if agent.Name == team.MainAgent {
			return agent.Role + "\n" + agent.Instructions
		}
	}
	return ""
}

func resultMessage(r *loop.AgentResult) string {
	if r == nil {
		return "agent execution ended without a result"
	}
	return r.Message
}
func resultTokens(r *loop.AgentResult) int {
	if r == nil {
		return 0
	}
	return r.TotalTokens
}
func resultCost(r *loop.AgentResult) float64 {
	if r == nil {
		return 0
	}
	return r.CostUSD
}

func eventMessage(ev loop.AgentEvent) string {
	switch v := ev.Payload.(type) {
	case string:
		return v
	case loop.ProgressPayload:
		return v.Info
	case loop.ToolCallPayload:
		return "Tool call: " + v.ToolName
	case loop.ToolResultPayload:
		if v.Error != "" {
			return "Tool " + v.ToolName + " failed: " + v.Error
		}
		return "Tool completed: " + v.ToolName
	case loop.VerificationPayload:
		if v.Passed {
			return "Verified: " + v.Command
		}
		return "Verification failed: " + v.Command + " — " + v.Output
	case loop.FilePatchPayload:
		return "File " + v.Action + ": " + v.Path
	case loop.AgentResult:
		return v.Status + ": " + v.Message
	default:
		return ""
	}
}

func safeRuntimeError(err error, secret string) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if secret != "" {
		text = strings.ReplaceAll(text, secret, "[REDACTED]")
	}
	return text
}
