package foreman

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// postRoomMessage sends one chat message through the real HTTP handler and
// returns the decoded response plus the resulting room state.
func postRoomMessage(t *testing.T, server *Server, project, roomID, content string) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]string{"role": "user", "content": content})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/rooms/" + url.PathEscape(roomID) + "/messages?project=" + url.QueryEscape(project)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Host = "127.0.0.1:8080"
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("post message status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rec.Body.String())
	}
	return payload
}

// waitForPrompt polls the fake engine until at least one prompt was recorded.
func waitForPrompt(t *testing.T, engine *fakeEngine) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if prompts := engine.promptCalls(); len(prompts) > 0 {
			return prompts[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the agent run never received a prompt")
	return ""
}

// waitForRuns lets background runs finish before the test's TempDir is removed.
// A run still writing to .foreman when cleanup starts makes RemoveAll fail with
// "directory not empty" — a test-only artifact, but a flaky one.
func waitForRuns(t *testing.T, server *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		server.runtime.mu.Lock()
		active := server.runtime.activeCount
		server.runtime.mu.Unlock()
		if active == 0 {
			// Give the final store write a moment to land.
			time.Sleep(20 * time.Millisecond)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// newChatTestServer builds a server with a fake provider, the Quick Fix team,
// and one room — the exact shape of the room that misread /journal.
func newChatTestServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	isolateGlobal(t)
	server, project := newTestServer(t)
	st := server.store
	ensureFakeProvider(t, st)
	team, err := st.ImportTeam("quick-fix", "Quick Fix")
	if err != nil {
		t.Fatal(err)
	}
	room, err := st.CreateRoom("fix", "perbaiki bug status tiket", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	return server, project, room.ID
}

// TestSlashJournalReachesAgentAsInstruction is the regression guard for the
// reported bug: the agent replied "The user sent /journal. I have already
// applied both patches…" because it received the literal token. The command must
// arrive as a concrete instruction naming the journal mechanism.
func TestSlashJournalReachesAgentAsInstruction(t *testing.T) {
	server, project, roomID := newChatTestServer(t)
	t.Cleanup(func() { waitForRuns(t, server) })
	engine := &fakeEngine{results: []EngineResult{{Status: "completed", Message: "Jurnal tercatat."}}}
	server.runtime.SetEngine(engine)

	postRoomMessage(t, server, project, roomID, "/journal")
	prompt := waitForPrompt(t, engine)

	if strings.Contains(prompt, "Task: /journal") {
		t.Errorf("the raw command leaked into the task title: %q", prompt)
	}
	if !strings.Contains(prompt, "action:journal") {
		t.Errorf("agent was not told to use the journal mechanism: %q", prompt)
	}
	if !strings.Contains(prompt, "memory tool") {
		t.Errorf("instruction must name the tool: %q", prompt)
	}
}

// TestSlashHelpAnswersWithoutARun: a control verb that needs no model call must
// not spend a run (and must not look frozen while one starts).
func TestSlashHelpAnswersWithoutARun(t *testing.T) {
	server, project, roomID := newChatTestServer(t)
	t.Cleanup(func() { waitForRuns(t, server) })
	engine := &fakeEngine{}
	server.runtime.SetEngine(engine)

	payload := postRoomMessage(t, server, project, roomID, "/help")
	if payload["replied"] != "command" {
		t.Errorf("expected a native command reply, got %#v", payload)
	}
	if run, ok := payload["run"]; ok && run != nil {
		t.Errorf("/help must not start a run, got %#v", run)
	}
	// Give any accidental background run a moment to appear.
	time.Sleep(30 * time.Millisecond)
	if prompts := engine.promptCalls(); len(prompts) != 0 {
		t.Errorf("/help started a run: %q", prompts[0])
	}
}

// TestSlashCommandPreservesArguments ensures "/fix <target>" keeps the target:
// dropping it would make the rewrite worse than the raw text.
func TestSlashCommandPreservesArguments(t *testing.T) {
	server, project, roomID := newChatTestServer(t)
	t.Cleanup(func() { waitForRuns(t, server) })
	engine := &fakeEngine{results: []EngineResult{{Status: "completed", Message: "ok"}}}
	server.runtime.SetEngine(engine)

	postRoomMessage(t, server, project, roomID, "/fix ticket-mutation.service.ts:737")
	prompt := waitForPrompt(t, engine)
	if !strings.Contains(prompt, "ticket-mutation.service.ts:737") {
		t.Errorf("command argument was lost: %q", prompt)
	}
}

// TestOrdinaryMessageIsNotTreatedAsCommand: a path that looks slash-like inside
// prose must pass through untouched.
func TestOrdinaryMessageIsNotTreatedAsCommand(t *testing.T) {
	server, project, roomID := newChatTestServer(t)
	t.Cleanup(func() { waitForRuns(t, server) })
	engine := &fakeEngine{results: []EngineResult{{Status: "completed", Message: "ok"}}}
	server.runtime.SetEngine(engine)

	msg := "tolong cek src/services/journal lalu perbaiki"
	postRoomMessage(t, server, project, roomID, msg)
	prompt := waitForPrompt(t, engine)
	// The task must carry the user's own words, not a rewritten command.
	if !strings.Contains(prompt, "Task: "+msg) {
		t.Errorf("the user's message must reach the agent intact: %q", prompt)
	}
	// The journal REWRITE must not appear. (The contract line that mentions
	// "action:journal" is always present, so assert on the rewrite's own words.)
	if strings.Contains(prompt, "Write a journal entry for this room's recent work now") {
		t.Errorf("a path in prose was mistaken for the journal command: %q", prompt)
	}
}

// TestReplyLanguageIsStatedInTheRunPrompt: the language the user writes in must
// be resolved and stated, not left to a buried contract bullet.
func TestReplyLanguageIsStatedInTheRunPrompt(t *testing.T) {
	server, project, roomID := newChatTestServer(t)
	t.Cleanup(func() { waitForRuns(t, server) })
	engine := &fakeEngine{results: []EngineResult{{Status: "completed", Message: "ok"}}}
	server.runtime.SetEngine(engine)

	postRoomMessage(t, server, project, roomID,
		"tolong cek kenapa status open tidak ada di database staging dan perbaiki seed-nya ya")
	prompt := waitForPrompt(t, engine)
	if !strings.Contains(prompt, "Reply language (enforced)") {
		t.Fatalf("run prompt has no enforced language section:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Indonesian") {
		t.Errorf("expected Indonesian to be detected in:\n%s", prompt)
	}
}

// TestCoordinatorRunReceivesDelegationTools proves the role wiring end to end:
// the run request for a coordinator must allow spawn_agent/create_task, because
// the prompt tells it to delegate.
func TestCoordinatorRunReceivesDelegationTools(t *testing.T) {
	server, project, roomID := newChatTestServer(t)
	t.Cleanup(func() { waitForRuns(t, server) })
	engine := &fakeEngine{results: []EngineResult{{Status: "completed", Message: "ok"}}}
	server.runtime.SetEngine(engine)

	postRoomMessage(t, server, project, roomID, "tolong perbaiki bug status tiket di staging")
	prompt := waitForPrompt(t, engine)

	// The prompt must promise delegation…
	if !strings.Contains(prompt, "spawn_agent") {
		t.Fatal("coordinator prompt should tell it to delegate")
	}
	// …and the tool filter must actually permit it. Resolve the coordinator the
	// same way the runtime does, from the stored workspace snapshot.
	st := server.store
	state := st.Snapshot()
	var room Room
	for _, r := range state.Rooms {
		if r.ID == roomID {
			room = r
			break
		}
	}
	if len(room.TeamIDs) == 0 {
		t.Fatal("room has no team")
	}
	var team *Team
	for i := range state.Teams {
		if state.Teams[i].ID == room.TeamIDs[0] {
			team = &state.Teams[i]
			break
		}
	}
	if team == nil {
		t.Fatal("team not found")
	}
	task := Task{AgentName: team.MainAgent, Title: "fix"}
	runAgent := activeTeamAgent(team, task)
	if resolveRoleType(*team, task.AgentName, runAgent) != "coordinator" {
		t.Fatalf("expected a coordinator run")
	}
	allowed := allowedADKToolNames(withDelegationTools(agentToolIDs(team, task)))
	for _, want := range []string{"spawn_agent", "create_task", "list_tasks"} {
		if !allowed[want] {
			t.Errorf("coordinator run is missing %q", want)
		}
	}
}
