package foreman

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type activeRun struct {
	cancel  context.CancelFunc
	control *RunControl
}

type Runtime struct {
	store     *Store
	engine    *Engine
	mu        sync.Mutex
	active    map[string]*activeRun
	semaphore chan struct{}
}

func NewRuntime(st *Store) *Runtime {
	return &Runtime{
		store:     st,
		engine:    NewEngine(),
		active:    map[string]*activeRun{},
		semaphore: make(chan struct{}, 8),
	}
}

// SetEngine swaps the bridge engine; used by tests to inject a fake engine.
func (rt *Runtime) SetEngine(e *Engine) { rt.engine = e }

// EngineInstalled reports whether the OpenHands SDK is importable.
func (rt *Runtime) EngineInstalled() (bool, string) {
	return rt.engine.sdkInstalled()
}

// EngineDiagnostics summarizes the engine environment for the dashboard.
func (rt *Runtime) EngineDiagnostics() EngineDiag {
	return rt.engine.diagnostics()
}

// InstallEngine installs the OpenHands SDK into the engine's Python.
func (rt *Runtime) InstallEngine(ctx context.Context) (string, error) {
	return rt.engine.installSDK(ctx)
}

// Exported engine health helpers used by the CLI (cmd/foreman).

// SDKInstalled reports whether the OpenHands SDK is importable.
func (e *Engine) SDKInstalled() (bool, string) { return e.sdkInstalled() }

// PythonVersion returns the engine interpreter's version string.
func (e *Engine) PythonVersion() string { return e.pythonVersion() }

// PipAvailable reports whether pip is available in the engine's Python.
func (e *Engine) PipAvailable() bool {
	return e.diagnostics().PipAvailable
}

// InstallSDK installs the OpenHands SDK (same as the dashboard's install).
func (e *Engine) InstallSDK(ctx context.Context) (string, error) {
	return e.installSDK(ctx)
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
	control := &RunControl{}
	rt.mu.Lock()
	rt.active[run.ID] = &activeRun{cancel: cancel, control: control}
	rt.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			rt.mu.Lock()
			delete(rt.active, run.ID)
			rt.mu.Unlock()
			<-rt.semaphore
		}()
		finished := rt.execute(runCtx, control, run, task, room, team, providerConfig, secret, settings)
		_ = rt.store.FinishRun(finished)
	}()
	return run, nil
}

// CancelRun immediately stops a run (hard kill).
func (rt *Runtime) CancelRun(runID string) error {
	rt.mu.Lock()
	entry := rt.active[runID]
	rt.mu.Unlock()
	if entry == nil {
		return errors.New("run is not active")
	}
	entry.cancel()
	return nil
}

// PauseRun asks a run to stop at the next step boundary. The conversation state
// is persisted so the task can be continued later.
func (rt *Runtime) PauseRun(runID string) error {
	rt.mu.Lock()
	entry := rt.active[runID]
	rt.mu.Unlock()
	if entry == nil {
		return errors.New("run is not active")
	}
	if entry.control == nil {
		return errors.New("run cannot be paused")
	}
	entry.control.Pause()
	return nil
}

// execute drives one OpenHands bridge run and persists its events.
func (rt *Runtime) execute(ctx context.Context, control *RunControl, run AgentRun, task Task, room Room, team *Team, config ProviderConfig, secret string, settings Settings) AgentRun {
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

	prompt := buildRunPrompt(room, task, team, skillsContext(team, task, rt.store.Snapshot().Skills))
	agentToolIDs := agentToolIDs(team, task)
	snapshot := rt.store.Snapshot()
	req := EngineRequest{
		Workspace:            snapshot.ProjectPath,
		Prompt:               prompt,
		Protocol:             config.Protocol,
		BaseURL:              config.BaseURL,
		APIKey:               secret,
		Model:                config.DefaultModel,
		Tools:                ResolveEngineTools(agentToolIDs, settings.AllowProjectCommands),
		PersistenceDir:       filepath.Join(snapshot.ProjectPath, ".foreman", "agent-conversations"),
		ConversationID:       task.ID,
		MaxIterations:        30,
		BudgetUSD:            settings.BudgetUSD,
		AllowProjectCommands: settings.AllowProjectCommands,
	}
	result, err := rt.engine.RunControlled(ctx, req, func(ev EngineEvent) {
		msg := ev.Message
		if msg == "" {
			msg = ev.Type
		}
		_ = rt.store.AppendRunEvent(run.ID, ev.Type, safeRuntimeError(errors.New(msg), secret))
	}, control)
	if err != nil {
		final := finish(result.Status, resultOrError(result.Message, err), err, result.Tokens, result.CostUSD)
		if task.Hidden && final.Result != "" {
			_ = rt.store.AppendAgentReply(room.ID, task.AgentName, final.Result)
		}
		return final
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
	final := finish(result.Status, result.Message, nil, result.Tokens, result.CostUSD)
	if task.Hidden && final.Result != "" {
		_ = rt.store.AppendAgentReply(room.ID, task.AgentName, final.Result)
	}
	return final
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

func buildRunPrompt(room Room, task Task, team *Team, skills string) string {
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
	if skills != "" {
		sections = append(sections, "# Skills\n\n"+skills)
	}
	sections = append(sections,
		"Work only on this task. Keep changes within the active project workspace. Report what changed and what you verified.",
		"Need a decision from the human before continuing? End your reply with a fenced block:\n\n```question\n<the question>\n- option one\n- option two\n```\n\nThen stop and wait for the answer.",
	)
	return strings.Join(sections, "\n\n")
}

// skillsContext renders the assigned agent's skills into the run prompt.
// Active skills contribute their instructions (bounded); passive skills are
// listed by name as reference only.
func skillsContext(team *Team, task Task, skills []Skill) string {
	refs := agentSkillRefs(team, task)
	if len(refs) == 0 || len(skills) == 0 {
		return ""
	}
	byRef := make(map[string]Skill, len(skills)*2)
	for _, skill := range skills {
		byRef[skill.ID] = skill
		byRef[skill.Name] = skill
	}
	var active, passive []Skill
	seen := map[string]bool{}
	for _, ref := range refs {
		skill, ok := byRef[ref]
		if !ok || seen[skill.ID] {
			continue
		}
		seen[skill.ID] = true
		if skill.Mode == SkillModeActive {
			active = append(active, skill)
		} else {
			passive = append(passive, skill)
		}
	}

	var builder strings.Builder
	if len(active) > 0 {
		builder.WriteString("## Active skills\n\n")
		budget := 8000
		for _, skill := range active {
			body := strings.TrimSpace(skill.Instructions)
			var files strings.Builder
			for _, file := range skill.Files {
				if strings.EqualFold(file.Path, "SKILL.md") {
					continue
				}
				files.WriteString("- " + file.Path + "\n")
			}
			section := "### " + skill.Name + "\n" + body
			if files.Len() > 0 {
				section += "\n\nBundle files:\n" + files.String()
			}
			if len(section) > budget {
				section = section[:budget] + "\n…(truncated)"
			}
			builder.WriteString(section + "\n\n")
			budget -= len(section)
			if budget <= 0 {
				break
			}
		}
	}
	if len(passive) > 0 {
		builder.WriteString("## Reference skills (passive)\n")
		for _, skill := range passive {
			desc := strings.TrimSpace(skill.Description)
			if desc != "" {
				builder.WriteString("- " + skill.Name + ": " + desc + "\n")
			} else {
				builder.WriteString("- " + skill.Name + "\n")
			}
		}
	}
	return strings.TrimSpace(builder.String())
}

func agentSkillRefs(team *Team, task Task) []string {
	if team == nil {
		return nil
	}
	target := task.AgentName
	if target == "" {
		target = team.MainAgent
	}
	for _, agent := range team.Agents {
		if agent.Name == target {
			return agent.Skills
		}
	}
	for _, agent := range team.Agents {
		if agent.Name == team.MainAgent {
			return agent.Skills
		}
	}
	return nil
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
