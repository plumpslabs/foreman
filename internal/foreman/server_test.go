package foreman

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	project := t.TempDir()
	store, err := openStoreForTest(t, project)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(store, project)
	if err != nil {
		t.Fatal(err)
	}
	return server, project
}

func TestDashboardServesEmbeddedUIAndWorkspaceAPI(t *testing.T) {
	server, project := newTestServer(t)
	handler := server.Handler()
	root := httptest.NewRecorder()
	rootReq := httptest.NewRequest(http.MethodGet, "/", nil)
	rootReq.RemoteAddr = "127.0.0.1:1234"
	rootReq.Host = "127.0.0.1:8080"
	handler.ServeHTTP(root, rootReq)
	if root.Code != http.StatusOK || !strings.Contains(root.Body.String(), "Foreman") {
		t.Fatalf("dashboard response = %d %s", root.Code, root.Body.String())
	}

	path := "/api/workspace?project=" + url.QueryEscape(project)
	response := httptest.NewRecorder()
	workspaceReq := httptest.NewRequest(http.MethodGet, path, nil)
	workspaceReq.RemoteAddr = "127.0.0.1:1234"
	workspaceReq.Host = "127.0.0.1:8080"
	handler.ServeHTTP(response, workspaceReq)
	if response.Code != http.StatusOK {
		t.Fatalf("workspace response status = %d: %s", response.Code, response.Body.String())
	}
	var state WorkspaceState
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Templates) != 5 || state.ProjectPath != project || state.Settings.MaxConcurrentRuns != 4 || len(state.Tasks) != 0 {
		t.Fatalf("unexpected workspace response: %#v", state)
	}
}

func TestDeleteRoomRemovesItsTasksAndRuns(t *testing.T) {
	server, project := newTestServer(t)
	handler := server.Handler()
	base := "?project=" + url.QueryEscape(project)

	roomBody := bytes.NewBufferString(`{"name":"Temp Room","goal":"g"}`)
	roomReq := httptest.NewRequest(http.MethodPost, "/api/rooms"+base, roomBody)
	roomReq.Header.Set("Content-Type", "application/json")
	roomReq.RemoteAddr = "127.0.0.1:1234"
	roomReq.Host = "127.0.0.1:8080"
	roomRes := httptest.NewRecorder()
	handler.ServeHTTP(roomRes, roomReq)
	if roomRes.Code != http.StatusCreated {
		t.Fatalf("create room status = %d: %s", roomRes.Code, roomRes.Body.String())
	}
	var room Room
	if err := json.Unmarshal(roomRes.Body.Bytes(), &room); err != nil {
		t.Fatal(err)
	}

	taskBody := bytes.NewBufferString(fmt.Sprintf(`{"title":"seed","roomId":%q}`, room.ID))
	taskReq := httptest.NewRequest(http.MethodPost, "/api/rooms/"+room.ID+"/tasks"+base, taskBody)
	taskReq.Header.Set("Content-Type", "application/json")
	taskReq.RemoteAddr = "127.0.0.1:1234"
	taskReq.Host = "127.0.0.1:8080"
	taskRes := httptest.NewRecorder()
	handler.ServeHTTP(taskRes, taskReq)
	if taskRes.Code != http.StatusCreated {
		t.Fatalf("create task status = %d: %s", taskRes.Code, taskRes.Body.String())
	}
	var task Task
	if err := json.Unmarshal(taskRes.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/api/rooms/"+room.ID+base, nil)
	delReq.RemoteAddr = "127.0.0.1:1234"
	delReq.Host = "127.0.0.1:8080"
	delRes := httptest.NewRecorder()
	handler.ServeHTTP(delRes, delReq)
	if delRes.Code != http.StatusNoContent {
		t.Fatalf("delete room status = %d: %s", delRes.Code, delRes.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/workspace"+base, nil)
	getReq.RemoteAddr = "127.0.0.1:1234"
	getReq.Host = "127.0.0.1:8080"
	getRes := httptest.NewRecorder()
	handler.ServeHTTP(getRes, getReq)
	var state WorkspaceState
	if err := json.Unmarshal(getRes.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	for _, r := range state.Rooms {
		if r.ID == room.ID {
			t.Fatalf("room %q still present after delete", room.ID)
		}
	}
	for _, tk := range state.Tasks {
		if tk.ID == task.ID {
			t.Fatalf("task %q still present after room delete", task.ID)
		}
	}

	// Deleting again must 422.
	del2Req := httptest.NewRequest(http.MethodDelete, "/api/rooms/"+room.ID+base, nil)
	del2Req.RemoteAddr = "127.0.0.1:1234"
	del2Req.Host = "127.0.0.1:8080"
	del2Res := httptest.NewRecorder()
	handler.ServeHTTP(del2Res, del2Req)
	if del2Res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("second delete status = %d, want 422", del2Res.Code)
	}
}

func TestChatMessageCreatesHiddenTask(t *testing.T) {
	server, project := newTestServer(t)
	handler := server.Handler()
	base := "?project=" + url.QueryEscape(project)

	importBody := bytes.NewBufferString(`{"templateId":"quick-fix","name":"Chat Team"}`)
	importReq := httptest.NewRequest(http.MethodPost, "/api/teams/import"+base, importBody)
	importReq.Header.Set("Content-Type", "application/json")
	importReq.RemoteAddr = "127.0.0.1:1234"
	importReq.Host = "127.0.0.1:8080"
	importRes := httptest.NewRecorder()
	handler.ServeHTTP(importRes, importReq)
	var team Team
	if err := json.Unmarshal(importRes.Body.Bytes(), &team); err != nil {
		t.Fatal(err)
	}

	roomBody := bytes.NewBufferString(fmt.Sprintf(`{"name":"Chat Room","teamIds":[%q]}`, team.ID))
	roomReq := httptest.NewRequest(http.MethodPost, "/api/rooms"+base, roomBody)
	roomReq.Header.Set("Content-Type", "application/json")
	roomReq.RemoteAddr = "127.0.0.1:1234"
	roomReq.Host = "127.0.0.1:8080"
	roomRes := httptest.NewRecorder()
	handler.ServeHTTP(roomRes, roomReq)
	var room Room
	if err := json.Unmarshal(roomRes.Body.Bytes(), &room); err != nil {
		t.Fatal(err)
	}

	msgBody := bytes.NewBufferString(`{"role":"user","content":"@Quick Coordinator cek perubahan"}`)
	msgReq := httptest.NewRequest(http.MethodPost, "/api/rooms/"+room.ID+"/messages"+base, msgBody)
	msgReq.Header.Set("Content-Type", "application/json")
	msgReq.RemoteAddr = "127.0.0.1:1234"
	msgReq.Host = "127.0.0.1:8080"
	msgRes := httptest.NewRecorder()
	handler.ServeHTTP(msgRes, msgReq)
	if msgRes.Code != http.StatusCreated {
		t.Fatalf("message status = %d: %s", msgRes.Code, msgRes.Body.String())
	}

	stateReq := httptest.NewRequest(http.MethodGet, "/api/workspace"+base, nil)
	stateReq.RemoteAddr = "127.0.0.1:1234"
	stateReq.Host = "127.0.0.1:8080"
	stateRes := httptest.NewRecorder()
	handler.ServeHTTP(stateRes, stateReq)
	var state WorkspaceState
	if err := json.Unmarshal(stateRes.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Rooms[0].Messages) < 1 || state.Rooms[0].Messages[0].Role != "user" {
		t.Fatalf("expected user chat message first, got %d messages", len(state.Rooms[0].Messages))
	}
	if len(state.Tasks) != 1 {
		t.Fatalf("expected one hidden chat task, got %d", len(state.Tasks))
	}
	task := state.Tasks[0]
	if !task.Hidden || task.RoomID != room.ID || task.TeamID != team.ID || task.AgentName != "Quick Coordinator" {
		t.Fatalf("unexpected chat task: %#v", task)
	}
}

func TestImportTeamAndCreateRoomAPIs(t *testing.T) {
	server, project := newTestServer(t)
	handler := server.Handler()
	base := "?project=" + url.QueryEscape(project)

	importBody := bytes.NewBufferString(`{"templateId":"quick-fix","name":"Quick fix for app"}`)
	importReq := httptest.NewRequest(http.MethodPost, "/api/teams/import"+base, importBody)
	importReq.Header.Set("Content-Type", "application/json")
	importReq.RemoteAddr = "127.0.0.1:1234"
	importReq.Host = "127.0.0.1:8080"
	importRes := httptest.NewRecorder()
	handler.ServeHTTP(importRes, importReq)
	if importRes.Code != http.StatusCreated {
		t.Fatalf("team import status = %d: %s", importRes.Code, importRes.Body.String())
	}
	var team Team
	if err := json.Unmarshal(importRes.Body.Bytes(), &team); err != nil {
		t.Fatal(err)
	}

	roomBody := bytes.NewBufferString(fmt.Sprintf(`{"name":"First room","goal":"Ship a focused fix","teamIds":[%q]}`, team.ID))
	roomReq := httptest.NewRequest(http.MethodPost, "/api/rooms"+base, roomBody)
	roomReq.Header.Set("Content-Type", "application/json")
	roomReq.RemoteAddr = "127.0.0.1:1234"
	roomReq.Host = "127.0.0.1:8080"
	roomRes := httptest.NewRecorder()
	handler.ServeHTTP(roomRes, roomReq)
	if roomRes.Code != http.StatusCreated {
		t.Fatalf("room creation status = %d: %s", roomRes.Code, roomRes.Body.String())
	}
	var room Room
	if err := json.Unmarshal(roomRes.Body.Bytes(), &room); err != nil {
		t.Fatal(err)
	}
	if room.Name != "First room" || len(room.TeamIDs) != 1 || room.TeamIDs[0] != team.ID {
		t.Fatalf("unexpected room response: %#v", room)
	}

	messageBody := bytes.NewBufferString(`{"role":"user","content":"Please inspect this project now"}`)
	messageReq := httptest.NewRequest(http.MethodPost, "/api/rooms/"+room.ID+"/messages"+base, messageBody)
	messageReq.RemoteAddr = "127.0.0.1:1234"
	messageReq.Host = "127.0.0.1:8080"
	messageRes := httptest.NewRecorder()
	handler.ServeHTTP(messageRes, messageReq)
	if messageRes.Code != http.StatusCreated {
		t.Fatalf("room message status = %d: %s", messageRes.Code, messageRes.Body.String())
	}
	var messageEnvelope struct {
		Message ChatMessage `json:"message"`
		Run     *AgentRun   `json:"run"`
		Warning string      `json:"warning"`
	}
	if err := json.Unmarshal(messageRes.Body.Bytes(), &messageEnvelope); err != nil {
		t.Fatal(err)
	}
	message := messageEnvelope.Message
	if message.Role != "user" || message.Content != "Please inspect this project now" {
		t.Fatalf("unexpected chat message: %#v", message)
	}
	stateRes := httptest.NewRecorder()
	stateReq := httptest.NewRequest(http.MethodGet, "/api/workspace"+base, nil)
	stateReq.RemoteAddr = "127.0.0.1:1234"
	stateReq.Host = "127.0.0.1:8080"
	handler.ServeHTTP(stateRes, stateReq)
	var state WorkspaceState
	if err := json.Unmarshal(stateRes.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Rooms[0].Messages) != 1 || state.Rooms[0].Messages[0].ID != message.ID {
		found := false
		for _, m := range state.Rooms[0].Messages {
			if m.ID == message.ID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("chat message was not persisted with room: %#v", state.Rooms[0])
		}
	}
}

func TestWorkspaceAPIRejectsWrongProjectAndInvalidJSON(t *testing.T) {
	server, project := newTestServer(t)
	wrong := t.TempDir()
	path := "/api/workspace?project=" + url.QueryEscape(wrong)
	response := httptest.NewRecorder()
	wrongProjectReq := httptest.NewRequest(http.MethodGet, path, nil)
	wrongProjectReq.RemoteAddr = "127.0.0.1:1234"
	wrongProjectReq.Host = "127.0.0.1:8080"
	server.Handler().ServeHTTP(response, wrongProjectReq)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("wrong project status = %d", response.Code)
	}

	badBody := bytes.NewBufferString(`{"templateId":"quick-fix","name":"x","extra":"unknown"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/teams/import?project="+url.QueryEscape(project), badBody)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Host = "127.0.0.1:8080"
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("unknown JSON field status = %d", res.Code)
	}
}

func TestLocalOnlyMiddlewareRejectsRemoteAndCrossOriginRequests(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.localOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	remote := httptest.NewRequest(http.MethodGet, "/", nil)
	remote.RemoteAddr = "192.0.2.10:1234"
	remoteRes := httptest.NewRecorder()
	handler.ServeHTTP(remoteRes, remote)
	if remoteRes.Code != http.StatusForbidden {
		t.Fatalf("remote request status = %d", remoteRes.Code)
	}

	crossOrigin := httptest.NewRequest(http.MethodPost, "/", nil)
	crossOrigin.RemoteAddr = "127.0.0.1:1234"
	crossOrigin.Host = "127.0.0.1:8080"
	crossOrigin.Header.Set("Origin", "http://attacker.example")
	originRes := httptest.NewRecorder()
	handler.ServeHTTP(originRes, crossOrigin)
	if originRes.Code != http.StatusForbidden {
		t.Fatalf("cross-origin request status = %d", originRes.Code)
	}

	localOrigin := httptest.NewRequest(http.MethodPost, "/", nil)
	localOrigin.RemoteAddr = "127.0.0.1:1234"
	localOrigin.Host = "localhost:8080"
	localOrigin.Header.Set("Origin", "http://localhost:8080")
	localRes := httptest.NewRecorder()
	handler.ServeHTTP(localRes, localOrigin)
	if localRes.Code != http.StatusNoContent {
		t.Fatalf("same-origin local request status = %d", localRes.Code)
	}
}

func TestTasksSettingsAndSkillsAPI(t *testing.T) {
	server, project := newTestServer(t)
	handler := server.Handler()
	base := "?project=" + url.QueryEscape(project)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path+base, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Host = "127.0.0.1:8080"
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	teamRes := call(http.MethodPost, "/api/teams/import", `{"templateId":"quick-fix","name":"Team"}`)
	var team Team
	if err := json.Unmarshal(teamRes.Body.Bytes(), &team); err != nil || teamRes.Code != http.StatusCreated {
		t.Fatalf("team: %d %s %v", teamRes.Code, teamRes.Body.String(), err)
	}
	roomRes := call(http.MethodPost, "/api/rooms", fmt.Sprintf(`{"name":"Room","teamIds":[%q]}`, team.ID))
	var room Room
	if err := json.Unmarshal(roomRes.Body.Bytes(), &room); err != nil || roomRes.Code != http.StatusCreated {
		t.Fatalf("room: %d %s %v", roomRes.Code, roomRes.Body.String(), err)
	}
	taskRes := call(http.MethodPost, "/api/rooms/"+room.ID+"/tasks", `{"title":"first task","description":"bounded"}`)
	var task Task
	if err := json.Unmarshal(taskRes.Body.Bytes(), &task); err != nil || taskRes.Code != http.StatusCreated {
		t.Fatalf("task: %d %s %v", taskRes.Code, taskRes.Body.String(), err)
	}
	if task.Status != "todo" {
		t.Fatalf("status = %q", task.Status)
	}
	blockedRes := call(http.MethodPost, "/api/rooms/"+room.ID+"/tasks", fmt.Sprintf(`{"title":"dependent","dependencies":[%q]}`, task.ID))
	var dependent Task
	if err := json.Unmarshal(blockedRes.Body.Bytes(), &dependent); err != nil || blockedRes.Code != http.StatusCreated {
		t.Fatalf("dependent task: %d %s %v", blockedRes.Code, blockedRes.Body.String(), err)
	}
	if dependent.Status != "blocked" {
		t.Fatalf("dependent status = %q", dependent.Status)
	}
	settingsRes := call(http.MethodPut, "/api/settings", `{"theme":"light","effortMode":"manual","defaultEffort":"high","maxConcurrentRuns":2,"maxWorkersPerTask":1,"maxDelegationDepth":0,"maxRetries":0,"maxExecutionSeconds":60,"budgetUsd":1.5,"requireRunApproval":true,"allowProjectCommands":false}`)
	if settingsRes.Code != http.StatusOK {
		t.Fatalf("settings: %d %s", settingsRes.Code, settingsRes.Body.String())
	}
	skillRes := call(http.MethodPost, "/api/skills", `{"name":"Go tests","description":"Run tests","instructions":"Use go test ./...","enabled":true}`)
	var skill Skill
	if err := json.Unmarshal(skillRes.Body.Bytes(), &skill); err != nil || skillRes.Code != http.StatusCreated {
		t.Fatalf("skill: %d %s %v", skillRes.Code, skillRes.Body.String(), err)
	}
	stateRes := call(http.MethodGet, "/api/workspace", "")
	var state WorkspaceState
	if err := json.Unmarshal(stateRes.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Settings.Theme != "light" || len(state.Tasks) != 2 || state.Tasks[0].Status != "blocked" {
		t.Fatalf("unexpected persisted state: %#v", state)
	}
	foundSkill := false
	for _, item := range state.Skills {
		if item.Name == "Go tests" {
			foundSkill = true
		}
	}
	if !foundSkill {
		t.Fatalf("created skill was not persisted: %#v", state.Skills)
	}
	if got := call(http.MethodPost, "/api/tasks/"+dependent.ID+"/run", `{}`); got.Code != http.StatusUnprocessableEntity {
		t.Fatalf("blocked task run status = %d %s", got.Code, got.Body.String())
	}
}

func TestTaskEditAndPriorityNormalizationAPI(t *testing.T) {
	server, project := newTestServer(t)
	handler := server.Handler()
	base := "?project=" + url.QueryEscape(project)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path+base, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Host = "127.0.0.1:8080"
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	teamRes := call(http.MethodPost, "/api/teams/import", `{"templateId":"quick-fix","name":"Team"}`)
	var team Team
	if err := json.Unmarshal(teamRes.Body.Bytes(), &team); err != nil || teamRes.Code != http.StatusCreated {
		t.Fatalf("team: %d %s %v", teamRes.Code, teamRes.Body.String(), err)
	}
	roomRes := call(http.MethodPost, "/api/rooms", fmt.Sprintf(`{"name":"Room","teamIds":[%q]}`, team.ID))
	var room Room
	if err := json.Unmarshal(roomRes.Body.Bytes(), &room); err != nil || roomRes.Code != http.StatusCreated {
		t.Fatalf("room: %d %s %v", roomRes.Code, roomRes.Body.String(), err)
	}

	// Legacy "urgent" is normalized to the canonical P0 scale on create.
	taskRes := call(http.MethodPost, "/api/rooms/"+room.ID+"/tasks", `{"title":"legacy priority","priority":"urgent"}`)
	var task Task
	if err := json.Unmarshal(taskRes.Body.Bytes(), &task); err != nil || taskRes.Code != http.StatusCreated {
		t.Fatalf("task: %d %s %v", taskRes.Code, taskRes.Body.String(), err)
	}
	if task.Priority != "p0" {
		t.Fatalf("legacy priority not normalized: %q", task.Priority)
	}

	// Edit title, description, priority and effort via the new endpoint.
	editRes := call(http.MethodPut, "/api/tasks/"+task.ID, `{"title":"edited title","description":"now clearer","priority":"p3","effort":"high"}`)
	var edited Task
	if err := json.Unmarshal(editRes.Body.Bytes(), &edited); err != nil || editRes.Code != http.StatusOK {
		t.Fatalf("edit: %d %s %v", editRes.Code, editRes.Body.String(), err)
	}
	if edited.Title != "edited title" || edited.Description != "now clearer" || edited.Priority != "p3" || edited.Effort != "high" {
		t.Fatalf("task edit not applied: %#v", edited)
	}

	// Invalid priority and empty title are rejected.
	if got := call(http.MethodPut, "/api/tasks/"+task.ID, `{"priority":"nope"}`); got.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid priority status = %d %s", got.Code, got.Body.String())
	}
	if got := call(http.MethodPut, "/api/tasks/"+task.ID, `{"title":"  "}`); got.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty title status = %d %s", got.Code, got.Body.String())
	}
}

func TestOpenStoreRequiresDirectory(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "not-a-dir")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := openStoreForTest(t, file.Name()); err == nil {
		t.Fatal("expected non-directory project path to fail")
	}
}

func TestAgentLibraryAPI(t *testing.T) {
	server, project := newTestServer(t)
	handler := server.Handler()
	base := "?project=" + url.QueryEscape(project)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path+base, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Host = "127.0.0.1:8080"
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}

	created := call(http.MethodPost, "/api/agents", `{"name":"Scout","role":"Researcher","instructions":"read only","tools":["read","search"]}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create agent status = %d: %s", created.Code, created.Body.String())
	}
	var agent Agent
	if err := json.Unmarshal(created.Body.Bytes(), &agent); err != nil {
		t.Fatal(err)
	}
	if agent.ID == "" || agent.Name != "Scout" {
		t.Fatalf("unexpected agent: %#v", agent)
	}

	updated := call(http.MethodPut, "/api/agents/"+agent.ID,
		`{"name":"Scout","role":"Researcher","instructions":"read only","tools":["read"],"requireReview":true}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update agent status = %d: %s", updated.Code, updated.Body.String())
	}

	stateRes := call(http.MethodGet, "/api/workspace", "")
	var state WorkspaceState
	if err := json.Unmarshal(stateRes.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range state.Agents {
		if item.ID == agent.ID && item.RequireReview {
			found = true
		}
	}
	if !found {
		t.Fatal("agent update was not persisted in the workspace")
	}

	deleted := call(http.MethodDelete, "/api/agents/"+agent.ID, "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete agent status = %d", deleted.Code)
	}
}

// questionAnswerResponse is what PUT .../questions/{id} returns: the stored
// answer plus the run it kicked off (or why it could not).
type questionAnswerResponse struct {
	Question AgentQuestion `json:"question"`
	Run      *AgentRun     `json:"run"`
	Warning  string        `json:"warning"`
}

func answerQuestionReq(t *testing.T, server *Server, project, roomID, questionID, body string) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/rooms/%s/questions/%s?project=%s", roomID, questionID, url.QueryEscape(project))
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:1234"
	req.Host = "127.0.0.1:8080"
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	return res
}

// Picking an option on an agent's confirmation card must wake that agent
// again. The answer used to be written to the transcript only, so the room went
// silent the moment the human replied: no reply, no history, no continuation.
func TestAnswerRoomQuestionResumesAskingAgent(t *testing.T) {
	server, project := newTestServer(t)
	st := server.store
	ensureFakeProvider(t, st)
	team, err := st.ImportTeam("quick-fix", "Quick Fix")
	if err != nil {
		t.Fatal(err)
	}
	room, err := st.CreateRoom("Confirm Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	question, err := st.AddRoomQuestion(room.ID, AgentQuestion{
		Prompt:    "Ship the waiting-color fix?",
		Options:   []string{"Yes", "No"},
		AgentName: team.MainAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	server.runtime.SetEngine(&fakeEngine{results: []EngineResult{
		{Status: "completed", Message: "Shipping the fix now."},
	}})
	defer server.runtime.stopWatchdog()

	res := answerQuestionReq(t, server, project, room.ID, question.ID, `{"answer":"Yes, ship it"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("answer status = %d: %s", res.Code, res.Body.String())
	}
	var payload questionAnswerResponse
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Run == nil {
		t.Fatalf("answering a confirmation must start a run, warning = %q", payload.Warning)
	}
	if payload.Question.Answer != "Yes, ship it" || payload.Question.AgentName != team.MainAgent {
		t.Fatalf("unexpected answer payload: %#v", payload.Question)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		running := false
		for _, run := range st.Snapshot().Runs {
			if run.Status == "running" {
				running = true
			}
		}
		if !running {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	runs := st.Snapshot().Runs
	if len(runs) != 1 || runs[0].RoomID != room.ID {
		t.Fatalf("expected exactly one run in the room, got %#v", runs)
	}
	var thread *Task
	for i, task := range st.Snapshot().Tasks {
		if task.RoomID == room.ID && task.Hidden {
			thread = &st.Snapshot().Tasks[i]
		}
	}
	if thread == nil {
		t.Fatal("answering did not create/reuse a chat thread")
	}
	if thread.AgentName != team.MainAgent {
		t.Fatalf("answer routed to %q, want the asking agent %q", thread.AgentName, team.MainAgent)
	}
	if !strings.Contains(thread.Description, "Yes, ship it") {
		t.Fatalf("the agent never received the answer: %q", thread.Description)
	}
	// The resumed agent's reply must land in the room, otherwise the human
	// still sees nothing happen after confirming.
	var reply string
	for _, m := range st.Snapshot().Rooms[0].Messages {
		if m.Role == "agent" {
			reply = m.Content
		}
	}
	if !strings.Contains(reply, "Shipping the fix now.") {
		t.Fatalf("agent did not reply after the confirmation, got %q", reply)
	}
}

// When the run cannot start, the answer must still be kept and the reason must
// be visible instead of failing the request (which would lose the reply).
func TestAnswerRoomQuestionReportsRunFailure(t *testing.T) {
	server, project := newTestServer(t)
	st := server.store
	team, err := st.ImportTeam("quick-fix", "Quick Fix")
	if err != nil {
		t.Fatal(err)
	}
	room, err := st.CreateRoom("Quiet Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	question, err := st.AddRoomQuestion(room.ID, AgentQuestion{
		Prompt:    "Continue with the migration?",
		AgentName: team.MainAgent,
	})
	if err != nil {
		t.Fatal(err)
	}

	res := answerQuestionReq(t, server, project, room.ID, question.ID, `{"answer":"Yes"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("answer status = %d: %s", res.Code, res.Body.String())
	}
	var payload questionAnswerResponse
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Run != nil || payload.Warning == "" {
		t.Fatalf("expected a warning without a run, got run=%#v warning=%q", payload.Run, payload.Warning)
	}
	if payload.Question.Answer != "Yes" {
		t.Fatalf("answer must be stored even when the run fails: %#v", payload.Question)
	}
	notice := false
	for _, m := range st.Snapshot().Rooms[0].Messages {
		if m.Role == "system" && m.ActivityType == "chat.error" {
			notice = true
		}
	}
	if !notice {
		t.Fatal("no chat notice explains why the answer could not be run")
	}
}
