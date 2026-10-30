package foreman

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type activeRun struct {
	cancel  context.CancelFunc
	control *RunControl
}

// RunEngine is the minimal engine surface the runtime depends on. The real
// implementation is *ADKEngine; tests inject a fake to drive orchestration
// deterministically without calling any LLM.
type RunEngine interface {
	RunControlled(ctx context.Context, req EngineRequest, emit func(EngineEvent), control *RunControl) (EngineResult, error)
	sdkInstalledCached() (bool, string)
	diagnostics() EngineDiag
	installSDK(ctx context.Context) (string, error)
}

type Runtime struct {
	store       *Store
	engine      RunEngine
	mu          sync.Mutex
	active      map[string]*activeRun
	activeCount int
	// watchdogStop halts the background heartbeat watchdog.
	watchdogStop chan struct{}
	stopOnce     sync.Once
}

func NewRuntime(st *Store) *Runtime {
	rt := &Runtime{
		store:        st,
		engine:       NewADKEngine(st),
		active:       map[string]*activeRun{},
		watchdogStop: make(chan struct{}),
	}
	// Wake queued spawned/review tasks the moment they are enqueued, instead of
	// waiting for another run to finish. EnqueueAutoStart fires this hook.
	if st != nil {
		st.SetOnAutoStart(func() { go rt.drainAutoStart() })
	}
	rt.startWatchdog()
	return rt
}

// stopWatchdog halts the background heartbeat watchdog (used by tests to keep
// goroutine-leak checks clean; production keeps it running for the process).
func (rt *Runtime) stopWatchdog() {
	rt.stopOnce.Do(func() { close(rt.watchdogStop) })
}

func (rt *Runtime) startWatchdog() {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-rt.watchdogStop:
				return
			case <-ticker.C:
			}
			if rt.store == nil || rt.store.DB() == nil {
				continue
			}
			for _, room := range rt.store.Snapshot().Rooms {
				stalled, err := rt.store.DB().CheckStalledAgents(room.ID, 45*time.Second)
				if err != nil || len(stalled) == 0 {
					continue
				}
				supervisor := rt.store.RoomMainAgentName(room.ID)
				if supervisor == "" {
					supervisor = "Supervisor"
				}
				for _, agent := range stalled {
					_ = rt.store.RecordActivity(room.ID, "Watchdog", "watchdog.stalled",
						fmt.Sprintf("⚠️ Heartbeat alert: Agent @%s stalled (>45s without heartbeat). Supervisor @%s notified.", agent, supervisor),
						nil)
				}
			}
		}
	}()
}

// SetEngine swaps the bridge engine; used by tests to inject a fake engine.
func (rt *Runtime) SetEngine(e RunEngine) { rt.engine = e }

// EngineInstalled reports whether the OpenHands SDK is importable.
func (rt *Runtime) EngineInstalled() (bool, string) {
	return rt.engine.sdkInstalledCached()
}

// EngineDiagnostics summarizes the engine environment for the dashboard.
func (rt *Runtime) EngineDiagnostics() EngineDiag {
	return rt.engine.diagnostics()
}

// InstallEngine installs the OpenHands SDK into the engine's Python.
func (rt *Runtime) InstallEngine(ctx context.Context) (string, error) {
	return rt.engine.installSDK(ctx)
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
	if limit > 8 {
		limit = 8
	}
	// Reserve a concurrency slot, honoring the configured run limit. activeCount
	// is the real cap (the old fixed-size semaphore ignored the setting).
	rt.mu.Lock()
	if rt.activeCount >= limit {
		rt.mu.Unlock()
		return AgentRun{}, errors.New("maximum concurrent Foreman runs reached")
	}
	rt.activeCount++
	rt.mu.Unlock()
	release := func() {
		rt.mu.Lock()
		rt.activeCount--
		rt.mu.Unlock()
	}
	if err := rt.store.MarkTaskRunning(taskID); err != nil {
		release()
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
		release()
		_, _ = rt.store.UpdateTaskStatus(taskID, "todo", false)
		return AgentRun{}, err
	}
	runCtx, cancel := context.WithTimeout(parent, time.Duration(settings.MaxExecutionSeconds)*time.Second)
	control := &RunControl{}
	rt.mu.Lock()
	rt.active[run.ID] = &activeRun{cancel: cancel, control: control}
	rt.mu.Unlock()
	go func() {
		var finished AgentRun
		defer func() {
			cancel()
			rt.mu.Lock()
			delete(rt.active, run.ID)
			rt.mu.Unlock()
			release()
			// Wake any spawned/delegated tasks now that a run slot is free.
			go rt.drainAutoStart()
		}()
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("agent run goroutine panic", "err", fmt.Sprint(rec), "run", run.ID[:8], "task", task.ID[:8])
				if finished.ID == "" {
					finished = AgentRun{
						ID: run.ID, TaskID: task.ID, RoomID: room.ID,
						Provider: providerConfig.ID, Model: run.Model,
						Status: "failed", Error: "agent panic: " + fmt.Sprint(rec),
						StartedAt: run.StartedAt, EndedAt: time.Now().UTC(),
					}
				}
				_ = rt.store.FinishRun(finished)
			}
		}()
		finished = rt.execute(runCtx, control, run, task, room, team, providerConfig, secret, settings)
		_ = rt.store.FinishRun(finished)
	}()
	return run, nil
}

// drainAutoStart starts queued spawned tasks (FIFO) as capacity allows. When
// the global run limit is reached it requeues the remainder and stops; the
// next finished run tries again.
func (rt *Runtime) drainAutoStart() {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("drainAutoStart panic", "err", fmt.Sprint(rec))
		}
	}()
	for {
		id := rt.store.PopAutoStart()
		if id == "" {
			return
		}
		_, err := rt.StartTask(context.Background(), id)
		if err != nil && strings.Contains(err.Error(), "maximum concurrent") {
			rt.store.requeueAutoStart(id)
			return
		}
		if err != nil {
			// Permanent failure (task deleted/unknown) — drop and keep draining.
			continue
		}
	}
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
		if status := rt.store.RunStatus(runID); status != "" && status != "running" {
			return fmt.Errorf("run is already %s", status)
		}
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

	agentToolIDs := agentToolIDs(team, task)
	runAgent := activeTeamAgent(team, task)
	maxIterations, readOnlyBudget := effectiveBudgets(runAgent, settings)
	contextTokenLimit := settings.CompactionThreshold
	if settings.DisableCompaction {
		contextTokenLimit = 0
	}
	prompt := buildRunPrompt(room, task, team, skillsContext(team, task, rt.store.Snapshot().Skills), agentToolIDs)
	prompt += fmt.Sprintf("\n\n## Exploration budget\nYou may spend up to %d read-only tool rounds (view/grep/search/repo_map/list) before you must act on what you found. Do not re-read the same file or region — if a search or view already answered the question, act on it. Before the budget runs out, choose one: create_plan, spawn_agent / create_task, make the code change, ask_human, or finish_task. Once the budget is spent, further read-only tools are blocked automatically; if that happens, reply with a concise summary of findings and your recommendation instead of retrying reads.", readOnlyBudget)
	if mcpServers := rt.store.MCPServersByIDs(room.MCPServerIDs); len(mcpServers) > 0 {
		names := make([]string, 0, len(mcpServers))
		for _, srv := range mcpServers {
			names = append(names, srv.Name)
		}
		prompt += "\n\n## External tools (MCP)\nServers available in this room: " + strings.Join(names, ", ") +
			".\nTheir tools are exposed as `mcp__<server>_<tool>`. Use them when they are the right tool for the job (e.g. querying a connected service, database, or documentation) instead of guessing or fabricating data." +
			"\nIf a context/memory server (e.g. Kuma) is listed, call its read/init action for the project FIRST (e.g. `mcp__kuma_kuma_context` with action=`init`, then `research` on the area you are about to touch) before editing unfamiliar code."
	}
	// Every run — chat threads and delegated/spawned tasks alike — gets the
	// recent room conversation so the agent keeps context of what was said.
	if recent := rt.store.RoomRecentContext(room.ID); recent != "" {
		prompt += "\n\n## Recent room conversation\n\n" + recent
	}
	_ = rt.store.AppendRoomNotice(room.ID, task.AgentName, "run.started",
		fmt.Sprintf("🚀 @%s started working on: %s", task.AgentName, task.Title))
	slog.Info("run started", "room", room.ID[:8], "task", task.ID[:8], "agent", task.AgentName)
	snapshot := rt.store.Snapshot()
	primaryModel, fallbackModels := resolveModelChain(config, team, task)

	var result EngineResult
	var lastErr error
	maxRetries := settings.MaxRetries
	if maxRetries < 1 {
		maxRetries = 3
	}
	attempt := 0
	attemptsUsed := 0

	// Model chain cycles across attempts so a rate-limited primary model is not
	// retried with itself: attempt 0 uses the primary, attempt 1 the first
	// fallback, and so on, wrapping around.
	modelChain := append([]string{primaryModel}, fallbackModels...)
	if len(modelChain) == 0 || modelChain[0] == "" {
		modelChain = []string{config.DefaultModel}
	}

	for ; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := backoffForAttempt(lastErr, attempt)
			_ = rt.store.RecordActivity(room.ID, task.AgentName, "retry",
				fmt.Sprintf("⚠️ Run attempt %d failed (%v). Retrying in %v...", attempt, lastErr, backoff),
				nil)
			_ = rt.store.AppendRoomNotice(room.ID, task.AgentName, "run.retry",
				fmt.Sprintf("⚠️ Attempt %d failed (%v). Retrying in %v…", attempt, lastErr, backoff))
			time.Sleep(backoff)
		}

		// Rotate to the next model in the chain on every retry.
		modelForAttempt := modelChain[attempt%len(modelChain)]
		if attempt > 0 && modelForAttempt != primaryModel {
			_ = rt.store.AppendRoomNotice(room.ID, task.AgentName, "run.model_escalate",
				fmt.Sprintf("🔁 Retry %d using model %s", attempt, modelForAttempt))
		}

		req := EngineRequest{
			Workspace:            snapshot.ProjectPath,
			RoomID:               room.ID,
			TaskID:               task.ID,
			AgentName:            task.AgentName,
			TriageTier:           room.TriageTier,
			StepTimeout:          60,
			Prompt:               prompt,
			Protocol:             config.Protocol,
			BaseURL:              config.BaseURL,
			APIKey:               secret,
			Model:                modelForAttempt,
			FallbackModels:       fallbackModels,
			Tools:                ResolveEngineTools(agentToolIDs, settings.AllowProjectCommands),
			AllowedTools:         agentToolIDs,
			PersistenceDir:       filepath.Join(snapshot.ProjectPath, ".foreman", "agent-conversations"),
			MCPServerIDs:         room.MCPServerIDs,
			ConversationID:       conversationID(room.ID, task),
			MaxIterations:        maxIterations,
			ReadOnlyBudget:       readOnlyBudget,
			ContextTokenLimit:    contextTokenLimit,
			ContextRetention:     settings.CompactionRetention,
			BudgetUSD:            settings.BudgetUSD,
			AllowProjectCommands: settings.AllowProjectCommands,
		}

		result, lastErr = rt.engine.RunControlled(ctx, req, func(ev EngineEvent) {
			msg := ev.Message
			if msg == "" {
				msg = ev.Type
			}
			_ = rt.store.AppendRunEvent(run.ID, ev.Type, safeRuntimeError(errors.New(msg), secret))
		}, control)
		attemptsUsed = attempt + 1

		// Break immediately when the whole-run budget/deadline is gone or the
		// run was stopped: retrying can only fail again and burns time.
		if lastErr == nil || ctx.Err() != nil || result.Status == "paused" || result.Status == "blocked" {
			break
		}
	}

	if result.Status == "paused" && task.Hidden {
		_, _ = rt.store.UpdateTaskStatus(task.ID, "paused", false)
	}
	if lastErr != nil && result.Status != "paused" {
		supervisor := rt.store.RoomMainAgentName(room.ID)
		if supervisor == "" {
			supervisor = "Supervisor"
		}
		finalStatus := "blocked"
		noticeType := "task.blocked"
		noticeText := fmt.Sprintf("⛔ Task %s blocked after %d attempts: %v", task.Title, attemptsUsed, lastErr)
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			finalStatus = "timed_out"
			noticeType = "run.timed_out"
			noticeText = fmt.Sprintf("⏱️ Run stopped: the execution time limit (%ds) was reached. Send a narrower prompt to continue, or raise Max Execution Seconds in Settings.", settings.MaxExecutionSeconds)
		case finalStatus == "cancelled" || result.Status == "cancelled":
			finalStatus = "cancelled"
			noticeType = "run.cancelled"
			noticeText = fmt.Sprintf("🛑 Run stopped by you before finishing. The task is marked blocked — send a new prompt to continue.")
		}
		_ = rt.store.RecordActivity(room.ID, task.AgentName, "task.blocked",
			fmt.Sprintf("🚨 Task blocked after %d attempts: %v. Supervisor @%s notified.", attemptsUsed, lastErr, supervisor),
			nil)
		_, _ = rt.store.UpdateTaskStatus(task.ID, "blocked", false)
		_ = rt.store.AppendRoomNotice(room.ID, task.AgentName, noticeType, noticeText)

		final := finish(finalStatus, humanizeFinalFailure(finalStatus, lastErr, task, settings, attemptsUsed), lastErr, result.Tokens, result.CostUSD)
		slog.Warn("run stopped", "room", room.ID[:8], "task", task.ID[:8], "status", final.Status, "error", lastErr.Error(), "tokens", final.Tokens, "cost", final.CostUSD)
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
	if task.Hidden && result.Status != "paused" && result.Status != "blocked" {
		_, _ = rt.store.UpdateTaskStatus(task.ID, "todo", false)
	}
	final := finish(result.Status, result.Message, nil, result.Tokens, result.CostUSD)
	slog.Info("run finished", "room", room.ID[:8], "task", task.ID[:8], "status", final.Status, "tokens", final.Tokens, "cost", final.CostUSD)
	if task.Hidden && final.Result != "" {
		_ = rt.store.AppendAgentReply(room.ID, task.AgentName, final.Result)
	} else if !task.Hidden {
		statusEmoji := "✅"
		if result.Status == "failed" || result.Status == "timed_out" || result.Status == "cancelled" {
			statusEmoji = "⚠️"
		}
		_ = rt.store.AppendRoomNotice(room.ID, task.AgentName, "run.finished",
			fmt.Sprintf("%s Task %s finished — status: %s", statusEmoji, task.Title, result.Status))
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

func buildRunPrompt(room Room, task Task, team *Team, skills string, allowedTools []string) string {
	tier := room.TriageTier
	if tier == "" {
		tier = "standard"
	}
	goal := room.Goal
	if goal == "" {
		goal = "Resolve requested issue"
	}
	sections := []string{
		fmt.Sprintf("Room: %s", room.Name),
		fmt.Sprintf("Room goal: %s", goal),
		fmt.Sprintf("Triage Tier: %s", strings.ToUpper(tier)),
		fmt.Sprintf("Task: %s", task.Title),
		fmt.Sprintf("Description: %s", task.Description),
	}
	if team != nil {
		activeAgent := task.AgentName
		if activeAgent == "" {
			activeAgent = team.MainAgent
		}
		sections = append(sections,
			fmt.Sprintf("You are currently acting as **%s**.", activeAgent),
			teamRoster(*team, activeAgent),
			team.Instructions,
			agentInstructions(*team, activeAgent),
		)
		agent := teamAgentByName(*team, activeAgent)
		switch resolveRoleType(*team, activeAgent, agent) {
		case "coordinator":
			sections = append(sections, `## Your role: Coordinator (lead agent)
- You hold delegate authority: create tasks and spawn specialists when that saves time, and decide scope.
- Prefer delegating substantial work to the right teammate (spawn_agent), but you may do a small task yourself when spinning up a teammate would cost more than it returns.
- Track the board and enforce gates (review before done). Keep the human informed; escalate decisions you cannot make (ask_human) instead of guessing.`)
		case "reviewer":
			sections = append(sections, `## Your role: Reviewer
- Review independently; do not self-approve your own work. Be specific and fair.
- If changes are needed: finish the review task with status "blocked" and a clear summary of what must change. The system will automatically reopen the implementer's task for rework (bounded), so do not edit the code yourself unless asked.
- If the work is sound: finish_task with status "done" and the evidence you checked.`)
		default:
			sections = append(sections, `## Your role: Specialist
- Work on your assigned task. Do NOT spawn other agents, do NOT change scope, do NOT reassign work — that is the coordinator's job.
- Finish with finish_task (status review/done + summary + changed files + evidence). If blocked or you need a decision, use ask_human or escalate to the coordinator.`)
		}
	}
	if skills != "" {
		sections = append(sections, "# Skills\n\n"+skills)
	}

	triageGuidance := `## Before starting (important):
- First inspect existing tasks/plan/backlog (list_tasks, existing plan) for this room.
- If unfinished work exists, ask the human whether to continue, skip, or replace it before proceeding.
- Do not assume previous work is irrelevant.

## Orchestration Protocol & Triage Tiers:
- Quick tier: 1 agent, minimal fix. Directly diagnose, inspect files, apply minimal bounded fix, verify, and call finish_task(status="review" or "done", summary="...", changed_files=[...], evidence="..."). Do not create large plans.
- Standard tier: Create a short checklist plan (create_plan), create tasks on Kanban (create_task), delegate to the right specialist with spawn_agent (it creates a real task for that teammate), execute, run verify steps you have permission for, and move the task to review.
- Epic tier: Create full plan with dependencies, spawn parallel specialist agents (spawn_agent), manage the task board (create_task/update_task/claim_task), enforce review gates, and summarize when all tasks complete.

## Delegation rules:
- You are the coordinator when teammates are present. Rely on the team: use spawn_agent with the exact member name from the Team Roster (e.g. debugger, reviewer, planner roles) instead of doing specialist work yourself.
- Delegation creates a real Kanban task; give the teammate a clear title and description including file paths and acceptance criteria.
- If a delegation tool errors, retry once with an unassigned create_task (omit agent_name) so the work is not lost, then note the failure in your summary.`

	sections = append(sections,
		triageGuidance,
		advertisedToolsSection(allowedTools),
		"Work only on this task. Keep changes within the active project workspace. Report what changed and what you verified.",
		`## Agent contract (summary; full reference in AGENT_CONTRACT.md)
- Language: reply in the same language as the user's latest message.
- Tasks: concise title (verb + object, ≤10 words); description includes context + acceptance criteria.
- Navigation: call repo_map once on unfamiliar codebases before grepping/reading many files.
- Loop Governor (enforced in code): hard limits (steps/tokens/cost/wall time); repeated tool+args or short cycles stop with a visible reason; no progress → stop and report; always finish with finish_task(result) including summary, changed files, evidence; if stuck, escalate or ask the human.
- Search: prefer repo_map first, then targeted grep/glob; grep uses RE2; results are capped with a truncated flag — refine the query rather than repeat the same call.
- Diagnostics: run the diagnose tool (vet/build/test/lint for the detected language) to find bugs and to gather evidence before finishing; it is read-only and needs project-command permission.
- Deliberation: discussions/reviews follow an evidence-gated protocol (critique = claim + evidence + severity + suggested fix), position changes require new evidence, disputes are settled by running tools/tests, and decisions are recorded with dissent. Debates are moderated and bounded (max 2 rounds); full debate is reserved for Epic-tier design decisions; Standard tier uses a single critic.
- Spawn: hierarchical — spawning goes through the lead (can_spawn = false | request | limited); each spawn carries task + scope paths + budget + reason; depth/concurrency/total agent caps apply; duplicate check against the board; child budget is carved from the parent's remaining budget. Workers may message peers, and may use request_spawn (approval) instead of spawning directly.`,
		"Need a decision from the human before continuing? Use AskHumanTool or end your reply with a fenced block:\n\n```question\n<the question>\n- option one\n- option two\n```\n\nThen stop and wait for the answer.",
	)
	return strings.Join(sections, "\n\n")
}

func teamRoster(team Team, activeAgent string) string {
	var b strings.Builder
	b.WriteString("## Team Roster\n")
	b.WriteString(fmt.Sprintf("Team: %s (Main agent: %s)\n", team.Name, team.MainAgent))
	if team.Purpose != "" {
		b.WriteString(fmt.Sprintf("Team purpose: %s\n", team.Purpose))
	}
	b.WriteString("\n### Members:\n")
	reviewer := ""
	for _, a := range team.Agents {
		marker := ""
		if a.Name == activeAgent {
			marker = " [CURRENT TURN - YOU]"
		} else if a.Name == team.MainAgent {
			marker = " [MAIN AGENT]"
		}
		b.WriteString(fmt.Sprintf("- **%s** (%s)%s: %s\n", a.Name, a.Role, marker, a.Description))
		if reviewer == "" && a.Name != activeAgent && strings.Contains(strings.ToLower(a.Role+" "+a.Description), "review") {
			reviewer = a.Name
		}
	}
	b.WriteString("\n### Multi-Agent Coordination & Delegation Rules:\n")
	b.WriteString("1. You are operating as part of this collaborative team in Foreman.\n")
	b.WriteString("2. If you are the Coordinator / Lead, break down goals into structured steps or tasks.\n")
	b.WriteString("3. If delegating to or calling on a specialist teammate, specify the delegation clearly using `@TeammateName <task>` in your response so the user and room can track handoffs.\n")
	b.WriteString("4. Always report actions taken, files inspected or modified, and verification evidence.\n")
	b.WriteString("\n### Who to contact for what:\n")
	b.WriteString("- Work feedback, quality issues, or change requests → route to the agent who owns that task/file (see Members above).\n")
	if reviewer != "" {
		b.WriteString(fmt.Sprintf("- Independent review of completed work → @%s (Reviewer).\n", reviewer))
	}
	b.WriteString(fmt.Sprintf("- Decisions about scope, new tasks, or delegation → @%s (Coordinator).\n", team.MainAgent))
	b.WriteString("- Blockers you cannot resolve → escalate to the Coordinator; if the Coordinator needs a human decision, they will use ask_human.\n")
	b.WriteString("- Direct question to a teammate → send_message to them (via @Name); keep teammates informed when your work affects theirs.\n")
	return b.String()
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

// teamAgentByName returns a pointer to the named team member, or nil.
func teamAgentByName(team Team, name string) *Agent {
	for i := range team.Agents {
		if team.Agents[i].Name == name {
			return &team.Agents[i]
		}
	}
	return nil
}

// resolveRoleType resolves an agent's role: an explicit RoleType wins; otherwise
// the team's main agent is a coordinator, a role/description mentioning
// "review" is a reviewer, and everyone else is a specialist.
func resolveRoleType(team Team, name string, agent *Agent) string {
	if agent != nil && agent.RoleType != "" {
		return agent.RoleType
	}
	if name == team.MainAgent {
		return "coordinator"
	}
	if agent != nil {
		hay := strings.ToLower(agent.Role + " " + agent.Description)
		if strings.Contains(hay, "review") {
			return "reviewer"
		}
	}
	return "specialist"
}

// activeTeamAgent resolves the agent that will act for this task, falling back
// to the team's main agent.
func activeTeamAgent(team *Team, task Task) *Agent {
	if team == nil {
		return nil
	}
	target := task.AgentName
	if target == "" {
		target = team.MainAgent
	}
	for i := range team.Agents {
		if team.Agents[i].Name == target {
			return &team.Agents[i]
		}
	}
	for i := range team.Agents {
		if team.Agents[i].Name == team.MainAgent {
			return &team.Agents[i]
		}
	}
	return nil
}

// effectiveBudgets resolves the per-run action-round and read-only budgets,
// letting an agent override the workspace defaults (0 = inherit).
func effectiveBudgets(agent *Agent, settings Settings) (maxIterations, readOnlyBudget int) {
	maxIterations = settings.MaxIterations
	readOnlyBudget = settings.ReadOnlyBudget
	if agent != nil {
		if agent.MaxIterations > 0 {
			maxIterations = agent.MaxIterations
		}
		if agent.ReadOnlyBudget > 0 {
			readOnlyBudget = agent.ReadOnlyBudget
		}
	}
	if maxIterations <= 0 {
		maxIterations = 30
	}
	if readOnlyBudget <= 0 {
		readOnlyBudget = 40
	}
	return maxIterations, readOnlyBudget
}

func resultOrError(message string, err error) string {
	if message != "" {
		return message
	}
	return err.Error()
}

// humanizeFinalFailure renders a failed/timeout/cancelled run as a humane,
// actionable message instead of a raw Go error string.
func humanizeFinalFailure(status string, err error, task Task, settings Settings, attempts int) string {
	switch status {
	case "timed_out":
		return fmt.Sprintf("Stopped: the execution time limit (%ds) was reached while working on %q. Partial work already saved (tasks, diffs, artifacts) stays visible. To continue, send a narrower prompt or raise Max Execution Seconds in Settings.", settings.MaxExecutionSeconds, task.Title)
	case "cancelled":
		return fmt.Sprintf("Run stopped by you while working on %q. Send a new prompt to continue.", task.Title)
	case "blocked":
		return fmt.Sprintf("Blocked after %d attempts: %v. You can retry, narrow the scope, or check the activity log.", attempts, err)
	default:
		return fmt.Sprintf("Failed: %v.", err)
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

func resolveModelChain(config ProviderConfig, team *Team, task Task) (string, []string) {
	var candidates []string
	seen := map[string]bool{}
	add := func(m string) {
		m = strings.TrimSpace(m)
		if m != "" && !strings.EqualFold(m, "auto") && !seen[m] {
			seen[m] = true
			candidates = append(candidates, m)
		}
	}

	agentModel := ""
	if team != nil {
		target := task.AgentName
		if target == "" {
			target = team.MainAgent
		}
		for _, agent := range team.Agents {
			if agent.Name == target {
				agentModel = agent.Model
				break
			}
		}
	}
	if agentModel != "" {
		for _, part := range strings.Split(agentModel, ",") {
			add(part)
		}
	}
	add(config.DefaultModel)
	for _, m := range config.Models {
		add(m)
	}

	if len(candidates) == 0 {
		return config.DefaultModel, nil
	}
	primary := candidates[0]
	var fallbacks []string
	if len(candidates) > 1 {
		fallbacks = candidates[1:]
	}
	return primary, fallbacks
}

// conversationID scopes a run's conversation to the room + agent rather than a
// single task, so repeated prompts in the same room keep the same thread.
func conversationID(roomID string, task Task) string {
	if task.AgentName == "" {
		return roomID
	}
	return roomID + ":" + task.AgentName
}

// backoffForAttempt returns how long to wait before retrying a failed run. It
// honors a "Retry-After" hint in the provider error when present, otherwise it
// backs off exponentially (2s, 4s, 8s, ...) capped at 30s.
func backoffForAttempt(err error, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if secs, ok := parseRetryAfter(err); ok {
		d := time.Duration(secs) * time.Second
		if d > 60*time.Second {
			d = 60 * time.Second
		}
		if d < time.Second {
			d = time.Second
		}
		return d
	}
	d := time.Duration(1<<attempt) * time.Second
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// parseRetryAfter extracts a seconds hint from an error string such as
// "retry-after: 20" or "retry after 20 seconds".
func parseRetryAfter(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	text := strings.ToLower(err.Error())
	idx := strings.Index(text, "retry-after")
	marker := len("retry-after")
	if idx < 0 {
		idx = strings.Index(text, "retry after")
		marker = len("retry after")
	}
	if idx < 0 {
		return 0, false
	}
	rest := text[idx+marker:]
	rest = strings.TrimLeft(rest, " :=\t")
	digits := ""
	for _, r := range rest {
		if r >= '0' && r <= '9' {
			digits += string(r)
			continue
		}
		break
	}
	if digits == "" {
		return 0, false
	}
	n, convErr := strconv.Atoi(digits)
	if convErr != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// autonomousToolDocs is the single source of truth for how Foreman tools are
// described to agents. advertisedToolsSection prints only the ones actually
// registered for this run, so the prompt never promises a tool the agent lacks.
var autonomousToolDocs = []struct{ name, doc string }{
	{"create_task", "create a task card on Kanban (assigns to you if agent_name omitted)."},
	{"update_task", "move a task card (todo/in_progress/review/blocked/done/cancelled) or edit its title/description/priority/effort."},
	{"claim_task", "claim an open task card atomically."},
	{"list_tasks", "list current tasks on the board."},
	{"delete_task", "delete a task card."},
	{"finish_task", "MUST call this when finishing a task to record results, changed files, and evidence! Never just say done in chat without calling finish_task."},
	{"spawn_agent", "delegate work to a teammate agent (creates a Kanban task for them)."},
	{"request_spawn", "ask the lead/supervisor to spawn a teammate (workers only)."},
	{"create_plan", "save plan checklist and diagram nodes/edges to the Plan tab."},
	{"delete_plan", "remove a saved plan."},
	{"send_message", "message teammates in mailbox."},
	{"ask_human", "if you encounter ambiguity or require confirmation, do NOT make assumptions—call ask_human and wait. Set severity (info|warning|blocker) so it is triaged correctly."},
	{"escalate", "report blockers to supervisor. Set severity (info|warning|blocker); blocker halts, warning needs a decision soon, info is FYI."},
	{"run_checks", "run test/lint commands (only available when project commands are allowed)."},
	{"diagnose", "run read-only vet/build/test/lint for the detected language."},
	{"git_ops", "inspect git diff/status, or commit changes (commit requires human confirmation)."},
	{"memory", "read/write shared cross-agent memory."},
	{"repo_map", "get a compact repository topology before exploring unfamiliar code."},
	{"search_index", "full-text search across indexed source files."},
	{"register_panel", "register custom UI panels (table, markdown, chart, kanban, diagram)."},
}

// advertisedToolsSection renders the "Available Autonomous Tools" list from the
// tools that are actually registered for this run (see allowedADKToolNames).
func advertisedToolsSection(allowedTools []string) string {
	registered := allowedADKToolNames(allowedTools)
	var b strings.Builder
	b.WriteString("## Available Autonomous Tools (exact names, only the ones listed here are wired up):\n")
	any := false
	for _, d := range autonomousToolDocs {
		if !registered[d.name] {
			continue
		}
		any = true
		fmt.Fprintf(&b, "- %s: %s\n", d.name, d.doc)
	}
	if !any {
		b.WriteString("- (none beyond the core set)\n")
	}
	b.WriteString("\nDo not attempt to call a tool that is not in this list; if a capability is missing, work with the tools you have or escalate.")
	return b.String()
}
