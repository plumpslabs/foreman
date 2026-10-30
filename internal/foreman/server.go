package foreman

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed all:web/dist
var webFiles embed.FS

type Server struct {
	store   *Store
	root    string
	runtime *Runtime

	sseMu   sync.Mutex
	sseSubs map[chan struct{}]struct{}
}

func NewServer(st *Store, projectPath string) (*Server, error) {
	root, err := filepath.Abs(projectPath)
	if err != nil {
		return nil, err
	}
	srv := &Server{store: st, root: root, runtime: NewRuntime(st), sseSubs: map[chan struct{}]struct{}{}}
	st.SetOnChange(srv.broadcastSSE)
	return srv, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/workspace", s.workspace)
	mux.HandleFunc("POST /api/teams/import", s.importTeam)
	mux.HandleFunc("PUT /api/teams/{teamID}", s.updateTeam)
	mux.HandleFunc("DELETE /api/teams/{teamID}", s.deleteTeam)
	mux.HandleFunc("POST /api/agents", s.saveAgent)
	mux.HandleFunc("PUT /api/agents/{agentID}", s.updateAgent)
	mux.HandleFunc("DELETE /api/agents/{agentID}", s.deleteAgent)
	mux.HandleFunc("GET /api/agents/{agentID}/avatar", s.agentAvatar)
	mux.HandleFunc("POST /api/agents/{agentID}/avatar/shuffle", s.shuffleAgentAvatar)
	mux.HandleFunc("POST /api/rooms", s.createRoom)
	mux.HandleFunc("DELETE /api/rooms/{roomID}", s.deleteRoom)
	mux.HandleFunc("POST /api/rooms/{roomID}/messages", s.appendRoomMessage)
	mux.HandleFunc("PUT /api/rooms/{roomID}/teams", s.updateRoomTeams)
	mux.HandleFunc("PUT /api/rooms/{roomID}/questions/{questionID}", s.answerRoomQuestion)
	mux.HandleFunc("POST /api/rooms/{roomID}/tasks", s.createTask)
	mux.HandleFunc("PUT /api/tasks/{taskID}/status", s.updateTaskStatus)
	mux.HandleFunc("PUT /api/tasks/{taskID}", s.updateTaskDetails)
	mux.HandleFunc("POST /api/tasks/{taskID}/run", s.startTask)
	mux.HandleFunc("POST /api/runs/{runID}/cancel", s.cancelRun)
	mux.HandleFunc("POST /api/runs/{runID}/pause", s.pauseRun)
	mux.HandleFunc("POST /api/providers", s.saveProvider)
	mux.HandleFunc("POST /api/providers/test", s.testProvider)
	mux.HandleFunc("POST /api/providers/discover", s.discoverModels)
	mux.HandleFunc("DELETE /api/providers/{providerID}", s.deleteProvider)
	mux.HandleFunc("GET /api/engine/status", s.engineStatus)
	mux.HandleFunc("POST /api/engine/install", s.engineInstall)
	mux.HandleFunc("POST /api/global/reset", s.resetGlobal)
	mux.HandleFunc("PUT /api/settings", s.updateSettings)
	mux.HandleFunc("POST /api/skills", s.saveSkill)
	mux.HandleFunc("DELETE /api/skills/{skillID}", s.deleteSkill)
	mux.HandleFunc("GET /api/rooms/{roomID}/plan", s.getRoomPlan)
	mux.HandleFunc("PUT /api/rooms/{roomID}/plan", s.saveRoomPlan)
	mux.HandleFunc("GET /api/rooms/{roomID}/artifacts", s.listRoomArtifacts)
	mux.HandleFunc("POST /api/rooms/{roomID}/artifacts", s.saveRoomArtifact)
	mux.HandleFunc("GET /api/rooms/{roomID}/approvals", s.listRoomApprovals)
	mux.HandleFunc("GET /api/approvals", s.listAllApprovals)
	mux.HandleFunc("POST /api/approvals/{approvalID}/resolve", s.resolveApproval)
	mux.HandleFunc("GET /api/rooms/{roomID}/panels", s.listRoomPanels)
	mux.HandleFunc("POST /api/rooms/{roomID}/panels", s.registerRoomPanel)
	mux.HandleFunc("GET /api/rooms/{roomID}/memory", s.getRoomMemory)
	mux.HandleFunc("POST /api/rooms/{roomID}/memory", s.setRoomMemory)
	mux.HandleFunc("GET /api/mcp", s.listMCP)
	mux.HandleFunc("POST /api/mcp", s.saveMCP)
	mux.HandleFunc("PUT /api/mcp/{serverID}", s.updateMCP)
	mux.HandleFunc("DELETE /api/mcp/{serverID}", s.deleteMCP)
	mux.HandleFunc("POST /api/mcp/{serverID}/test", s.testMCP)
	mux.HandleFunc("PUT /api/rooms/{roomID}/mcp", s.setRoomMCP)
	static, err := fs.Sub(webFiles, "web/dist")
	if err != nil {
		panic("foreman: embedded dashboard is missing; run `make web` before building: " + err.Error())
	}
	mux.Handle("GET /", http.FileServer(http.FS(static)))
	return s.localOnly(s.recoverMiddleware(mux))
}

// recoverMiddleware prevents a panic in any handler from tearing down the
// whole server and logs it as a structured error.
func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic recovered", "err", fmt.Sprint(rec), "path", r.URL.Path, "method", r.Method)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) broadcastSSE() {
	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	for ch := range s.sseSubs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *Server) subscribeSSE() chan struct{} {
	ch := make(chan struct{}, 1)
	s.sseMu.Lock()
	s.sseSubs[ch] = struct{}{}
	s.sseMu.Unlock()
	return ch
}

func (s *Server) unsubscribeSSE(ch chan struct{}) {
	s.sseMu.Lock()
	delete(s.sseSubs, ch)
	s.sseMu.Unlock()
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := s.subscribeSSE()
	defer s.unsubscribeSSE(ch)

	fmt.Fprintf(w, "event: update\ndata: {}\n\n")
	flusher.Flush()

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ch:
			fmt.Fprintf(w, "event: update\ndata: {}\n\n")
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "product": "Foreman"})
}

func (s *Server) workspace(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	state := s.store.Snapshot()
	state.Providers = s.store.ProvidersForAPI()
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) importTeam(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		TemplateID string `json:"templateId"`
		Name       string `json:"name"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	team, err := s.store.ImportTeam(req.TemplateID, req.Name)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, team)
}

func (s *Server) updateTeam(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var team Team
	if err := decodeJSON(w, r, &team); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	team.ID = r.PathValue("teamID")
	updated, err := s.store.UpdateTeam(team)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteTeam(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.DeleteTeam(r.PathValue("teamID")); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) saveAgent(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var agent Agent
	if err := decodeJSON(w, r, &agent); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := s.store.SaveAgent(agent)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var agent Agent
	if err := decodeJSON(w, r, &agent); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agent.ID = r.PathValue("agentID")
	updated, err := s.store.SaveAgent(agent)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.DeleteAgent(r.PathValue("agentID")); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// agentAvatar serves the cached (or freshly generated) SVG avatar for an agent.
// It is fetched directly by <img> tags, so it does not require the project
// query parameter; the server is bound to a single workspace.
func (s *Server) agentAvatar(w http.ResponseWriter, r *http.Request) {
	data, err := s.store.AvatarSVG(r.PathValue("agentID"))
	if err != nil {
		slog.Debug("agent avatar unavailable", "agent", r.PathValue("agentID"), "error", err)
	}
	if len(data) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	_, _ = w.Write(data)
}

// shuffleAgentAvatar re-rolls an agent's avatar seed/style so a user can pick a
// different random avatar.
func (s *Server) shuffleAgentAvatar(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agent, err := s.store.ShuffleAgentAvatar(r.PathValue("agentID"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) deleteRoom(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.DeleteRoom(r.PathValue("roomID")); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RuntimeEngineInstalled lets the CLI prewarm the SDK probe so the dashboard's
// engine status does not pay the slow first-import cost.
func (s *Server) RuntimeEngineInstalled() (bool, string) {
	return s.runtime.EngineInstalled()
}

// shouldLightReply decides whether a chat message should be answered locally
// instead of running the agent. It uses the heuristic, or the configured
// provider model when routeIntentsWithModel is enabled (falling back to the
// heuristic on any router error).
func (s *Server) shouldLightReply(content string) bool {
	settings := s.store.Snapshot().Settings
	if settings.DisableQuickReplies {
		return false
	}
	fallback := classifyChatMessage(content) == chatLight
	if !settings.RouteIntentsWithModel {
		return fallback
	}
	cfg, secret, ok := s.store.FirstEnabledProviderSecret()
	if !ok {
		return fallback
	}
	kind, err := classifyWithModel(context.Background(), cfg, secret, content)
	if err != nil {
		return fallback
	}
	return kind == chatLight
}

func (s *Server) engineStatus(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	diag := s.runtime.EngineDiagnostics()
	writeJSON(w, http.StatusOK, map[string]any{
		"installed":     diag.Installed,
		"detail":        diag.ProbeDetail,
		"pythonVersion": diag.PythonVersion,
		"pipAvailable":  diag.PipAvailable,
	})
}

func (s *Server) resetGlobal(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Agents bool `json:"agents"`
		Teams  bool `json:"teams"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.ResetGlobalCollections(req.Agents, req.Teams); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) engineInstall(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := s.runtime.InstallEngine(context.WithoutCancel(r.Context()))
	diag := s.runtime.EngineDiagnostics()
	reason := ""
	hint := ""
	if err != nil {
		reason, hint = classifyInstallError(err, out, diag.PythonVersion)
		if reason == "" {
			reason = "Installation failed — see the log below."
		}
	}
	if len(out) > 6000 {
		out = out[:6000] + "\n… (log dipotong)"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            err == nil,
		"installed":     diag.Installed,
		"error":         reason,
		"hint":          hint,
		"output":        out,
		"pythonVersion": diag.PythonVersion,
		"pipAvailable":  diag.PipAvailable,
	})
}

func (s *Server) createRoom(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Name    string   `json:"name"`
		Goal    string   `json:"goal"`
		TeamIDs []string `json:"teamIds"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	room, err := s.store.CreateRoom(req.Name, req.Goal, req.TeamIDs)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, room)
}

func (s *Server) appendRoomMessage(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	message, err := s.store.AppendRoomMessage(r.PathValue("roomID"), req.Role, req.Content)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// Short small talk is answered locally so a simple "halo" does not start an
	// expensive OpenHands run. Can be disabled in Settings (disableQuickReplies);
	// optional model-backed routing (routeIntentsWithModel) refines the decision.
	if req.Role == "user" && s.shouldLightReply(req.Content) {
		name := s.store.RoomMainAgentName(r.PathValue("roomID"))
		if name == "" {
			name = "Foreman"
		}
		_ = s.store.AppendAgentReply(r.PathValue("roomID"), name,
			"👋 Halo! Aku siap. Ceritakan tugasnya atau tanya apa pun — kalau mau aku langsung kerjakan sesuatu, sebutkan goal room-nya.")
		writeJSON(w, http.StatusCreated, map[string]any{
			"message": message,
			"run":     nil,
			"warning": "",
			"replied": "greeting",
		})
		return
	}
	// User chat messages trigger an agent run (routed to the mentioned agent or
	// the team's main agent). The run is started in the background so the HTTP
	// response (and the persisted user message) returns immediately; the
	// dashboard picks up the running state via its polling loop.
	var run *AgentRun
	var warning string
	if req.Role == "user" {
		started, runErr := s.runtime.StartChatRun(context.WithoutCancel(r.Context()), r.PathValue("roomID"), req.Content)
		if runErr != nil {
			warning = runErr.Error()
			_ = s.store.AppendRoomNotice(r.PathValue("roomID"), "", "chat.error",
				fmt.Sprintf("⚠️ Message could not be run: %v", runErr))
		} else {
			run = &started
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"message": message,
		"run":     run,
		"warning": warning,
	})
}

func (s *Server) updateRoomTeams(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		TeamIDs []string `json:"teamIds"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	room, err := s.store.UpdateRoomTeams(r.PathValue("roomID"), req.TeamIDs)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, room)
}

func (s *Server) answerRoomQuestion(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Answer string `json:"answer"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	roomID := r.PathValue("roomID")
	question, err := s.store.AnswerRoomQuestion(roomID, r.PathValue("questionID"), req.Answer)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// Answering is a new user message, so it must wake the agent that asked.
	// Without this the reply only landed in the transcript: the human picked a
	// confirmation, the dashboard refreshed, and nothing ever happened — no
	// answer, no history, no continuation. The asker is addressed by mention
	// (or the room's main agent when the question did not record one), which is
	// exactly how a typed chat message is routed.
	content := strings.TrimSpace(req.Answer)
	if question.AgentName != "" {
		content = "@" + question.AgentName + " " + content
	}
	var run *AgentRun
	var warning string
	started, runErr := s.runtime.StartChatRun(context.WithoutCancel(r.Context()), roomID, content)
	if runErr != nil {
		// The answer itself is already stored; surface why the agent did not
		// resume instead of failing the whole request and losing the reply.
		warning = runErr.Error()
		_ = s.store.AppendRoomNotice(roomID, question.AgentName, "chat.error",
			fmt.Sprintf("⚠️ Answer could not be run: %v", runErr))
		slog.Warn("question answer did not start a run", "room", roomID, "err", runErr)
	} else {
		run = &started
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"question": question,
		"run":      run,
		"warning":  warning,
	})
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var task Task
	if err := decodeJSON(w, r, &task); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	task.RoomID = r.PathValue("roomID")
	created, err := s.store.CreateTask(task)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateTaskStatus(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Status   string `json:"status"`
		Approved bool   `json:"approved"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	task, err := s.store.UpdateTaskStatus(r.PathValue("taskID"), req.Status, req.Approved)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) updateTaskDetails(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var patch TaskDetailsPatch
	if err := decodeJSON(w, r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	task, err := s.store.UpdateTaskDetails(r.PathValue("taskID"), patch)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) startTask(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	run, err := s.runtime.StartTask(context.WithoutCancel(r.Context()), r.PathValue("taskID"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.runtime.CancelRun(r.PathValue("runID")); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pauseRun(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.runtime.PauseRun(r.PathValue("runID")); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) saveProvider(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Config ProviderConfig `json:"config"`
		APIKey string         `json:"apiKey"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	config, err := s.store.SaveProvider(req.Config, req.APIKey)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, config)
}

func (s *Server) testProvider(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Config ProviderConfig `json:"config"`
		APIKey string         `json:"apiKey"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateProviderInput(req.Config.ID, req.Config.Name, req.Config.Protocol, req.Config.BaseURL, req.Config.DefaultModel); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	secret := s.resolveProviderSecret(req.Config.ID, req.APIKey)
	if err := testProvider(r.Context(), req.Config, secret); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "connected", "model": req.Config.DefaultModel})
}

func (s *Server) discoverModels(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Config ProviderConfig `json:"config"`
		APIKey string         `json:"apiKey"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Config.Protocol != "openai-compatible" && req.Config.Protocol != "anthropic" {
		writeError(w, http.StatusUnprocessableEntity, "protocol must be openai-compatible or anthropic")
		return
	}
	secret := s.resolveProviderSecret(req.Config.ID, req.APIKey)
	models, err := listProviderModels(r.Context(), req.Config, secret)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (s *Server) deleteProvider(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.DeleteProvider(r.PathValue("providerID")); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveProviderSecret prefers a freshly typed key, then falls back to the
// credential already stored for an existing provider.
func (s *Server) resolveProviderSecret(providerID, typed string) string {
	if strings.TrimSpace(typed) != "" {
		return typed
	}
	if providerID == "" {
		return ""
	}
	secret, err := s.store.loadProviderCredential(s.root, providerID)
	if err != nil {
		return ""
	}
	return secret
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var settings Settings
	if err := decodeJSON(w, r, &settings); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := s.store.UpdateSettings(settings)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) saveSkill(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var skill Skill
	if err := decodeJSON(w, r, &skill); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := s.store.SaveSkill(skill)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

func (s *Server) deleteSkill(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.DeleteSkill(r.PathValue("skillID")); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) verifyProject(raw string) error {
	if raw == "" {
		return errors.New("project path is required")
	}
	abs, err := filepath.Abs(raw)
	if err != nil || abs != s.root {
		return errors.New("project path does not match the active workspace")
	}
	return nil
}

func (s *Server) localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostName := r.Host
		if parsedHost, _, err := net.SplitHostPort(r.Host); err == nil {
			hostName = parsedHost
		}
		if !isLoopbackHost(hostName) {
			http.Error(w, "Foreman only accepts local hostnames", http.StatusForbidden)
			return
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			http.Error(w, "Foreman only accepts local connections", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method == http.MethodPost {
			origin := r.Header.Get("Origin")
			if origin != "" {
				u, parseErr := url.Parse(origin)
				hostname := ""
				if parseErr == nil {
					hostname = u.Hostname()
				}
				if parseErr != nil || hostname == "" || !strings.EqualFold(u.Host, r.Host) || !isLoopbackHost(hostname) {
					http.Error(w, "cross-origin requests are not allowed", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	if err := decoder.Decode(new(any)); err == nil {
		return errors.New("request must contain a single JSON value")
	} else if !errors.Is(err, io.EOF) {
		return errors.New("request must contain a single JSON value")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func NewHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

func (s *Server) getRoomPlan(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomID")
	plan, err := s.store.GetPlan(roomID)
	if err != nil {
		writeError(w, http.StatusNotFound, "plan not found")
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) saveRoomPlan(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomID")
	var plan PlanRecord
	if err := decodeJSON(w, r, &plan); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	plan.RoomID = roomID
	if err := s.store.SavePlan(plan); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) listRoomArtifacts(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomID")
	artifacts, err := s.store.ListArtifacts(roomID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, artifacts)
}

func (s *Server) saveRoomArtifact(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomID")
	var art ArtifactRecord
	if err := decodeJSON(w, r, &art); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	art.RoomID = roomID
	if err := s.store.SaveArtifact(art); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, art)
}

func (s *Server) listRoomApprovals(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomID")
	approvals, err := s.store.ListApprovals(roomID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, approvals)
}

func (s *Server) listAllApprovals(w http.ResponseWriter, r *http.Request) {
	approvals, err := s.store.ListApprovals("")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, approvals)
}

func (s *Server) resolveApproval(w http.ResponseWriter, r *http.Request) {
	approvalID := r.PathValue("approvalID")
	var req struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.ResolveApproval(approvalID, req.Status); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.Status == "approved" {
		s.applyApprovedApproval(approvalID)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": req.Status})
}

// applyApprovedApproval performs side effects for approved approvals, such as
// actually spawning a teammate when a request_spawn approval is approved.
func (s *Server) applyApprovedApproval(approvalID string) {
	app, ok := s.store.GetApproval(approvalID)
	if !ok || app.Type != "request_spawn" {
		return
	}
	var pl struct {
		Action    string `json:"action"`
		AgentName string `json:"agent_name"`
		Title     string `json:"title"`
		Desc      string `json:"desc"`
	}
	if err := json.Unmarshal([]byte(app.PayloadJSON), &pl); err != nil || pl.Action != "request_spawn" || pl.AgentName == "" {
		return
	}
	teamID := s.store.TeamIDForRoomAgent(app.RoomID, pl.AgentName)
	if teamID == "" {
		return
	}
	title := pl.Title
	if title == "" {
		title = "Delegated task for " + pl.AgentName
	}
	task := Task{
		RoomID: app.RoomID, AgentName: pl.AgentName, TeamID: teamID,
		Title: title, Description: pl.Desc, Status: "todo",
		Priority: "normal", Effort: "medium", Dependencies: []string{},
	}
	created, err := s.store.CreateTask(task)
	if err != nil {
		return
	}
	s.store.EnqueueAutoStart(created.ID)
	_ = s.store.AppendRoomNotice(app.RoomID, pl.AgentName, "agent.spawned",
		fmt.Sprintf("🆘 spawn request approved — @%s spawned: %s", pl.AgentName, title))
}

func (s *Server) listRoomPanels(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomID")
	includePending := r.URL.Query().Get("all") == "true"
	panels, err := s.store.ListPanels(roomID, includePending)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, panels)
}

func (s *Server) registerRoomPanel(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomID")
	var p PanelRecord
	if err := decodeJSON(w, r, &p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p.RoomID = roomID
	id, err := s.store.RegisterPanel(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p.ID = id
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) getRoomMemory(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomID")
	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	val, err := s.store.DB().GetMemory(roomID, key)
	if err != nil {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": key, "value": val})
}

func (s *Server) setRoomMemory(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomID")
	var req struct {
		Key       string `json:"key"`
		Value     string `json:"value"`
		UpdatedBy string `json:"updatedBy"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.DB().SetMemory(roomID, req.Key, req.Value, req.UpdatedBy); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

/* ---------- MCP Servers (global) ---------- */

func (s *Server) listMCP(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.store.ListMCPServers())
}

func (s *Server) saveMCP(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var srv MCPServer
	if err := decodeJSON(w, r, &srv); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := s.store.SaveMCPServer(srv)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

func (s *Server) updateMCP(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var srv MCPServer
	if err := decodeJSON(w, r, &srv); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	srv.ID = r.PathValue("serverID")
	saved, err := s.store.SaveMCPServer(srv)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) deleteMCP(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.DeleteMCPServer(r.PathValue("serverID")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) testMCP(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	srv, ok := s.store.GetMCPServer(r.PathValue("serverID"))
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown mcp server")
		return
	}
	n, err := testMCPConnection(srv)
	if err != nil {
		writeError(w, http.StatusBadRequest, "MCP connection failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tools": n})
}

func (s *Server) setRoomMCP(w http.ResponseWriter, r *http.Request) {
	if err := s.verifyProject(r.URL.Query().Get("project")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		ServerIDs []string `json:"serverIds"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	room, err := s.store.SetRoomMCP(r.PathValue("roomID"), body.ServerIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, room)
}
