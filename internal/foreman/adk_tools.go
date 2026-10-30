package foreman

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

type adkToolContext struct {
	Store     *Store
	RoomID    string
	TaskID    string
	Workspace string
	AgentName string
	AllowCmds bool
	Emit      func(EngineEvent)
	// diff remembers the pre-run content of each file changed in this run so
	// emitted diffs stay cumulative (one diff per file, latest state).
	diff *diffTracker
}

// diffTracker keeps the pre-run ("origin") content of every file an agent has
// edited during one run, guarded for concurrent tool calls.
type diffTracker struct {
	mu      sync.Mutex
	origins map[string]string
}

func (d *diffTracker) originOf(rel, fallback string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.origins == nil {
		d.origins = map[string]string{}
	}
	if prev, ok := d.origins[rel]; ok {
		return prev
	}
	d.origins[rel] = fallback
	return fallback
}

type createTaskArgs struct {
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	AgentName    string   `json:"agent_name"`
	TeamID       string   `json:"team_id"`
	Priority     string   `json:"priority"`
	Severity     string   `json:"severity"`
	Effort       string   `json:"effort"`
	Dependencies []string `json:"dependencies"`
}

// Optional-by-default argument tags are not cosmetic: the generated JSON
// schema treats untagged fields as REQUIRED, so a model calling finish_task
// with only task_id+status was rejected by schema validation before the tool
// ever ran — one of the reasons cards never left their stage.
type updateTaskArgs struct {
	TaskID      string `json:"task_id,omitempty"`
	Status      string `json:"status,omitempty"`
	Severity    string `json:"severity,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Priority    string `json:"priority,omitempty"`
	Effort      string `json:"effort,omitempty"`
	Approved    bool   `json:"approved,omitempty"`
}

type claimTaskArgs struct {
	TaskID    string `json:"task_id,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
}

type finishTaskArgs struct {
	TaskID       string   `json:"task_id,omitempty"`
	Summary      string   `json:"summary,omitempty"`
	ChangedFiles []string `json:"changed_files,omitempty"`
	Evidence     string   `json:"evidence,omitempty"`
	Status       string   `json:"status,omitempty"`
}

type listTasksArgs struct {
	RoomID string `json:"room_id,omitempty"`
}

type createPlanArgs struct {
	RoomID    string `json:"room_id"`
	Title     string `json:"title"`
	Tier      string `json:"tier"`
	Markdown  string `json:"markdown"`
	NodesJSON string `json:"nodes_json"`
	EdgesJSON string `json:"edges_json"`
}

type runChecksArgs struct {
	Command string `json:"command"`
}

type diagnoseArgs struct {
	// Scope optionally narrows the run: "build", "vet", "test", or "" for all.
	Scope string `json:"scope"`
}

type gitOpsArgs struct {
	Operation string `json:"operation"`
	Message   string `json:"message"`
}

type memoryArgs struct {
	Action string `json:"action"`
	Key    string `json:"key"`
	Value  string `json:"value"`
}

type askHumanArgs struct {
	Question string `json:"question"`
	Severity string `json:"severity,omitempty"` // info (default), warning, blocker
}

type requestSpawnArgs struct {
	AgentName string `json:"agent_name"`
	TaskTitle string `json:"task_title"`
	TaskDesc  string `json:"task_desc"`
	Reason    string `json:"reason"`
}

type spawnAgentArgs struct {
	AgentName string `json:"agent_name"`
	TaskTitle string `json:"task_title"`
	TaskDesc  string `json:"task_desc"`
}

// maxSpawnDepth caps hierarchical spawn chain depth (lead → worker → helper).
const maxSpawnDepth = 2

type registerPanelArgs struct {
	RoomID    string `json:"room_id"`
	PanelType string `json:"panel_type"`
	Title     string `json:"title"`
	DataJSON  string `json:"data_json"`
}

type sendMessageArgs struct {
	RoomID  string `json:"room_id,omitempty"`
	Content string `json:"content"`
}

type escalateArgs struct {
	Message  string `json:"message"`
	Title    string `json:"title,omitempty"`
	Severity string `json:"severity,omitempty"` // info, warning (default), blocker
}

type grepFilesArgs struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path"`
	Before          int    `json:"before,omitempty"`
	After           int    `json:"after,omitempty"`
	MaxMatches      int    `json:"max_matches,omitempty"`
	CaseInsensitive bool   `json:"case_insensitive,omitempty"`
}

type globFilesArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

type repoMapArgs struct {
	MaxChars int    `json:"max_chars,omitempty"`
	Path     string `json:"path,omitempty"`
}

type searchIndexArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type deleteTaskArgs struct {
	TaskID string `json:"task_id,omitempty"`
}

type deletePlanArgs struct {
	RoomID string `json:"room_id"`
}

type deleteArtifactArgs struct {
	ArtifactID string `json:"artifact_id"`
}

func makeADKTools(ctx adkToolContext) []tool.Tool {
	var tools []tool.Tool
	add := func(t tool.Tool, err error) {
		if err == nil && t != nil {
			tools = append(tools, t)
		}
	}

	// create_task
	add(functiontool.New[createTaskArgs, map[string]any](functiontool.Config{
		Name:        "create_task",
		Description: "Create a task card on the Kanban board.",
	}, func(ac agent.Context, args createTaskArgs) (map[string]any, error) {
		if ctx.Store == nil {
			return nil, fmt.Errorf("store unavailable")
		}
		roomID := ctx.RoomID
		if args.AgentName == "" {
			args.AgentName = ctx.AgentName
		}
		// Resolve the team for the assigned agent so assignment succeeds.
		teamID := args.TeamID
		if teamID == "" {
			teamID = ctx.Store.TeamIDForRoomAgent(roomID, args.AgentName)
		}
		if teamID == "" {
			return nil, fmt.Errorf("cannot assign task to %q: not a member of a team invited to this room (available teammates: %s). Omit agent_name to assign the task to yourself", args.AgentName, strings.Join(ctx.Store.RoomAgentNames(roomID), ", "))
		}
		priority := args.Priority
		if priority == "" {
			priority = "normal"
		}
		effort := args.Effort
		if effort == "" {
			effort = "medium"
		}
		deps := args.Dependencies
		if deps == nil {
			deps = []string{}
		}
		// Stop duplicate cards: the same agent + title in this room that is not
		// yet finished means the work is already tracked.
		if existing, dup := ctx.Store.FindOpenTaskByTitle(roomID, args.AgentName, args.Title); dup {
			return map[string]any{"task_id": existing.ID, "status": "exists", "note": "an open task with this title already exists for this agent; not creating a duplicate"}, nil
		}
		task := Task{
			ID:           uuid.NewString(),
			RoomID:       roomID,
			AgentName:    args.AgentName,
			TeamID:       teamID,
			Title:        args.Title,
			Description:  args.Description,
			Status:       "todo",
			Priority:     priority,
			Severity:     normalizeSeverity(args.Severity),
			Effort:       effort,
			Dependencies: deps,
		}
		created, err := ctx.Store.CreateTask(task)
		if err != nil {
			return nil, err
		}
		_ = ctx.Store.RecordActivity(roomID, ctx.AgentName, "task.created", fmt.Sprintf("Created task: %s", args.Title), nil)
		mention := ""
		if args.AgentName != "" && args.AgentName != ctx.AgentName {
			mention = fmt.Sprintf(" for @%s", args.AgentName)
		}
		_ = ctx.Store.AppendRoomNotice(roomID, ctx.AgentName, "task.created",
			fmt.Sprintf("📋 @%s created task %s%s", ctx.AgentName, args.Title, mention))
		return map[string]any{"task_id": created.ID, "status": "created"}, nil
	}))

	// update_task
	add(functiontool.New[updateTaskArgs, map[string]any](functiontool.Config{
		Name:        "update_task",
		Description: "Move a task card to a new status and/or edit its title, description, priority (P0-P3) or effort.",
	}, func(ac agent.Context, args updateTaskArgs) (map[string]any, error) {
		if ctx.Store == nil {
			return nil, fmt.Errorf("store unavailable")
		}
		taskID := args.TaskID
		if taskID == "" {
			taskID = ctx.TaskID
		}
		// Accept id or exact title, same as finish_task.
		resolved, err := ctx.Store.ResolveTaskRef(ctx.RoomID, taskID)
		if err != nil {
			return nil, err
		}
		taskID = resolved.ID
		// Detail edits (title/description/priority/effort) may happen without a
		// status change.
		edited := false
		if args.Title != "" || args.Description != "" || args.Priority != "" || args.Effort != "" {
			patch := TaskDetailsPatch{}
			if args.Title != "" {
				v := args.Title
				patch.Title = &v
			}
			if args.Description != "" {
				v := args.Description
				patch.Description = &v
			}
			if args.Priority != "" {
				v := args.Priority
				patch.Priority = &v
			}
			if args.Effort != "" {
				v := args.Effort
				patch.Effort = &v
			}
			if _, err := ctx.Store.UpdateTaskDetails(taskID, patch); err != nil {
				return nil, err
			}
			edited = true
		}
		if strings.TrimSpace(args.Severity) != "" {
			if _, err := ctx.Store.SetTaskSeverity(taskID, args.Severity); err != nil {
				return nil, err
			}
			edited = true
		}
		if strings.TrimSpace(args.Status) != "" {
			updated, err := ctx.Store.UpdateTaskStatus(taskID, args.Status, args.Approved)
			if err != nil {
				return nil, err
			}
			_ = ctx.Store.RecordActivity(ctx.RoomID, ctx.AgentName, "task.moved", fmt.Sprintf("Task %s moved to %s", taskID, args.Status), nil)
			_ = ctx.Store.AppendRoomNotice(ctx.RoomID, ctx.AgentName, "task.moved",
				fmt.Sprintf("📌 @%s moved a task to %s", ctx.AgentName, args.Status))
			return map[string]any{"task_id": updated.ID, "status": updated.Status}, nil
		}
		if !edited {
			return nil, fmt.Errorf("provide status, severity, or fields to edit")
		}
		if edited {
			_ = ctx.Store.AppendRoomNotice(ctx.RoomID, ctx.AgentName, "task.edited",
				fmt.Sprintf("✏️ @%s edited a task card", ctx.AgentName))
		}
		for _, t := range ctx.Store.Snapshot().Tasks {
			if t.ID == taskID {
				return map[string]any{"task_id": t.ID, "status": t.Status, "priority": t.Priority, "severity": t.Severity}, nil
			}
		}
		return map[string]any{"task_id": taskID, "status": "updated"}, nil
	}))

	// claim_task
	add(functiontool.New[claimTaskArgs, map[string]any](functiontool.Config{
		Name:        "claim_task",
		Description: "Claim a task card atomically (todo/backlog → in_progress). task_id accepts the card id OR its exact title.",
	}, func(ac agent.Context, args claimTaskArgs) (map[string]any, error) {
		if ctx.Store == nil {
			return nil, fmt.Errorf("store unavailable")
		}
		agentName := args.AgentName
		if agentName == "" {
			agentName = ctx.AgentName
		}
		task, err := ctx.Store.ResolveTaskRef(ctx.RoomID, args.TaskID)
		if err != nil {
			return nil, err
		}
		if task.Status != "todo" && task.Status != "backlog" {
			// Not an error: someone else (or an earlier run) already owns it.
			return map[string]any{"claimed": false, "status": task.Status,
				"reason": fmt.Sprintf("task is %s, only todo/backlog cards can be claimed", task.Status)}, nil
		}
		// The old path wrote to the SQLite tasks table, which the app never
		// populates — every claim failed with "no such row".
		if _, err := ctx.Store.ClaimTask(task.ID, agentName); err != nil {
			return nil, err
		}
		_ = ctx.Store.AppendRoomNotice(ctx.RoomID, agentName, "task.claimed",
			fmt.Sprintf("🖐️ @%s claimed %q", agentName, task.Title))
		return map[string]any{"claimed": true, "status": "in_progress", "task_id": task.ID}, nil
	}))

	// finish_task
	add(functiontool.New[finishTaskArgs, map[string]any](functiontool.Config{
		Name:        "finish_task",
		Description: "Finish a task and record results, changed files, and evidence. task_id accepts the card id OR the exact card title (as printed by list_tasks). status: done (approved/complete), review (needs a reviewer), blocked (request rework on the implementation).",
	}, func(ac agent.Context, args finishTaskArgs) (map[string]any, error) {
		if ctx.Store == nil {
			return nil, fmt.Errorf("store unavailable")
		}
		if args.TaskID == "" {
			args.TaskID = ctx.TaskID
		}
		// Accept id, exact title, or slug: models pass the title list_tasks
		// printed, and rejecting it here used to leave the card stuck.
		task, err := ctx.Store.ResolveTaskRef(ctx.RoomID, args.TaskID)
		if err != nil {
			return nil, err
		}
		args.TaskID = task.ID
		status := args.Status
		if status == "" {
			status = "done"
		}
		_, err = ctx.Store.UpdateTaskStatus(args.TaskID, status, true)
		if err != nil {
			return nil, err
		}
		taskTitle := task.Title
		parentID := task.ParentID

		// A reviewer approving their "Review: …" card closes the implementation
		// card it was created for. Without this, approved work sat in the Review
		// column forever: the only parent handling that existed fired on
		// "blocked" (rework), so a clean approval never propagated.
		if status == "done" && parentID != "" && strings.HasPrefix(taskTitle, "Review: ") {
			if parent, perr := ctx.Store.ResolveTaskRef(ctx.RoomID, parentID); perr == nil && parent.Status == "review" {
				if _, uerr := ctx.Store.UpdateTaskStatus(parent.ID, "done", true); uerr == nil {
					_ = ctx.Store.RecordActivity(ctx.RoomID, ctx.AgentName, "review.approved",
						fmt.Sprintf("Review approved; task %s marked done", parent.ID), nil)
					_ = ctx.Store.AppendRoomNotice(ctx.RoomID, ctx.AgentName, "review.approved",
						fmt.Sprintf("✅ Review approved — task %q marked done by @%s", parent.Title, ctx.AgentName))
				}
			}
		}
		var filesList string
		if len(args.ChangedFiles) > 0 {
			var files []string
			for _, f := range args.ChangedFiles {
				files = append(files, "- `"+f+"`")
			}
			filesList = "\n\n**Changed files:**\n" + strings.Join(files, "\n")
		}
		artifact := ArtifactRecord{
			ID:        uuid.NewString(),
			RoomID:    ctx.RoomID,
			Type:      "report",
			Title:     "Task result: " + taskTitle,
			Summary:   args.Summary,
			Content:   args.Evidence + filesList,
			CreatedAt: time.Now().UTC(),
		}
		_ = ctx.Store.SaveArtifact(artifact)
		_ = ctx.Store.RecordActivity(ctx.RoomID, ctx.AgentName, "task.finished", fmt.Sprintf("Finished task %s", args.TaskID), nil)
		_ = ctx.Store.AppendRoomNotice(ctx.RoomID, ctx.AgentName, "task.finished",
			fmt.Sprintf("✅ Task %s marked %s by @%s — report saved in the Artifacts tab", taskTitle, status, ctx.AgentName))

		// Reviewer requested changes (review task finished as "blocked"):
		// reopen the original implementer task for rework, bounded by retries.
		if status == "blocked" && parentID != "" {
			orig, reopened, rerr := ctx.Store.ReopenForRework(parentID)
			if rerr != nil {
				return nil, rerr
			}
			if reopened {
				ctx.Store.EnqueueAutoStart(orig.ID)
				_ = ctx.Store.AppendRoomNotice(ctx.RoomID, orig.AgentName, "review.rework",
					fmt.Sprintf("🔄 Review requested changes (cycle %d). Task reopened for @%s: %q", orig.RetryCount, orig.AgentName, orig.Title))
			} else {
				_, _ = ctx.Store.CreateApproval(ApprovalRecord{
					RoomID:      ctx.RoomID,
					AgentName:   ctx.AgentName,
					Type:        "review",
					Description: fmt.Sprintf("Rework limit reached for %q after %d cycles. Review the discussion and decide next steps.", orig.Title, orig.RetryCount),
				})
				_ = ctx.Store.AppendRoomNotice(ctx.RoomID, ctx.AgentName, "review.escalated",
					fmt.Sprintf("🚩 Rework limit reached for %q — escalated to the human for a decision.", orig.Title))
			}
		}

		// A "review" status hands the work to a reviewer teammate: create their
		// review task automatically and wake them up, so review actually happens.
		if status == "review" {
			reviewer := ctx.Store.ReviewerForRoom(ctx.RoomID, ctx.AgentName)
			if reviewer != "" {
				teamID := ctx.Store.TeamIDForRoomAgent(ctx.RoomID, reviewer)
				if teamID != "" {
					reviewTask := Task{
						ID:           uuid.NewString(),
						RoomID:       ctx.RoomID,
						AgentName:    reviewer,
						TeamID:       teamID,
						Title:        "Review: " + taskTitle,
						Description:  fmt.Sprintf("Review the fix for task %q. Verify against acceptance criteria, check the diff and tests, then move the task to done or request changes (status blocked).", taskTitle),
						Status:       "todo",
						Priority:     "normal",
						Effort:       "medium",
						Dependencies: []string{},
						ParentID:     args.TaskID,
					}
					if _, err := ctx.Store.CreateTask(reviewTask); err == nil {
						ctx.Store.EnqueueAutoStart(reviewTask.ID)
						_ = ctx.Store.AppendRoomNotice(ctx.RoomID, reviewer, "review.assigned",
							fmt.Sprintf("🔎 Review task created for @%s: %q", reviewer, reviewTask.Title))
					}
				}
			}
		}

		return map[string]any{"task_id": args.TaskID, "status": status}, nil
	}))

	// list_tasks
	add(functiontool.New[listTasksArgs, map[string]any](functiontool.Config{
		Name:        "list_tasks",
		Description: "List current tasks on the board. room_id accepts a room id or the room's display name (defaults to this room).",
	}, func(ac agent.Context, args listTasksArgs) (map[string]any, error) {
		if ctx.Store == nil {
			return nil, fmt.Errorf("store unavailable")
		}
		roomID := args.RoomID
		if roomID == "" {
			roomID = ctx.RoomID
		} else if resolved := ctx.Store.ResolveRoomID(roomID); resolved != "" {
			// Agents pass the room's display name; matching it against room ids
			// returned {"tasks": null} and read to the agent as an empty board.
			roomID = resolved
		}
		out := []map[string]any{}
		for _, t := range ctx.Store.Snapshot().Tasks {
			if t.RoomID == roomID {
				out = append(out, map[string]any{
					"id": t.ID, "title": t.Title, "status": t.Status, "agent": t.AgentName,
				})
			}
		}
		return map[string]any{"tasks": out, "room_id": roomID}, nil
	}))

	// create_plan
	add(functiontool.New[createPlanArgs, map[string]any](functiontool.Config{
		Name:        "create_plan",
		Description: "Save a plan checklist and diagram nodes/edges to the Plan tab.",
	}, func(ac agent.Context, args createPlanArgs) (map[string]any, error) {
		if ctx.Store == nil {
			return nil, fmt.Errorf("store unavailable")
		}
		roomID := args.RoomID
		if roomID == "" {
			roomID = ctx.RoomID
		}
		tier := args.Tier
		if tier == "" {
			tier = "standard"
		}
		plan := PlanRecord{
			RoomID:    roomID,
			Title:     args.Title,
			Tier:      tier,
			Markdown:  args.Markdown,
			NodesJSON: args.NodesJSON,
			EdgesJSON: args.EdgesJSON,
		}
		if err := ctx.Store.SavePlan(plan); err != nil {
			return nil, err
		}
		return map[string]any{"status": "saved"}, nil
	}))

	// diagnose (bug/ops): run language-appropriate static checks and return a
	// structured, read-only summary. Gated on project command execution.
	if ctx.AllowCmds {
		add(functiontool.New[diagnoseArgs, map[string]any](functiontool.Config{
			Name:        "diagnose",
			Description: "Run static checks (vet/build/test/lint) for the workspace and return a structured summary of failures. Read-only; use it to find bugs before finishing a task.",
		}, func(ac agent.Context, args diagnoseArgs) (map[string]any, error) {
			checks := diagnoseChecks(ctx.Workspace, args.Scope)
			results := make([]map[string]any, 0, len(checks))
			failed := 0
			for _, c := range checks {
				cmd := exec.Command("sh", "-c", c.command)
				cmd.Dir = ctx.Workspace
				out, err := cmd.CombinedOutput()
				status := "passed"
				if err != nil {
					status = "failed"
					failed++
				}
				text := string(out)
				if len(text) > 4000 {
					text = text[:4000] + "\n…(truncated)"
				}
				results = append(results, map[string]any{
					"check":    c.name,
					"command":  c.command,
					"status":   status,
					"output":   text,
					"language": c.language,
				})
			}
			return map[string]any{
				"checks":  results,
				"failed":  failed,
				"summary": fmt.Sprintf("%d/%d checks failed", failed, len(results)),
			}, nil
		}))
	}

	// run_checks (only when project command execution is enabled — otherwise
	// granting it just wastes an agent turn on "project commands disabled")
	if ctx.AllowCmds {
		add(functiontool.New[runChecksArgs, map[string]any](functiontool.Config{
			Name:        "run_checks",
			Description: "Run a test/lint command in the workspace.",
		}, func(ac agent.Context, args runChecksArgs) (map[string]any, error) {
			if !ctx.AllowCmds {
				return nil, fmt.Errorf("project commands disabled")
			}
			cmd := exec.Command("sh", "-c", args.Command)
			cmd.Dir = ctx.Workspace
			out, err := cmd.CombinedOutput()
			status := "passed"
			if err != nil {
				status = "failed"
			}
			return map[string]any{"status": status, "output": string(out)}, nil
		}))
	}

	// git_ops
	add(functiontool.New[gitOpsArgs, map[string]any](functiontool.Config{
		Name:        "git_ops",
		Description: "Inspect git diff/status or propose a commit. A commit is a request: it is recorded for human approval and executes only after a human approves it in the Approvals Inbox.",
	}, func(ac agent.Context, args gitOpsArgs) (map[string]any, error) {
		op := args.Operation
		if op == "commit" {
			return requestCommitApproval(ctx, args)
		}
		var cmd *exec.Cmd
		switch op {
		case "status":
			cmd = exec.Command("git", "status", "--short")
		case "diff":
			cmd = exec.Command("git", "diff")
		default:
			cmd = exec.Command("git", "status")
		}
		cmd.Dir = ctx.Workspace
		out, err := cmd.CombinedOutput()
		if err != nil {
			return map[string]any{"status": "error", "output": string(out), "error": err.Error()}, nil
		}
		// Surface diffs in the room's Changes tab.
		if op == "diff" {
			ctx.emitDiff("", string(out))
		}
		return map[string]any{"status": "ok", "output": string(out)}, nil
	}))

	// memory
	add(functiontool.New[memoryArgs, map[string]any](functiontool.Config{
		Name:        "memory",
		Description: "Read or write shared cross-agent memory.",
	}, func(ac agent.Context, args memoryArgs) (map[string]any, error) {
		if ctx.Store == nil {
			return nil, fmt.Errorf("store unavailable")
		}
		db := ctx.Store.DB()
		switch args.Action {
		case "set":
			err := db.SetMemory(ctx.RoomID, args.Key, args.Value, ctx.AgentName)
			if err != nil {
				return nil, err
			}
			return map[string]any{"status": "saved"}, nil
		default:
			val, err := db.GetMemory(ctx.RoomID, args.Key)
			if err != nil {
				return nil, err
			}
			return map[string]any{"value": val}, nil
		}
	}))

	// ask_human
	add(functiontool.New[askHumanArgs, map[string]any](functiontool.Config{
		Name:        "ask_human",
		Description: "Ask the human a question. It lands in the Approvals Inbox and as a chat notice; the human's decision is delivered to you as a follow-up message in this room, so do not block — summarize what you know and finish or continue with the safest assumption.",
	}, func(ac agent.Context, args askHumanArgs) (map[string]any, error) {
		if ctx.Store == nil {
			return nil, fmt.Errorf("store unavailable")
		}
		id, err := ctx.Store.CreateApproval(ApprovalRecord{
			RoomID:      ctx.RoomID,
			AgentName:   ctx.AgentName,
			Type:        "question",
			Description: args.Question,
			Severity:    normalizeSeverity(args.Severity),
		})
		if err != nil {
			return nil, err
		}
		// A question nobody can see is a question nobody answers: post a chip in
		// the room as well, so the ask does not depend on the human happening to
		// open the Approvals Inbox.
		_ = ctx.Store.AppendRoomNotice(ctx.RoomID, ctx.AgentName, "approval.requested",
			fmt.Sprintf("❓ @%s asks the human: %s", ctx.AgentName, strings.TrimSpace(args.Question)))
		return map[string]any{"approval_id": id, "status": "pending"}, nil
	}))

	// request_spawn
	add(functiontool.New[requestSpawnArgs, map[string]any](functiontool.Config{
		Name:        "request_spawn",
		Description: "Request the lead agent to spawn a teammate. Creates an approval for the human/lead; the task is created only after approval.",
	}, func(ac agent.Context, args requestSpawnArgs) (map[string]any, error) {
		if ctx.Store == nil {
			return nil, fmt.Errorf("store unavailable")
		}
		if args.AgentName == "" {
			return nil, fmt.Errorf("agent_name is required")
		}
		names := ctx.Store.RoomAgentNames(ctx.RoomID)
		found := false
		for _, n := range names {
			if n == args.AgentName {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("cannot request spawn: %q is not a member of a team invited to this room", args.AgentName)
		}
		title := args.TaskTitle
		if title == "" {
			title = "Delegated task for " + args.AgentName
		}
		desc := args.TaskDesc
		if desc == "" {
			desc = title
		}
		payload, _ := json.Marshal(map[string]string{
			"action": "request_spawn", "agent_name": args.AgentName,
			"title": title, "desc": desc,
		})
		id, err := ctx.Store.CreateApproval(ApprovalRecord{
			RoomID: ctx.RoomID, AgentName: ctx.AgentName, Type: "request_spawn",
			Description: fmt.Sprintf("🆘 @%s requests spawning @%s: %s — reason: %s", ctx.AgentName, args.AgentName, title, args.Reason),
			PayloadJSON: string(payload),
		})
		if err != nil {
			return nil, err
		}
		_ = ctx.Store.AppendRoomNotice(ctx.RoomID, ctx.AgentName, "spawn.requested",
			fmt.Sprintf("🆘 @%s requests spawning @%s — awaiting lead/human approval.", ctx.AgentName, args.AgentName))
		return map[string]any{"status": "pending", "approval_id": id}, nil
	}))

	// spawn_agent
	add(functiontool.New[spawnAgentArgs, map[string]any](functiontool.Config{
		Name:        "spawn_agent",
		Description: "Delegate work to a teammate agent (by exact team roster name). Creates a Kanban task assigned to that agent, which starts their run when capacity allows. Only the room's lead agent may spawn.",
	}, func(ac agent.Context, args spawnAgentArgs) (map[string]any, error) {
		if ctx.Store == nil {
			return nil, fmt.Errorf("store unavailable")
		}
		// Spawn model: hierarchical — only the lead agent may spawn peers by
		// default. An agent explicitly granted "limited" authority in its
		// profile may spawn directly too; "request" (or the default) must go
		// through request_spawn for lead/human approval.
		if supervisor := ctx.Store.RoomMainAgentName(ctx.RoomID); supervisor != "" && ctx.AgentName != supervisor {
			if ctx.Store.RoomAgentCanSpawn(ctx.RoomID, ctx.AgentName) != "limited" {
				return nil, fmt.Errorf("only the lead agent (%s) may spawn teammates; use request_spawn through the lead", supervisor)
			}
		}
		// Depth cap: walk the parent task chain (default max 2).
		depthCap := ctx.Store.Snapshot().Settings.MaxDelegationDepth
		if depthCap <= 0 {
			depthCap = maxSpawnDepth
		}
		depth := 0
		cur := ctx.TaskID
		for cur != "" {
			depth++
			if depth >= depthCap {
				return nil, fmt.Errorf("spawn depth limit (%d) reached", depthCap)
			}
			next := ""
			for _, t := range ctx.Store.Snapshot().Tasks {
				if t.ID == cur {
					next = t.ParentID
					break
				}
			}
			cur = next
		}
		title := args.TaskTitle
		if title == "" {
			title = "Delegated task for " + args.AgentName
		}
		if args.AgentName == "" {
			return nil, fmt.Errorf("agent_name is required (available teammates: %d agents)", len(ctx.Store.RoomAgentNames(ctx.RoomID)))
		}
		teamID := ctx.Store.TeamIDForRoomAgent(ctx.RoomID, args.AgentName)
		if teamID == "" {
			return nil, fmt.Errorf("cannot spawn %q: not a member of a team invited to this room (available teammates: %s)", args.AgentName, strings.Join(ctx.Store.RoomAgentNames(ctx.RoomID), ", "))
		}
		task := Task{
			ID:           uuid.NewString(),
			RoomID:       ctx.RoomID,
			AgentName:    args.AgentName,
			TeamID:       teamID,
			Title:        title,
			Description:  args.TaskDesc,
			Status:       "todo",
			Priority:     "normal",
			Effort:       "medium",
			Dependencies: []string{},
			ParentID:     ctx.TaskID,
		}
		// Duplicate guard: don't hand the same agent a second identical card.
		if existing, dup := ctx.Store.FindOpenTaskByTitle(ctx.RoomID, args.AgentName, title); dup {
			return map[string]any{"status": "exists", "agent": args.AgentName, "task_id": existing.ID, "note": "that agent already has an open task with this title"}, nil
		}
		created, err := ctx.Store.CreateTask(task)
		if err != nil {
			return nil, err
		}
		_ = ctx.Store.RecordActivity(ctx.RoomID, ctx.AgentName, "agent.spawned", fmt.Sprintf("Spawning agent @%s (task %s)", args.AgentName, created.ID), nil)
		_ = ctx.Store.AppendRoomNotice(ctx.RoomID, ctx.AgentName, "agent.spawned",
			fmt.Sprintf("🤝 @%s delegated to @%s: %s — task created on the Kanban board", ctx.AgentName, args.AgentName, title))
		// Wake the teammate up: their run auto-starts as soon as capacity frees.
		ctx.Store.EnqueueAutoStart(created.ID)
		return map[string]any{"status": "spawned", "agent": args.AgentName, "task_id": created.ID}, nil
	}))

	// register_panel
	add(functiontool.New[registerPanelArgs, map[string]any](functiontool.Config{
		Name:        "register_panel",
		Description: "Register a custom UI panel.",
	}, func(ac agent.Context, args registerPanelArgs) (map[string]any, error) {
		if ctx.Store == nil {
			return nil, fmt.Errorf("store unavailable")
		}
		roomID := args.RoomID
		if roomID == "" {
			roomID = ctx.RoomID
		}
		id, err := ctx.Store.RegisterPanel(PanelRecord{
			RoomID:       roomID,
			Title:        args.Title,
			Renderer:     args.PanelType,
			ManifestJSON: args.DataJSON,
			Status:       "pending",
			CreatedAt:    time.Now().UTC(),
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"panel_id": id, "status": "pending"}, nil
	}))

	// send_message
	add(functiontool.New[sendMessageArgs, map[string]any](functiontool.Config{
		Name:        "send_message",
		Description: "Post a message to the room chat as yourself. Address a teammate with @Name so they pick it up as context.",
	}, func(ac agent.Context, args sendMessageArgs) (map[string]any, error) {
		if ctx.Store == nil {
			return nil, fmt.Errorf("store unavailable")
		}
		roomID := args.RoomID
		if roomID == "" {
			roomID = ctx.RoomID
		} else if resolved := ctx.Store.ResolveRoomID(roomID); resolved != "" {
			roomID = resolved
		}
		// AppendRoomMessage only accepts role "user" (it is the human's input
		// path), so posting as "assistant" made this tool fail every single
		// time — teammates never saw the message. Agent speech goes through
		// AppendAgentReply.
		if err := ctx.Store.AppendAgentReply(roomID, ctx.AgentName, args.Content); err != nil {
			return nil, err
		}
		return map[string]any{"status": "sent"}, nil
	}))

	// escalate
	add(functiontool.New[escalateArgs, map[string]any](functiontool.Config{
		Name:        "escalate",
		Description: "Report a blocker or issue to the human/lead. Creates a review item with a severity (info|warning|blocker) so it is visible in the Approvals Inbox with the right colour.",
	}, func(ac agent.Context, args escalateArgs) (map[string]any, error) {
		if ctx.Store == nil {
			return nil, fmt.Errorf("store unavailable")
		}
		raw := args.Severity
		if raw == "" {
			raw = "warning"
		}
		sev := normalizeSeverity(raw)
		title := strings.TrimSpace(args.Title)
		if title == "" {
			title = "Escalation"
		}
		id, err := ctx.Store.CreateApproval(ApprovalRecord{
			RoomID:      ctx.RoomID,
			AgentName:   ctx.AgentName,
			Type:        "escalation",
			Description: args.Message,
			Severity:    sev,
		})
		if err != nil {
			return nil, err
		}
		_ = ctx.Store.RecordActivity(ctx.RoomID, ctx.AgentName, "task.blocked", args.Message, nil)
		icon := map[string]string{"blocker": "🛑", "warning": "⚠️", "info": "ℹ️"}[sev]
		_ = ctx.Store.AppendRoomNotice(ctx.RoomID, ctx.AgentName, "task.escalated",
			fmt.Sprintf("%s @%s raised a %s issue: %s", icon, ctx.AgentName, sev, args.Message))
		return map[string]any{"status": "escalated", "approval_id": id, "severity": sev}, nil
	}))

	// grep_files
	add(functiontool.New[grepFilesArgs, map[string]any](functiontool.Config{
		Name:        "grep_files",
		Description: "Search files for a regex pattern. Respects ignore rules, skips hidden/binary/large files, returns path:line:column:text (up to 100 matches).",
	}, func(ac agent.Context, args grepFilesArgs) (map[string]any, error) {
		root := ctx.Workspace
		if args.Path != "" {
			root = args.Path
		}
		re := safeRegexp(args.Pattern, args.CaseInsensitive)
		before := args.Before
		if before < 0 {
			before = 0
		}
		after := args.After
		if after < 0 {
			after = 0
		}
		maxMatches := args.MaxMatches
		if maxMatches <= 0 {
			maxMatches = 100
		}
		if maxMatches > 500 {
			maxMatches = 500
		}
		// Prefer ripgrep when available (fast, respects .gitignore), except
		// when context lines are requested (Go fallback handles those).
		if rgAvailable() && before == 0 && after == 0 {
			return rgGrep(root, args)
		}
		var matches []map[string]any
		var scanned int
		_ = walkProject(root, func(path, rel string, info os.FileInfo) error {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			if bytes.IndexByte(data, 0) >= 0 {
				return nil // binary
			}
			scanned++
			lines := strings.Split(string(data), "\n")
			for i, line := range lines {
				if !re.MatchString(line) {
					continue
				}
				ctxLines := []map[string]any{{"text": line}}
				for k := 1; k <= before && i-k >= 0; k++ {
					ctxLines = append([]map[string]any{{"text": lines[i-k], "context": true}}, ctxLines...)
				}
				for k := 1; k <= after && i+k < len(lines); k++ {
					ctxLines = append(ctxLines, map[string]any{"text": lines[i+k], "context": true})
				}
				matches = append(matches, map[string]any{
					"file": rel, "line": i + 1, "column": colOf(re, line), "lines": ctxLines,
				})
				if len(matches) >= maxMatches {
					return errStopWalk
				}
			}
			return nil
		})
		return map[string]any{"matches": matches, "count": len(matches), "files_scanned": scanned}, nil
	}))

	// glob_files
	add(functiontool.New[globFilesArgs, map[string]any](functiontool.Config{
		Name:        "glob_files",
		Description: "List workspace files matching a glob pattern (supports **). Respects ignore rules and skips hidden/build dirs.",
	}, func(ac agent.Context, args globFilesArgs) (map[string]any, error) {
		root := ctx.Workspace
		if args.Path != "" {
			root = args.Path
		}
		pattern := args.Pattern
		if pattern == "" {
			pattern = "**/*"
		}
		if rgAvailable() {
			return rgGlob(root, pattern)
		}
		re := globRegex(pattern)
		var out []string
		maxFiles := 500
		_ = walkProject(root, func(path, rel string, info os.FileInfo) error {
			if re.MatchString(rel) {
				out = append(out, rel)
				if len(out) >= maxFiles {
					return errStopWalk
				}
			}
			return nil
		})
		return map[string]any{"files": out, "count": len(out)}, nil
	}))

	// repo_map
	add(functiontool.New[repoMapArgs, map[string]any](functiontool.Config{
		Name:        "repo_map",
		Description: "Build a concise map of source files and their key symbols (Aider-style). Use once at the start to orient in an unfamiliar codebase before grepping/reading.",
	}, func(ac agent.Context, args repoMapArgs) (map[string]any, error) {
		root := ctx.Workspace
		if args.Path != "" {
			root = args.Path
		}
		return buildRepoMap(root, args.MaxChars), nil
	}))

	// search_index: FTS5 full-text search over the workspace with incremental
	// indexing. Faster than grep for locating a symbol across many files; falls
	// back to a plain scan when FTS5 is unavailable.
	if ctx.Store != nil {
		add(functiontool.New[searchIndexArgs, map[string]any](functiontool.Config{
			Name:        "search_index",
			Description: "Full-text search the workspace via an incremental index; returns file:line matches. Prefer over grep for locating symbols across many files; use grep for exact regex.",
		}, func(ac agent.Context, args searchIndexArgs) (map[string]any, error) {
			root := ctx.Workspace
			if root == "" {
				return nil, fmt.Errorf("no workspace")
			}
			indexed, removed, _ := ctx.Store.IndexWorkspace(root)
			hits, usedIndex, err := ctx.Store.SearchWorkspace(root, args.Query, args.Limit)
			if err != nil {
				return nil, err
			}
			out := make([]map[string]any, 0, len(hits))
			for _, h := range hits {
				out = append(out, map[string]any{"file": h.Path, "line": h.Line, "text": h.Text})
			}
			return map[string]any{
				"matches":    out,
				"count":      len(out),
				"used_index": usedIndex,
				"indexed":    indexed,
				"removed":    removed,
			}, nil
		}))
	}

	// delete_task
	add(functiontool.New[deleteTaskArgs, map[string]any](functiontool.Config{
		Name:        "delete_task",
		Description: "Delete a task card from the Kanban board.",
	}, func(ac agent.Context, args deleteTaskArgs) (map[string]any, error) {
		if ctx.Store == nil {
			return nil, fmt.Errorf("store unavailable")
		}
		if args.TaskID == "" {
			args.TaskID = ctx.TaskID
		}
		resolved, err := ctx.Store.ResolveTaskRef(ctx.RoomID, args.TaskID)
		if err != nil {
			return nil, err
		}
		args.TaskID = resolved.ID
		if err := ctx.Store.DeleteTask(args.TaskID); err != nil {
			return nil, err
		}
		_ = ctx.Store.RecordActivity(ctx.RoomID, ctx.AgentName, "task.deleted", fmt.Sprintf("Deleted task %s", args.TaskID), nil)
		_ = ctx.Store.AppendRoomNotice(ctx.RoomID, ctx.AgentName, "task.deleted",
			fmt.Sprintf("🗑️ @%s deleted a task card", ctx.AgentName))
		return map[string]any{"status": "deleted", "task_id": args.TaskID}, nil
	}))

	// delete_plan
	add(functiontool.New[deletePlanArgs, map[string]any](functiontool.Config{
		Name:        "delete_plan",
		Description: "Delete a room's plan.",
	}, func(ac agent.Context, args deletePlanArgs) (map[string]any, error) {
		if ctx.Store == nil {
			return nil, fmt.Errorf("store unavailable")
		}
		roomID := args.RoomID
		if roomID == "" {
			roomID = ctx.RoomID
		}
		if err := ctx.Store.DeletePlan(roomID); err != nil {
			return nil, err
		}
		return map[string]any{"status": "deleted"}, nil
	}))

	// delete_artifact
	add(functiontool.New[deleteArtifactArgs, map[string]any](functiontool.Config{
		Name:        "delete_artifact",
		Description: "Delete an artifact.",
	}, func(ac agent.Context, args deleteArtifactArgs) (map[string]any, error) {
		if ctx.Store == nil {
			return nil, fmt.Errorf("store unavailable")
		}
		if err := ctx.Store.DeleteArtifact(args.ArtifactID); err != nil {
			return nil, err
		}
		return map[string]any{"status": "deleted"}, nil
	}))

	// file editor tools
	feTools := []tool.Tool{}
	if t, err := makeViewFileTool(ctx); err == nil {
		if tt, ok := t.(tool.Tool); ok {
			feTools = append(feTools, tt)
		}
	}
	if t, err := makeCreateFileTool(ctx); err == nil {
		if tt, ok := t.(tool.Tool); ok {
			feTools = append(feTools, tt)
		}
	}
	if t, err := makeReplaceStringTool(ctx); err == nil {
		if tt, ok := t.(tool.Tool); ok {
			feTools = append(feTools, tt)
		}
	}
	if t, err := makeInsertLinesTool(ctx); err == nil {
		if tt, ok := t.(tool.Tool); ok {
			feTools = append(feTools, tt)
		}
	}
	tools = append(tools, feTools...)

	return tools
}

func mustMarshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// requestCommitApproval puts a git commit behind human sign-off.
//
// The previous implementation used ADK's RequireConfirmationProvider, which
// emits an `adk_request_confirmation` event and ends the invocation. Nothing in
// Foreman ever answered that event: the commit never ran, nobody was asked, and
// the run still reported "completed" — a silent violation of the "commits are
// human-approved" contract. Commits now go through Foreman's own approval,
// which the dashboard can see, approve and reject.
func requestCommitApproval(ctx adkToolContext, args gitOpsArgs) (map[string]any, error) {
	if ctx.Store == nil {
		return nil, fmt.Errorf("store unavailable")
	}
	message := strings.TrimSpace(args.Message)
	if message == "" {
		message = "foreman: update"
	}
	id, err := ctx.Store.CreateApproval(ApprovalRecord{
		RoomID:      ctx.RoomID,
		AgentName:   ctx.AgentName,
		Type:        "command",
		Description: "🔐 Commit requested: " + message,
		PayloadJSON: mustMarshal(map[string]any{"action": "git_commit", "message": message, "workspace": ctx.Workspace}),
	})
	if err != nil {
		return nil, err
	}
	_ = ctx.Store.AppendRoomNotice(ctx.RoomID, ctx.AgentName, "approval.requested",
		fmt.Sprintf("🔐 @%s requested a git commit (human approval required): %q", ctx.AgentName, message))
	return map[string]any{
		"status":      "awaiting_human_approval",
		"approval_id": id,
		"committed":   false,
		"output":      "Nothing was committed yet. A human must approve this in the Approvals Inbox; the commit runs there and its result is posted to the room.",
	}, nil
}

// runGitCommit stages the workspace and commits. It is shared by the tool's
// approval path so the commit a human approved is exactly the commit that runs.
func runGitCommit(workspace, message string) (string, error) {
	if strings.TrimSpace(message) == "" {
		message = "foreman: update"
	}
	staged := exec.Command("git", "add", "-A")
	staged.Dir = workspace
	_ = staged.Run()
	cmd := exec.Command("git", "commit", "-m", message)
	cmd.Dir = workspace
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
