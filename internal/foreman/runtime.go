package foreman

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Runtime struct {
	store     *Store
	engine    *Engine
	mu        sync.Mutex
	active    map[string]context.CancelFunc
	semaphore chan struct{}
}

func NewRuntime(st *Store) *Runtime {
	return &Runtime{
		store:     st,
		engine:    NewEngine(),
		active:    map[string]context.CancelFunc{},
		semaphore: make(chan struct{}, 8),
	}
}

// SetEngine swaps the bridge engine; used by tests to inject a fake engine.
func (rt *Runtime) SetEngine(e *Engine) { rt.engine = e }

func (rt *Runtime) StartTask(parent context.Context, taskID string) (AgentRun, error) {
	task, room, team, providerConfig, secret, settings, err := rt.store.PrepareTaskRun(taskID)
	if err != nil {
		return AgentRun{}, err
	}
	limit := settings.MaxConcurrentRuns
	if limit < 1 {
		limit = 1
	}
	select {
	case rt.semaphore <- struct{}{}:
	default:
		return AgentRun{}, errors.New("maximum concurrent Foreman runs reached")
	}
	if err := rt.store.MarkTaskRunning(taskID); err != nil {
		<-rt.semaphore
		return AgentRun{}, err
	}
	run := AgentRun{
		ID:        uuid.NewString(),
		TaskID:    task.ID,
		RoomID:    room.ID,
		Provider:  providerConfig.ID,
		Model:     providerConfig.DefaultModel,
		Status:    "running",
		StartedAt: time.Now().UTC(),
		Events:    []RunEvent{{ID: uuid.NewString(), Type: "status", Message: "Agent runtime started", CreatedAt: time.Now().UTC()}},
	}
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
		defer func() {
			cancel()
			rt.mu.Lock()
			delete(rt.active, run.ID)
			rt.mu.Unlock()
			<-rt.semaphore
		}()
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

// execute drives one OpenHands bridge run and persists its events.
func (rt *Runtime) execute(ctx context.Context, run AgentRun, task Task, room Room, team *Team, config ProviderConfig, secret string, settings Settings) AgentRun {
	finish := func(status, message string, err error, tokens int, cost float64) AgentRun {
		run.Status, run.Result, run.EndedAt = status, message, time.Now().UTC()
		if err != nil {
			run.Error = safeRuntimeError(err, secret)
		}
		run.Tokens, run.CostUSD = &tokens, &cost
		run.Events = append(run.Events, RunEvent{
			ID:        uuid.NewString(),
			Type:      status,
			Message:   safeRuntimeError(errors.New(message), secret),
			CreatedAt: time.Now().UTC(),
		})
		return run
	}

	prompt := buildRunPrompt(room, task, team)
	agentToolIDs := agentToolIDs(team, task)
	req := EngineRequest{
		Workspace:            rt.store.Snapshot().ProjectPath,
		Prompt:               prompt,
		Protocol:             config.Protocol,
		BaseURL:              config.BaseURL,
		APIKey:               secret,
		Model:                config.DefaultModel,
		Tools:                ResolveEngineTools(agentToolIDs, settings.AllowProjectCommands),
		MaxIterations:        30,
		BudgetUSD:            settings.BudgetUSD,
		AllowProjectCommands: settings.AllowProjectCommands,
	}
	result, err := rt.engine.Run(ctx, req, func(ev EngineEvent) {
		msg := ev.Message
		if msg == "" {
			msg = ev.Type
		}
		_ = rt.store.AppendRunEvent(run.ID, ev.Type, safeRuntimeError(errors.New(msg), secret))
	})
	if err != nil {
		return finish(result.Status, resultOrError(result.Message, err), err, result.Tokens, result.CostUSD)
	}
	if result.Message == "" {
		result.Message = "Agent run finished."
	}
	if result.Status != "failed" {
		for _, question := range parseAgentQuestions(result.Message) {
			question.ID = uuid.NewString()
			question.CreatedAt = time.Now().UTC()
			_, _ = rt.store.AddRoomQuestion(room.ID, question)
		}
	}
	return finish(result.Status, result.Message, nil, result.Tokens, result.CostUSD)
}

// parseAgentQuestions extracts fenced question blocks from an agent reply:
//
//	```question
//	Which approach should I take?
//	- Option A
//	- Option B
//	```
func parseAgentQuestions(message string) []AgentQuestion {
	var questions []AgentQuestion
	rest := message
	for len(questions) < 5 {
		start := strings.Index(rest, "```question")
		if start < 0 {
			break
		}
		body := rest[start+len("```question"):]
		end := strings.Index(body, "```")
		if end < 0 {
			break
		}
		block := strings.TrimSpace(body[:end])
		rest = body[end+3:]
		var promptParts []string
		var options []string
		for _, line := range strings.Split(block, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
				options = append(options, strings.TrimSpace(trimmed[2:]))
				continue
			}
			if trimmed != "" {
				promptParts = append(promptParts, trimmed)
			}
		}
		prompt := strings.Join(promptParts, " ")
		if prompt == "" {
			continue
		}
		questions = append(questions, AgentQuestion{Prompt: prompt, Options: options})
	}
	return questions
}

func buildRunPrompt(room Room, task Task, team *Team) string {
	sections := []string{
		fmt.Sprintf("Room: %s", room.Name),
		fmt.Sprintf("Room goal: %s", room.Goal),
		fmt.Sprintf("Task: %s", task.Title),
		fmt.Sprintf("Description: %s", task.Description),
	}
	if team != nil {
		sections = append(sections,
			strings.TrimSpace(strings.Join([]string{team.Purpose, team.Instructions, agentInstructions(*team, task.AgentName)}, "\n\n")))
	}
	sections = append(sections,
		"Work only on this task. Keep changes within the active project workspace. Report what changed and what you verified.",
		"Need a decision from the human before continuing? End your reply with a fenced block:\n\n```question\n<the question>\n- option one\n- option two\n```\n\nThen stop and wait for the answer.",
	)
	return strings.Join(sections, "\n\n")
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

// agentToolIDs returns the tool ids for the task's assigned agent, falling back
// to the team's main agent.
func agentToolIDs(team *Team, task Task) []string {
	if team == nil {
		return nil
	}
	target := task.AgentName
	if target == "" {
		target = team.MainAgent
	}
	for _, agent := range team.Agents {
		if agent.Name == target {
			return agent.Tools
		}
	}
	for _, agent := range team.Agents {
		if agent.Name == team.MainAgent {
			return agent.Tools
		}
	}
	return nil
}

func resultOrError(message string, err error) string {
	if message != "" {
		return message
	}
	return err.Error()
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
