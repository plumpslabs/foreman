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
)

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	project := t.TempDir()
	store, err := OpenStore(project)
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
	if len(state.Templates) != 4 || state.ProjectPath != project || state.Settings.MaxConcurrentRuns != 1 || len(state.Tasks) != 0 {
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

	messageBody := bytes.NewBufferString(`{"role":"user","content":"Please inspect this project"}`)
	messageReq := httptest.NewRequest(http.MethodPost, "/api/rooms/"+room.ID+"/messages"+base, messageBody)
	messageReq.RemoteAddr = "127.0.0.1:1234"
	messageReq.Host = "127.0.0.1:8080"
	messageRes := httptest.NewRecorder()
	handler.ServeHTTP(messageRes, messageReq)
	if messageRes.Code != http.StatusCreated {
		t.Fatalf("room message status = %d: %s", messageRes.Code, messageRes.Body.String())
	}
	var message ChatMessage
	if err := json.Unmarshal(messageRes.Body.Bytes(), &message); err != nil {
		t.Fatal(err)
	}
	if message.Role != "user" || message.Content != "Please inspect this project" {
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
		t.Fatalf("chat message was not persisted with room: %#v", state.Rooms[0])
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

func TestOpenStoreRequiresDirectory(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "not-a-dir")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := OpenStore(file.Name()); err == nil {
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
