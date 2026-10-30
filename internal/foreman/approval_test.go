package foreman

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
)

// The approvals table lives in SQLite. Reading it back from the (never
// populated) in-memory workspace copy meant GetApproval could not see an
// approval that CreateApproval had just written — so every approval side
// effect, including request_spawn, returned early and did nothing.
func TestGetApprovalReadsDatabase(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.CreateApproval(ApprovalRecord{
		RoomID: "r1", AgentName: "Orion", Type: "request_spawn",
		Description: "spawn Ferro", PayloadJSON: `{"action":"request_spawn"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := st.GetApproval(id)
	if !ok {
		t.Fatal("GetApproval cannot see an approval that CreateApproval just wrote — approval side effects are unreachable")
	}
	if got.AgentName != "Orion" || got.Type != "request_spawn" || got.Status != "pending" {
		t.Fatalf("unexpected approval: %#v", got)
	}
	if _, ok := st.GetApproval("does-not-exist"); ok {
		t.Fatal("GetApproval must not invent an approval for an unknown id")
	}
}

func resolveApprovalReq(t *testing.T, server *Server, project, approvalID, status string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/api/approvals/" + approvalID + "/resolve?project=" + url.QueryEscape(project)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"status":"`+status+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:1234"
	req.Host = "127.0.0.1:8080"
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	return res
}

type approvalResolveResponse struct {
	Status  string    `json:"status"`
	Run     *AgentRun `json:"run"`
	Warning string    `json:"warning"`
}

func decodeApprovalResolve(t *testing.T, res *httptest.ResponseRecorder) approvalResolveResponse {
	t.Helper()
	var payload approvalResolveResponse
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func waitNoRunningRuns(t *testing.T, st *Store) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		running := false
		for _, run := range st.Snapshot().Runs {
			if run.Status == "running" {
				running = true
			}
		}
		if !running {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("run did not finish in time")
}

// newApprovalTestServer stops the runtime watchdog when the test ends: this
// file runs before the goroutine-leak checks in the runtime tests, so a server
// left running its watchdog would surface there as a leak.
func newApprovalTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	server, project := newTestServer(t)
	t.Cleanup(server.runtime.stopWatchdog)
	return server, project
}

// A human decision on an agent's question has to reach that agent: before this,
// resolving wrote a status row and stopped, so the room went silent no matter
// what the human clicked in the Approvals Inbox.
func TestApprovalDecisionWakesAskingAgent(t *testing.T) {
	server, project := newApprovalTestServer(t)
	st := server.store
	ensureFakeProvider(t, st)
	team, err := st.ImportTeam("quick-fix", "Quick Fix")
	if err != nil {
		t.Fatal(err)
	}
	room, err := st.CreateRoom("Approval Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	approvalID, err := st.CreateApproval(ApprovalRecord{
		RoomID: room.ID, AgentName: team.MainAgent, Type: "question",
		Description: "Proceed with the schema migration?",
	})
	if err != nil {
		t.Fatal(err)
	}
	server.runtime.SetEngine(&fakeEngine{results: []EngineResult{
		{Status: "completed", Message: "Migration started as approved."},
	}})
	defer server.runtime.stopWatchdog()

	res := resolveApprovalReq(t, server, project, approvalID, "approved")
	if res.Code != http.StatusOK {
		t.Fatalf("resolve status = %d: %s", res.Code, res.Body.String())
	}
	payload := decodeApprovalResolve(t, res)
	if payload.Run == nil {
		t.Fatalf("approving a question must resume the agent, warning = %q", payload.Warning)
	}
	waitNoRunningRuns(t, st)

	var decision, reply string
	for _, m := range st.Snapshot().Rooms[0].Messages {
		if m.Role == "user" && strings.Contains(m.Content, "schema migration") {
			decision = m.Content
		}
		if m.Role == "agent" {
			reply = m.Content
		}
	}
	if !strings.Contains(decision, "You approved the question") {
		t.Fatalf("the decision never reached the transcript: %q", decision)
	}
	if !strings.Contains(reply, "Migration started as approved.") {
		t.Fatalf("the agent never answered the decision: %q", reply)
	}
}

// Every approval type must leave something the team can see when it is
// resolved — a chat message, a notice, or a created task. This is the
// invariant the bug class violated: an approval that resolves into nothing.
func TestEveryApprovalTypeLeavesATraceWhenResolved(t *testing.T) {
	cases := []struct {
		name    string
		typ     string
		payload string
		status  string
	}{
		{name: "question approved", typ: "question", status: "approved"},
		{name: "question rejected", typ: "question", status: "rejected"},
		{name: "escalation approved", typ: "escalation", status: "approved"},
		{name: "escalation rejected", typ: "escalation", status: "rejected"},
		{name: "review approved", typ: "review", status: "approved"},
		{name: "request_spawn rejected", typ: "request_spawn", payload: `{"action":"request_spawn","agent_name":"Quick Coordinator"}`, status: "rejected"},
		{name: "command with unknown payload", typ: "command", payload: `{"action":"unknown"}`, status: "approved"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, project := newApprovalTestServer(t)
			st := server.store
			ensureFakeProvider(t, st)
			team, err := st.ImportTeam("quick-fix", "Quick Fix")
			if err != nil {
				t.Fatal(err)
			}
			room, err := st.CreateRoom("Trace Room", "", []string{team.ID})
			if err != nil {
				t.Fatal(err)
			}
			before := len(st.Snapshot().Rooms[0].Messages)
			approvalID, err := st.CreateApproval(ApprovalRecord{
				RoomID: room.ID, AgentName: team.MainAgent, Type: tc.typ,
				Description: "Decide about the release", PayloadJSON: tc.payload,
			})
			if err != nil {
				t.Fatal(err)
			}
			server.runtime.SetEngine(&fakeEngine{results: []EngineResult{
				{Status: "completed", Message: "Understood, continuing."},
			}})
			defer server.runtime.stopWatchdog()

			res := resolveApprovalReq(t, server, project, approvalID, tc.status)
			if res.Code != http.StatusOK {
				t.Fatalf("resolve status = %d: %s", res.Code, res.Body.String())
			}
			waitNoRunningRuns(t, st)

			after := st.Snapshot()
			if len(after.Rooms[0].Messages) <= before {
				t.Fatalf("resolving %s left no message in the room (before=%d after=%d)",
					tc.typ, before, len(after.Rooms[0].Messages))
			}
			if got, _ := st.GetApproval(approvalID); got.Status != tc.status {
				t.Fatalf("approval status = %q, want %q", got.Status, tc.status)
			}
		})
	}
}

// request_spawn is the one approval with a hard side effect: approving must
// create the teammate's task and queue it to run.
func TestApprovedRequestSpawnCreatesTask(t *testing.T) {
	server, project := newApprovalTestServer(t)
	st := server.store
	ensureFakeProvider(t, st)
	team, err := st.ImportTeam("quick-fix", "Quick Fix")
	if err != nil {
		t.Fatal(err)
	}
	room, err := st.CreateRoom("Spawn Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	target := team.Agents[0].Name
	approvalID, err := st.CreateApproval(ApprovalRecord{
		RoomID: room.ID, AgentName: team.MainAgent, Type: "request_spawn",
		Description: "spawn " + target,
		PayloadJSON: `{"action":"request_spawn","agent_name":"` + target + `","title":"Triage the flaky test","desc":"reproduce first"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	server.runtime.SetEngine(&fakeEngine{results: []EngineResult{{Status: "completed", Message: "working"}}})
	defer server.runtime.stopWatchdog()

	res := resolveApprovalReq(t, server, project, approvalID, "approved")
	if res.Code != http.StatusOK {
		t.Fatalf("resolve status = %d: %s", res.Code, res.Body.String())
	}
	waitNoRunningRuns(t, st)

	var found bool
	for _, task := range st.Snapshot().Tasks {
		if task.RoomID == room.ID && task.Title == "Triage the flaky test" && task.AgentName == target {
			found = true
		}
	}
	if !found {
		t.Fatal("approving request_spawn did not create the delegated task")
	}
	notice := false
	for _, m := range st.Snapshot().Rooms[0].Messages {
		if m.ActivityType == "agent.spawned" {
			notice = true
		}
	}
	if !notice {
		t.Fatal("no chat notice reports the approved spawn")
	}
}

// toolRunner is the callable shape of an ADK tool (the exported tool.Tool
// interface has no Run).
type toolRunner interface {
	Name() string
	Run(ctx agent.Context, args any) (map[string]any, error)
}

// initTestRepo creates a git repository with a local identity so commits work
// regardless of the machine's global git config.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v — %s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "foreman@test")
	git("config", "user.name", "Foreman")
	return dir
}

// commitHead returns the subject of HEAD, or "" when there is no commit yet.
func commitHead(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "log", "-1", "--pretty=%s")
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	return strings.TrimSpace(string(out))
}

// git_ops commit used to rely on ADK's confirmation event, which nothing in
// Foreman ever answered: no commit ran and nobody was asked. A commit is now a
// request that only executes after a human approves it, with the result posted
// to the room.
func TestGitCommitRequiresHumanApproval(t *testing.T) {
	server, project := newApprovalTestServer(t)
	st := server.store
	team, err := st.ImportTeam("quick-fix", "Quick Fix")
	if err != nil {
		t.Fatal(err)
	}
	room, err := st.CreateRoom("Commit Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	repo := initTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "note.txt"), []byte("change\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var gitOps toolRunner
	for _, tl := range makeADKTools(adkToolContext{Store: st, RoomID: room.ID, Workspace: repo, AgentName: team.MainAgent}) {
		if tl.Name() == "git_ops" {
			if runner, ok := tl.(toolRunner); ok {
				gitOps = runner
			}
		}
	}
	if gitOps == nil {
		t.Fatal("git_ops tool is not registered")
	}
	result, err := gitOps.Run(&agent.ContextMock{}, map[string]any{"operation": "commit", "message": "record the note"})
	if err != nil {
		t.Fatalf("git_ops commit: %v", err)
	}
	// Depending on the ADK version the tool's own return value is either the
	// map itself or wrapped under "result".
	detail := result
	if nested, ok := result["result"].(map[string]any); ok {
		detail = nested
	}
	if detail["status"] != "awaiting_human_approval" {
		t.Fatalf("commit must request approval, got %#v", result)
	}
	if head := commitHead(t, repo); head == "record the note" {
		t.Fatal("commit executed before a human approved it")
	}
	approvals, err := st.ListApprovals(room.ID)
	if err != nil {
		t.Fatal(err)
	}
	var approvalID string
	for _, a := range approvals {
		if a.Type == "command" && a.Status == "pending" {
			approvalID = a.ID
		}
	}
	if approvalID == "" {
		t.Fatal("no pending approval was created for the commit")
	}

	// Human approves → the commit runs and its result is posted to the room.
	res := resolveApprovalReq(t, server, project, approvalID, "approved")
	if res.Code != http.StatusOK {
		t.Fatalf("resolve status = %d: %s", res.Code, res.Body.String())
	}
	if head := commitHead(t, repo); head != "record the note" {
		t.Fatalf("approved commit did not run, HEAD = %q", head)
	}
	var commitNotice bool
	for _, m := range st.Snapshot().Rooms[0].Messages {
		if m.ActivityType == "approval.resolved" && strings.Contains(m.Content, "Commit approved and created") {
			commitNotice = true
		}
	}
	if !commitNotice {
		t.Fatal("the approved commit result was never posted to the room")
	}
}

// A rejected commit must not run and must be visible in the room.
func TestRejectedCommitDoesNotRun(t *testing.T) {
	server, project := newApprovalTestServer(t)
	st := server.store
	team, err := st.ImportTeam("quick-fix", "Quick Fix")
	if err != nil {
		t.Fatal(err)
	}
	room, err := st.CreateRoom("Reject Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	repo := initTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	approvalID, err := st.CreateApproval(ApprovalRecord{
		RoomID: room.ID, AgentName: team.MainAgent, Type: "command",
		Description: "🔐 Commit requested: nope",
		PayloadJSON: mustMarshal(map[string]any{"action": "git_commit", "message": "nope", "workspace": repo}),
	})
	if err != nil {
		t.Fatal(err)
	}
	res := resolveApprovalReq(t, server, project, approvalID, "rejected")
	if res.Code != http.StatusOK {
		t.Fatalf("resolve status = %d: %s", res.Code, res.Body.String())
	}
	if head := commitHead(t, repo); head == "nope" {
		t.Fatal("rejected commit executed anyway")
	}
	rejected := false
	for _, m := range st.Snapshot().Rooms[0].Messages {
		if m.ActivityType == "approval.resolved" && strings.Contains(m.Content, "rejected") {
			rejected = true
		}
	}
	if !rejected {
		t.Fatal("the rejection was never posted to the room")
	}
}
