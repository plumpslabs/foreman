package foreman

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

//go:embed all:web/dist
var webFiles embed.FS

type Server struct {
	store   *Store
	root    string
	runtime *Runtime
}

func NewServer(st *Store, projectPath string) (*Server, error) {
	root, err := filepath.Abs(projectPath)
	if err != nil {
		return nil, err
	}
	return &Server{store: st, root: root, runtime: NewRuntime(st)}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/workspace", s.workspace)
	mux.HandleFunc("POST /api/teams/import", s.importTeam)
	mux.HandleFunc("PUT /api/teams/{teamID}", s.updateTeam)
	mux.HandleFunc("POST /api/agents", s.saveAgent)
	mux.HandleFunc("PUT /api/agents/{agentID}", s.updateAgent)
	mux.HandleFunc("DELETE /api/agents/{agentID}", s.deleteAgent)
	mux.HandleFunc("POST /api/rooms", s.createRoom)
	mux.HandleFunc("DELETE /api/rooms/{roomID}", s.deleteRoom)
	mux.HandleFunc("POST /api/rooms/{roomID}/messages", s.appendRoomMessage)
	mux.HandleFunc("PUT /api/rooms/{roomID}/teams", s.updateRoomTeams)
	mux.HandleFunc("PUT /api/rooms/{roomID}/questions/{questionID}", s.answerRoomQuestion)
	mux.HandleFunc("POST /api/rooms/{roomID}/tasks", s.createTask)
	mux.HandleFunc("PUT /api/tasks/{taskID}/status", s.updateTaskStatus)
	mux.HandleFunc("POST /api/tasks/{taskID}/run", s.startTask)
	mux.HandleFunc("POST /api/runs/{runID}/cancel", s.cancelRun)
	mux.HandleFunc("POST /api/runs/{runID}/pause", s.pauseRun)
	mux.HandleFunc("POST /api/providers", s.saveProvider)
	mux.HandleFunc("POST /api/providers/test", s.testProvider)
	mux.HandleFunc("POST /api/providers/discover", s.discoverModels)
	mux.HandleFunc("DELETE /api/providers/{providerID}", s.deleteProvider)
	mux.HandleFunc("PUT /api/settings", s.updateSettings)
	mux.HandleFunc("POST /api/skills", s.saveSkill)
	mux.HandleFunc("DELETE /api/skills/{skillID}", s.deleteSkill)
	static, err := fs.Sub(webFiles, "web/dist")
	if err != nil {
		panic("foreman: embedded dashboard is missing; run `make web` before building: " + err.Error())
	}
	mux.Handle("GET /", http.FileServer(http.FS(static)))
	return s.localOnly(mux)
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
	// User chat messages trigger an agent run (routed to the mentioned agent or
	// the team's main agent). If the room is not runnable, the message is still
	// stored and a human-readable warning is returned to the sender.
	var run *AgentRun
	var warning string
	if req.Role == "user" {
		started, runErr := s.runtime.StartChatRun(context.WithoutCancel(r.Context()), r.PathValue("roomID"), req.Content)
		if runErr != nil {
			warning = runErr.Error()
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
	question, err := s.store.AnswerRoomQuestion(r.PathValue("roomID"), r.PathValue("questionID"), req.Answer)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, question)
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
