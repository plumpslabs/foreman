package foreman

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
)

// toolDetail unwraps a tool's return value: depending on the ADK version the
// handler's own map is returned flat or nested under "result".
func toolDetail(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	if nested, ok := result["result"].(map[string]any); ok {
		return nested
	}
	return result
}

// findTool returns a callable ADK tool by name from the Foreman toolset.
func findTool(t *testing.T, st *Store, roomID, workspace, agentName, name string) toolRunner {
	t.Helper()
	for _, tl := range makeADKTools(adkToolContext{Store: st, RoomID: roomID, Workspace: workspace, AgentName: agentName}) {
		if tl.Name() == name {
			if runner, ok := tl.(toolRunner); ok {
				return runner
			}
		}
	}
	t.Fatalf("tool %q is not registered", name)
	return nil
}

func newReviewRoom(t *testing.T, st *Store, name string) (Room, Team) {
	t.Helper()
	team, err := st.ImportTeam("quick-fix", "Quick Fix")
	if err != nil {
		t.Fatal(err)
	}
	room, err := st.CreateRoom(name, "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	return room, team
}

func finishRunWith(t *testing.T, st *Store, taskID, roomID, runStatus string) {
	t.Helper()
	run := AgentRun{
		ID: "run-" + taskID + "-" + runStatus, TaskID: taskID, RoomID: roomID,
		Status: "running", StartedAt: time.Now().UTC().Add(-time.Minute),
	}
	if err := st.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	run.Status = runStatus
	run.EndedAt = time.Now().UTC()
	if err := st.FinishRun(run); err != nil {
		t.Fatal(err)
	}
}

func taskStatus(st *Store, id string) string {
	for _, task := range st.Snapshot().Tasks {
		if task.ID == id {
			return task.Status
		}
	}
	return "<missing>"
}

// FinishRun used to force every visible task to "review" when a run completed,
// overwriting a status finish_task had already set. Cards the agent finished as
// done (and review cards a reviewer had just approved) silently reopened as
// review the moment the run reported completed.
func TestFinishRunCompletedKeepsClosedStatus(t *testing.T) {
	server, _ := newApprovalTestServer(t)
	st := server.store
	room, team := newReviewRoom(t, st, "FinishRun Room")

	makeTask := func(title, status string, hidden bool) Task {
		t.Helper()
		task, err := st.CreateTask(Task{
			RoomID: room.ID, AgentName: team.MainAgent, TeamID: team.ID,
			Title: title, Status: "todo", Priority: "normal", Effort: "medium",
			Hidden: hidden,
		})
		if err != nil {
			t.Fatal(err)
		}
		if status != "todo" {
			if _, err := st.UpdateTaskStatus(task.ID, status, true); err != nil {
				t.Fatal(err)
			}
		}
		return task
	}

	closed := makeTask("closed card", "done", false)
	awaiting := makeTask("awaiting review", "blocked", false)
	fresh := makeTask("untouched card", "todo", false)
	chat := makeTask("chat thread", "todo", true)

	finishRunWith(t, st, closed.ID, room.ID, "completed")
	finishRunWith(t, st, awaiting.ID, room.ID, "completed")
	finishRunWith(t, st, fresh.ID, room.ID, "completed")
	finishRunWith(t, st, chat.ID, room.ID, "completed")

	if got := taskStatus(st, closed.ID); got != "done" {
		t.Errorf("completed run overwrote a finished card: status = %q, want done", got)
	}
	if got := taskStatus(st, awaiting.ID); got != "review" {
		t.Errorf("unfinished card should enter the review gate: status = %q, want review", got)
	}
	if got := taskStatus(st, fresh.ID); got != "review" {
		t.Errorf("completed run should move visible work to review: status = %q, want review", got)
	}
	if got := taskStatus(st, chat.ID); got != "todo" {
		t.Errorf("hidden chat thread must return to todo: status = %q, want todo", got)
	}
}

// A reviewer approving their "Review: …" card has to close the implementation
// card underneath it. Previously the only parent handling fired on "blocked",
// so approved work sat in Review forever — the exact state the room history
// showed (task stuck in review long after every run finished).
func TestReviewerApprovalClosesImplementationTask(t *testing.T) {
	server, _ := newApprovalTestServer(t)
	st := server.store
	room, team := newReviewRoom(t, st, "Review Room")

	impl, err := st.CreateTask(Task{
		RoomID: room.ID, AgentName: team.MainAgent, TeamID: team.ID,
		Title: "Backend: add date filter", Status: "todo", Priority: "normal", Effort: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateTaskStatus(impl.ID, "review", true); err != nil {
		t.Fatal(err)
	}
	review, err := st.CreateTask(Task{
		RoomID: room.ID, AgentName: team.MainAgent, TeamID: team.ID,
		Title: "Review: Backend: add date filter", Status: "todo", Priority: "normal",
		Effort: "medium", ParentID: impl.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The reference is passed the way models actually pass it: the title.
	finish := findTool(t, st, room.ID, t.TempDir(), team.MainAgent, "finish_task")
	result, err := finish.Run(&agent.ContextMock{}, map[string]any{
		"task_id": "Review: Backend: add date filter",
		"status":  "done",
		"summary": "Verified the diff and tests; approving.",
	})
	if err != nil {
		t.Fatalf("finish_task by title: %v", err)
	}
	if detail := toolDetail(t, result); detail["status"] != "done" {
		t.Fatalf("finish_task result = %#v", detail)
	}

	if got := taskStatus(st, impl.ID); got != "done" {
		t.Errorf("approved implementation card must be closed: status = %q, want done", got)
	}
	if got := taskStatus(st, review.ID); got != "done" {
		t.Errorf("review card status = %q, want done", got)
	}
	var notice bool
	for _, m := range st.Snapshot().Rooms[0].Messages {
		if m.ActivityType == "review.approved" && strings.Contains(m.Content, "Backend: add date filter") {
			notice = true
		}
	}
	if !notice {
		t.Error("no chat notice reports the approved review")
	}

	// The run finishing afterwards must not reopen the approved card
	// (regression pair with TestFinishRunCompletedKeepsClosedStatus).
	finishRunWith(t, st, review.ID, room.ID, "completed")
	if got := taskStatus(st, review.ID); got != "done" {
		t.Errorf("completed review run reopened the approved card: status = %q, want done", got)
	}
}

// A reviewer requesting changes (blocked) must still reopen the implementation
// card for rework — the promotion fix must not swallow that path.
func TestRejectedReviewReopensImplementationTask(t *testing.T) {
	server, _ := newApprovalTestServer(t)
	st := server.store
	room, team := newReviewRoom(t, st, "Rework Room")

	impl, err := st.CreateTask(Task{
		RoomID: room.ID, AgentName: team.MainAgent, TeamID: team.ID,
		Title: "Frontend: fix colors", Status: "todo", Priority: "normal", Effort: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateTaskStatus(impl.ID, "review", true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTask(Task{
		RoomID: room.ID, AgentName: team.MainAgent, TeamID: team.ID,
		Title: "Review: Frontend: fix colors", Status: "todo", Priority: "normal",
		Effort: "medium", ParentID: impl.ID,
	}); err != nil {
		t.Fatal(err)
	}

	finish := findTool(t, st, room.ID, t.TempDir(), team.MainAgent, "finish_task")
	if _, err := finish.Run(&agent.ContextMock{}, map[string]any{
		"task_id": "Review: Frontend: fix colors",
		"status":  "blocked",
		"summary": "Colors still stale in one branch.",
	}); err != nil {
		t.Fatalf("finish_task blocked: %v", err)
	}
	if got := taskStatus(st, impl.ID); got != "in_progress" {
		t.Errorf("rejected review must reopen the card for rework: status = %q, want in_progress", got)
	}
}

// finish_task used to reject anything that was not an exact id ("unknown task
// id"), which is how real runs left cards stuck: the model passed the title
// list_tasks printed. Unknown references must fail with a list of what exists.
func TestFinishTaskReferenceResolution(t *testing.T) {
	server, _ := newApprovalTestServer(t)
	st := server.store
	room, team := newReviewRoom(t, st, "Resolve Room")
	task, err := st.CreateTask(Task{
		RoomID: room.ID, AgentName: team.MainAgent, TeamID: team.ID,
		Title: "Fix getWaitingSide logic", Status: "todo", Priority: "normal", Effort: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	finish := findTool(t, st, room.ID, t.TempDir(), team.MainAgent, "finish_task")

	// Exact title.
	if _, err := finish.Run(&agent.ContextMock{}, map[string]any{
		"task_id": "Fix getWaitingSide logic", "status": "done",
	}); err != nil {
		t.Fatalf("title reference rejected: %v", err)
	}
	if got := taskStatus(st, task.ID); got != "done" {
		t.Fatalf("title reference did not move the card: status = %q", got)
	}

	// Slug: reset, then finish via slug.
	if _, err := st.UpdateTaskStatus(task.ID, "todo", true); err != nil {
		t.Fatal(err)
	}
	if _, err := finish.Run(&agent.ContextMock{}, map[string]any{
		"task_id": "fix-getwaitingside-logic", "status": "done",
	}); err != nil {
		t.Fatalf("slug reference rejected: %v", err)
	}
	if got := taskStatus(st, task.ID); got != "done" {
		t.Fatalf("slug reference did not move the card: status = %q", got)
	}

	// Unknown reference: error must name a real card so the model can retry.
	if _, err := finish.Run(&agent.ContextMock{}, map[string]any{
		"task_id": "no such card", "status": "done",
	}); err == nil {
		t.Fatal("unknown reference must fail")
	} else if !strings.Contains(err.Error(), "Fix getWaitingSide logic") {
		t.Fatalf("error does not list available cards: %v", err)
	}
}

// list_tasks matched room ids only; models pass the room's display name and
// got {"tasks": null}, which they read as an empty board and stopped acting.
func TestListTasksResolvesRoomName(t *testing.T) {
	server, _ := newApprovalTestServer(t)
	st := server.store
	room, team := newReviewRoom(t, st, "forman tets 1")
	if _, err := st.CreateTask(Task{
		RoomID: room.ID, AgentName: team.MainAgent, TeamID: team.ID,
		Title: "Visible card", Status: "todo", Priority: "normal", Effort: "medium",
	}); err != nil {
		t.Fatal(err)
	}
	list := findTool(t, st, room.ID, t.TempDir(), team.MainAgent, "list_tasks")

	result, err := list.Run(&agent.ContextMock{}, map[string]any{"room_id": "forman tets 1"})
	if err != nil {
		t.Fatal(err)
	}
	detail := toolDetail(t, result)
	if detail["room_id"] != room.ID {
		t.Fatalf("room name was not resolved: room_id = %#v, want %q", detail["room_id"], room.ID)
	}
	tasks, ok := detail["tasks"].([]any)
	if !ok || len(tasks) != 1 {
		t.Fatalf("tasks = %#v, want one card", detail["tasks"])
	}

	// An empty board must serialize as an empty list, never null.
	other, err := st.CreateRoom("Empty Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	empty := findTool(t, st, other.ID, t.TempDir(), team.MainAgent, "list_tasks")
	result, err = empty.Run(&agent.ContextMock{}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if got := toolDetail(t, result)["tasks"]; got == nil {
		t.Fatal(`empty board serialized as null — agents read that as "no board exists"`)
	}
}

// claim_task wrote to the SQLite tasks table, which the app never populates
// (cards live in the workspace JSON), so every claim failed.
func TestClaimTaskClaimsJSONCard(t *testing.T) {
	server, _ := newApprovalTestServer(t)
	st := server.store
	room, team := newReviewRoom(t, st, "Claim Room")
	task, err := st.CreateTask(Task{
		RoomID: room.ID, AgentName: team.MainAgent, TeamID: team.ID,
		Title: "Claimable card", Status: "todo", Priority: "normal", Effort: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	claim := findTool(t, st, room.ID, t.TempDir(), "Ferro", "claim_task")

	result, err := claim.Run(&agent.ContextMock{}, map[string]any{"task_id": task.ID})
	if err != nil {
		t.Fatalf("claim failed: %v", err)
	}
	if detail := toolDetail(t, result); detail["claimed"] != true {
		t.Fatalf("claim result = %#v, want claimed=true", detail)
	}
	if got := taskStatus(st, task.ID); got != "in_progress" {
		t.Fatalf("claimed card status = %q, want in_progress", got)
	}

	// A second claim must not steal the card, and must not error out either.
	result, err = claim.Run(&agent.ContextMock{}, map[string]any{"task_id": task.ID})
	if err != nil {
		t.Fatalf("re-claim errored instead of reporting the owner: %v", err)
	}
	if detail := toolDetail(t, result); detail["claimed"] != false {
		t.Fatalf("re-claim result = %#v, want claimed=false", detail)
	}
}

// The HTTP status surface (board column changes) must accept the same
// lifecycle the tools now produce, i.e. moving a card out of review.
func TestUpdateTaskStatusPromotesOutOfReviewOverHTTP(t *testing.T) {
	server, project := newApprovalTestServer(t)
	st := server.store
	room, team := newReviewRoom(t, st, "HTTP Room")
	task, err := st.CreateTask(Task{
		RoomID: room.ID, AgentName: team.MainAgent, TeamID: team.ID,
		Title: "Stuck card", Status: "todo", Priority: "normal", Effort: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateTaskStatus(task.ID, "review", true); err != nil {
		t.Fatal(err)
	}
	path := "/api/tasks/" + task.ID + "/status?project=" + url.QueryEscape(project)
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"status":"done","approved":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:1234"
	req.Host = "127.0.0.1:8080"
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", res.Code, res.Body.String())
	}
	if got := taskStatus(st, task.ID); got != "done" {
		t.Fatalf("card status = %q, want done", got)
	}
}
