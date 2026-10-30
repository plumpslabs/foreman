package foreman

import (
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
)

// libraryAgent returns the library profile with the given name.
func libraryAgent(t *testing.T, st *Store, name string) Agent {
	t.Helper()
	for _, a := range st.Snapshot().Agents {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("library profile %q not found", name)
	return Agent{}
}

// rosterAgent returns a team roster entry with the given name.
func rosterAgent(t *testing.T, team Team, name string) Agent {
	t.Helper()
	for _, a := range team.Agents {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("team %q has no roster agent %q", team.Name, name)
	return Agent{}
}

func teamByID(t *testing.T, st *Store, id string) Team {
	t.Helper()
	for _, tm := range st.Snapshot().Teams {
		if tm.ID == id {
			return tm
		}
	}
	t.Fatalf("team %q not found", id)
	return Team{}
}

// Saving a profile must update every team roster copy: runs read the roster,
// so an edit that only lands in the library never reaches the model. This was
// the root cause of "model selection in the profile is ignored".
func TestSaveAgentPropagatesProfileToTeamRoster(t *testing.T) {
	server, _ := newApprovalTestServer(t)
	st := server.store
	team, err := st.ImportTeam("quick-fix", "Quick Fix")
	if err != nil {
		t.Fatal(err)
	}

	lib := libraryAgent(t, st, "Implementer")
	lib.Model = "codebuddy/hy3"
	lib.Instructions = "profile instructions win"
	lib.Tools = []string{"terminal"}
	lib.Skills = nil
	lib.RequireReview = true
	lib.Effort = "high"
	lib.MaxIterations = 42
	lib.CanSpawn = "request"
	if _, err := st.SaveAgent(lib); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	roster := rosterAgent(t, teamByID(t, st, team.ID), "Implementer")
	if roster.Model != "codebuddy/hy3" {
		t.Errorf("roster model = %q, want codebuddy/hy3", roster.Model)
	}
	if roster.Instructions != "profile instructions win" {
		t.Errorf("roster instructions = %q, want the profile's text", roster.Instructions)
	}
	if len(roster.Tools) != 1 || roster.Tools[0] != "terminal" {
		t.Errorf("roster tools = %v, want [terminal]", roster.Tools)
	}
	if !roster.RequireReview {
		t.Error("roster requireReview did not follow the profile")
	}
	if roster.Effort != "high" || roster.MaxIterations != 42 || roster.CanSpawn != "request" {
		t.Errorf("roster effort/maxIterations/canSpawn = %q/%d/%q, want high/42/request",
			roster.Effort, roster.MaxIterations, roster.CanSpawn)
	}
}

// Diverged on-disk data (roster copies stashed before profiles were edited)
// must repair itself at load time via applyProfile — without the user
// re-saving every agent.
func TestApplyProfileReconcilesStaleRoster(t *testing.T) {
	server, _ := newApprovalTestServer(t)
	st := server.store
	team, err := st.ImportTeam("quick-fix", "Quick Fix")
	if err != nil {
		t.Fatal(err)
	}
	wantInstructions := libraryAgent(t, st, "Quick Coordinator").Instructions
	if wantInstructions == "" {
		t.Fatal("seeded profile should have instructions")
	}

	// Simulate the pre-fix state: roster copy diverged from the library.
	st.state.Teams[0].Agents[0].Instructions = "stale copy"
	st.state.Teams[0].Agents[0].Model = "wrong/model"
	st.state.Teams[0].Agents[0].RequireReview = true

	st.applyProfile(st.profile())

	roster := rosterAgent(t, teamByID(t, st, team.ID), "Quick Coordinator")
	if roster.Instructions != wantInstructions {
		t.Errorf("roster instructions = %q, want the library text restored", roster.Instructions)
	}
	if roster.Model != "" {
		t.Errorf("roster model = %q, want empty (library has none)", roster.Model)
	}
	if roster.RequireReview {
		t.Error("stale requireReview was not cleared by the library profile")
	}
}

// An explicit model chain on the profile is authoritative: exactly those
// models, in order. Provider-wide rotation must not drown a deliberate pick.
func TestResolveModelChainHonorsExplicitAgentChain(t *testing.T) {
	cfg := ProviderConfig{
		ID: "p", DefaultModel: "codebuddy/glm-5.2",
		Models: []string{"codebuddy/glm-5.2", "codebuddy/glm-5.3", "other/model"},
	}
	team := &Team{
		MainAgent: "Ferro",
		Agents:    []Agent{{Name: "Ferro", Model: "opencode/beam-free, codebuddy/hy3"}},
	}

	primary, fallbacks := resolveModelChain(cfg, team, Task{AgentName: "Ferro"})
	if primary != "opencode/beam-free" {
		t.Errorf("primary = %q, want opencode/beam-free", primary)
	}
	if len(fallbacks) != 1 || fallbacks[0] != "codebuddy/hy3" {
		t.Errorf("fallbacks = %v, want exactly [codebuddy/hy3] (no provider flood)", fallbacks)
	}

	// A legacy comma-joined DefaultModel must never surface as one model name.
	cfg.DefaultModel = "opencode/beam-free, codebuddy/hy3"
	primary, fallbacks = resolveModelChain(cfg, team, Task{AgentName: "Ferro"})
	for _, m := range append([]string{primary}, fallbacks...) {
		if strings.Contains(m, ",") {
			t.Fatalf("chain contains a comma-joined model name: %q", m)
		}
	}

	// Without an explicit pick the provider rotation still applies: default
	// first, then the provider's model list (deduplicated).
	auto := ProviderConfig{
		ID: "p", DefaultModel: "codebuddy/glm-5.2",
		Models: []string{"codebuddy/glm-5.2", "codebuddy/glm-5.3", "other/model"},
	}
	team.Agents[0].Model = ""
	primary, fallbacks = resolveModelChain(auto, team, Task{AgentName: "Ferro"})
	if primary != "codebuddy/glm-5.2" {
		t.Errorf("auto primary = %q, want the provider default", primary)
	}
	if len(fallbacks) != 2 {
		t.Errorf("auto fallbacks = %v, want the provider model list after the default", fallbacks)
	}
}

// finish_task by a profile flagged "Require review" must not close its own
// card: the work enters the review gate, which hands a Review card to a
// reviewer.
func TestFinishTaskRequireReviewRoutesToReview(t *testing.T) {
	server, _ := newApprovalTestServer(t)
	st := server.store
	room, team := newReviewRoom(t, st, "Require Review Room")

	lib := libraryAgent(t, st, "Implementer")
	lib.RequireReview = true
	if _, err := st.SaveAgent(lib); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	if !st.AgentRequiresReview(room.ID, "Implementer") {
		t.Fatal("AgentRequiresReview = false after enabling the profile flag")
	}

	task, err := st.CreateTask(Task{
		RoomID: room.ID, AgentName: "Implementer", TeamID: team.ID,
		Title: "Ship the date filter", Status: "todo", Priority: "normal",
	})
	if err != nil {
		t.Fatal(err)
	}

	finish := findTool(t, st, room.ID, t.TempDir(), "Implementer", "finish_task")
	result, err := finish.Run(&agent.ContextMock{}, map[string]any{
		"task_id": task.ID, "status": "done", "summary": "Implemented and verified.",
	})
	if err != nil {
		t.Fatalf("finish_task: %v", err)
	}
	if detail := toolDetail(t, result); detail["status"] != "review" {
		t.Fatalf("finish_task result status = %v, want review", detail["status"])
	}
	if got := taskStatus(st, task.ID); got != "review" {
		t.Errorf("task status = %q, want review", got)
	}

	// The review gate hands the work to a reviewer teammate.
	reviewCard := false
	for _, tk := range st.Snapshot().Tasks {
		if tk.RoomID == room.ID && tk.Title == "Review: Ship the date filter" {
			reviewCard = true
		}
	}
	if !reviewCard {
		t.Error("no reviewer card was created for the required review")
	}

	// A reviewer's own "Review: ..." card still closes normally: approvals
	// must never loop through the require-review gate.
	reviewTask, err := st.CreateTask(Task{
		RoomID: room.ID, AgentName: team.MainAgent, TeamID: team.ID,
		Title: "Review: Ship the date filter", Status: "todo", Priority: "normal",
	})
	if err != nil {
		t.Fatal(err)
	}
	finishReviewer := findTool(t, st, room.ID, t.TempDir(), team.MainAgent, "finish_task")
	if _, err := finishReviewer.Run(&agent.ContextMock{}, map[string]any{
		"task_id": reviewTask.ID, "status": "done", "summary": "Approved.",
	}); err != nil {
		t.Fatalf("reviewer finish_task: %v", err)
	}
	if got := taskStatus(st, reviewTask.ID); got != "done" {
		t.Errorf("review card status = %q, want done (reviews must not loop)", got)
	}
}

// The profile's effort flows into task creation (falling back to the
// workspace default) and is rejected when it is not on the effort scale.
func TestCreateTaskInheritsAgentEffort(t *testing.T) {
	server, _ := newApprovalTestServer(t)
	st := server.store
	room, team := newReviewRoom(t, st, "Effort Room")

	lib := libraryAgent(t, st, "Implementer")
	lib.Effort = "high"
	if _, err := st.SaveAgent(lib); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	lib.Effort = "ultra"
	if _, err := st.SaveAgent(lib); err == nil {
		t.Error("SaveAgent accepted an effort outside low/medium/high")
	}

	task, err := st.CreateTask(Task{
		RoomID: room.ID, AgentName: "Implementer", TeamID: team.ID,
		Title: "Thorough task", Status: "todo", Priority: "normal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.Effort != "high" {
		t.Errorf("task effort = %q, want the profile's high", task.Effort)
	}

	fallback, err := st.CreateTask(Task{
		RoomID: room.ID, AgentName: team.MainAgent, TeamID: team.ID,
		Title: "Default effort task", Status: "todo", Priority: "normal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Effort != st.Snapshot().Settings.DefaultEffort {
		t.Errorf("task effort = %q, want workspace default %q", fallback.Effort, st.Snapshot().Settings.DefaultEffort)
	}
}

// The run prompt must carry the effort directive so the model can act on it.
func TestBuildRunPromptCarriesEffort(t *testing.T) {
	room := Room{Name: "Room", Goal: "goal"}
	task := Task{Title: "T", Description: "D", Effort: "high"}
	prompt := buildRunPrompt(room, task, nil, "", nil)
	if !strings.Contains(prompt, "Effort: high") {
		t.Errorf("prompt missing the high-effort directive:\n%s", prompt)
	}
	if strings.Contains(prompt, "Effort: low") {
		t.Error("prompt carries a directive for a different effort")
	}
}
