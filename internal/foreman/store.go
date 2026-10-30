package foreman

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Store struct {
	mu         sync.RWMutex
	path       string
	globalPath string
	avatarDir  string
	state      WorkspaceState
	db         *DB
	onChange   func()
	// onAutoStart is invoked (async-friendly) whenever a task is enqueued for
	// auto-start so the runtime can drain the queue immediately instead of
	// waiting for another run to finish.
	onAutoStart func()
	// autoStart is an in-memory FIFO of task IDs whose agents should wake up
	// to work as soon as run capacity is available (spawned/delegated tasks).
	autoStart []string
}

// GlobalProfile holds user-level collections shared across all projects. They
// live in ~/.foreman/global.json (or $FOREMAN_GLOBAL_PATH): templates, the
// agent library, teams, skills, providers (with credentials beside it), and
// workspace settings. Only rooms (and their tasks/runs) are per-project.
type GlobalProfile struct {
	Templates  []TeamTemplate   `json:"templates"`
	Agents     []Agent          `json:"agents"`
	Teams      []Team           `json:"teams"`
	Skills     []Skill          `json:"skills"`
	Providers  []ProviderConfig `json:"providers"`
	MCPServers []MCPServer      `json:"mcpServers,omitempty"`
	Settings   Settings         `json:"settings"`
}

func OpenStore(projectPath string) (*Store, error) {
	abs, err := filepath.Abs(projectPath)
	if err != nil {
		return nil, fmt.Errorf("resolve project path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("inspect project path: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("project path must be a directory")
	}

	path := filepath.Join(abs, ".foreman", "workspace.json")
	s := &Store{path: path}
	s.globalPath = s.resolveGlobalPath(abs)
	s.avatarDir = avatarDirFor(s.globalPath)
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &s.state); err != nil {
			return nil, fmt.Errorf("read Foreman workspace state: %w", err)
		}
		if s.state.ProjectPath != abs {
			return nil, errors.New("Foreman workspace belongs to a different project path")
		}
		migrated, err := s.migrate()
		if err != nil {
			return nil, err
		}
		if migrated {
			// Project-only write; the global profile is handled by loadGlobal.
			if err := writeJSONAtomic(s.path, s.state); err != nil {
				return nil, err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("open Foreman workspace: %w", err)
	} else {
		s.state = WorkspaceState{
			SchemaVersion: SchemaVersion,
			ProjectPath:   abs,
			Rooms:         []Room{},
			Teams:         []Team{},
			Agents:        seedAgents(),
			Templates:     DefaultTemplates(),
			Providers:     []ProviderConfig{},
			Tasks:         []Task{},
			Runs:          []AgentRun{},
			Skills:        seedSkills(),
			Settings:      normalizeSettings(Settings{}),
		}
		// Write the project file only; the shared global profile is overlaid
		// (or seeded) by loadGlobal below. Saving global here would clobber the
		// user's existing global collections with empty defaults.
		if err := writeJSONAtomic(s.path, s.state); err != nil {
			return nil, err
		}
	}
	// Overlay user-level collections from the global profile so agents, teams,
	// skills, and providers are shared across projects.
	if err := s.loadGlobal(); err != nil {
		return nil, err
	}

	// Backfill avatar seeds/styles so agents created before avatars existed get
	// one too. Persisted with the state below.
	s.ensureAgentAvatars()

	// Initialize SQLite database for autonomous orchestration persistence and crash recovery.
	if db, err := OpenDB(abs); err == nil {
		s.db = db
		requeued, blocked, _ := s.db.RecoverInterruptedTasks()
		if len(requeued) > 0 || len(blocked) > 0 {
			for i := range s.state.Tasks {
				for _, id := range requeued {
					if s.state.Tasks[i].ID == id {
						s.state.Tasks[i].Status = "todo"
						s.state.Tasks[i].RetryCount++
					}
				}
				for _, id := range blocked {
					if s.state.Tasks[i].ID == id {
						s.state.Tasks[i].Status = "blocked"
					}
				}
			}
		}
	}

	// Auto-derive room goal for any room where goal is unset or placeholder
	for i := range s.state.Rooms {
		g := strings.TrimSpace(s.state.Rooms[i].Goal)
		if g == "" || strings.EqualFold(g, "no goal provided") || strings.EqualFold(g, "no goal provided.") {
			for _, m := range s.state.Rooms[i].Messages {
				if m.Role == "user" && strings.TrimSpace(m.Content) != "" {
					s.state.Rooms[i].Goal = DeriveRoomGoal(m.Content)
					break
				}
			}
		}
	}

	// Persist the overlaid state (project + global) so it is durable.
	if err := s.save(); err != nil {
		return nil, err
	}
	return s, nil
}

// resolveGlobalPath returns the path of the user-level global profile.
func (s *Store) resolveGlobalPath(absProject string) string {
	if p := strings.TrimSpace(os.Getenv("FOREMAN_GLOBAL_PATH")); p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".foreman", "global.json")
	}
	return filepath.Join(absProject, ".foreman", "global.json")
}

func (s *Store) profile() GlobalProfile {
	return GlobalProfile{
		Templates:  s.state.Templates,
		Agents:     s.state.Agents,
		Teams:      s.state.Teams,
		Skills:     s.state.Skills,
		Providers:  s.state.Providers,
		MCPServers: s.state.MCPServers,
		Settings:   s.state.Settings,
	}
}

func (s *Store) applyProfile(p GlobalProfile) {
	if len(p.Templates) > 0 || len(p.Agents) > 0 || len(p.Teams) > 0 || len(p.Skills) > 0 || len(p.Providers) > 0 || len(p.MCPServers) > 0 {
		s.state.Templates = p.Templates
		s.state.Agents = p.Agents
		s.state.Teams = p.Teams
		s.state.Skills = p.Skills
		s.state.Providers = p.Providers
		s.state.MCPServers = p.MCPServers
	}
	s.syncAgentsFromTeamsLocked()
	s.syncSkillsFromDefaultLocked()
	if p.Settings.Theme != "" || p.Settings.EffortMode != "" || p.Settings.MaxConcurrentRuns != 0 {
		s.state.Settings = normalizeSettings(p.Settings)
	}
}

// loadGlobal reads the user-level profile, or seeds it from the current
// workspace on first run so existing collections become global automatically.
func (s *Store) loadGlobal() error {
	data, err := os.ReadFile(s.globalPath)
	if err == nil {
		var p GlobalProfile
		if err := json.Unmarshal(data, &p); err != nil {
			return fmt.Errorf("read Foreman global profile: %w", err)
		}
		s.applyProfile(p)
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("open Foreman global profile: %w", err)
	}
	return s.saveGlobal()
}

func (s *Store) saveGlobal() error {
	return writeJSONAtomic(s.globalPath, s.profile())
}

// migrate upgrades an on-disk workspace to the current schema. It returns true
// when the state changed and must be written back. Schema 2 → 3 adds a
// standalone agent library and versioned skill bundles.
func (s *Store) migrate() (bool, error) {
	if s.state.SchemaVersion > SchemaVersion {
		return false, fmt.Errorf("workspace schema %d is newer than supported version %d", s.state.SchemaVersion, SchemaVersion)
	}
	if s.state.SchemaVersion < 2 {
		return false, fmt.Errorf("unsupported workspace schema %d (expected %d)", s.state.SchemaVersion, SchemaVersion)
	}
	migrated := false
	if s.state.SchemaVersion == 2 {
		s.state.Agents = agentsFromTeams(s.state.Teams)
		s.state.SchemaVersion = 3
		migrated = true
	}

	if s.state.Rooms == nil {
		s.state.Rooms = []Room{}
	}
	for i := range s.state.Rooms {
		if s.state.Rooms[i].Messages == nil {
			s.state.Rooms[i].Messages = []ChatMessage{}
		}
		if s.state.Rooms[i].Questions == nil {
			s.state.Rooms[i].Questions = []AgentQuestion{}
		}
	}
	if s.state.Teams == nil {
		s.state.Teams = []Team{}
	}
	for i := range s.state.Teams {
		for j := range s.state.Teams[i].Agents {
			if s.state.Teams[i].Agents[j].ID == "" {
				s.state.Teams[i].Agents[j].ID = uuid.NewString()
			}
		}
	}
	if s.state.Agents == nil || (migrated && len(s.state.Agents) == 0) {
		s.state.Agents = seedAgents()
	}
	for i := range s.state.Agents {
		if s.state.Agents[i].ID == "" {
			s.state.Agents[i].ID = uuid.NewString()
		}
	}
	if s.state.Providers == nil {
		s.state.Providers = []ProviderConfig{}
	}
	if s.state.Tasks == nil {
		s.state.Tasks = []Task{}
	}
	if s.state.Runs == nil {
		s.state.Runs = []AgentRun{}
	}
	if s.state.Skills == nil {
		s.state.Skills = []Skill{}
	}
	if migrated && len(s.state.Skills) == 0 {
		s.state.Skills = seedSkills()
	}
	for i := range s.state.Skills {
		skill := &s.state.Skills[i]
		if skill.Mode != SkillModeActive && skill.Mode != SkillModePassive {
			if skill.Enabled {
				skill.Mode = SkillModeActive
			} else {
				skill.Mode = SkillModePassive
			}
		}
		skill.Enabled = skill.Mode == SkillModeActive
		if skill.Files == nil {
			skill.Files = []SkillFile{}
		}
		if len(skill.Files) == 0 && skill.Instructions != "" {
			skill.Files = []SkillFile{{Path: "SKILL.md", Content: skill.Instructions}}
		}
	}
	if s.state.Templates == nil {
		s.state.Templates = DefaultTemplates()
	}
	s.syncAgentsFromTeamsLocked()
	s.syncSkillsFromDefaultLocked()
	s.state.Settings = normalizeSettings(s.state.Settings)
	// Normalize task priorities to the P0..P3 scale (legacy names like
	// urgent/high/normal/low are migrated once).
	for i := range s.state.Tasks {
		if canon, ok := NormalizePriority(s.state.Tasks[i].Priority); ok && canon != s.state.Tasks[i].Priority {
			s.state.Tasks[i].Priority = canon
			migrated = true
		}
	}
	// Reconcile orphans left by a previous process: any task still
	// "in_progress" cannot have an active run (runs die with the process),
	// so it must not block new prompts or run starts. Reset it to "todo".
	// Stale "running" runs are marked cancelled for the same reason.
	for i := range s.state.Tasks {
		if s.state.Tasks[i].Status == "in_progress" || s.state.Tasks[i].Status == "paused" && !s.state.Tasks[i].Hidden {
			s.state.Tasks[i].Status = "todo"
			migrated = true
		}
	}
	for i := range s.state.Runs {
		if s.state.Runs[i].Status == "running" {
			s.state.Runs[i].Status = "cancelled"
			s.state.Runs[i].Error = "interrupted by restart"
			migrated = true
		}
	}
	return migrated, nil
}

func (s *Store) syncSkillsFromDefaultLocked() {
	existing := map[string]int{} // key -> index in s.state.Skills
	for i, sk := range s.state.Skills {
		key := strings.ToLower(strings.TrimSpace(sk.Name))
		if key != "" {
			existing[key] = i
		}
	}
	for _, def := range DefaultSkills() {
		key := strings.ToLower(strings.TrimSpace(def.Name))
		if key == "" {
			continue
		}
		if idx, ok := existing[key]; ok {
			// Upgrade existing skill if the built-in version has richer content.
			cur := &s.state.Skills[idx]
			if len(def.Instructions) > len(cur.Instructions) {
				cur.Instructions = def.Instructions
				cur.Description = def.Description
				cur.Files = def.Files
			}
		} else {
			if def.ID == "" {
				def.ID = uuid.NewString()
			}
			existing[key] = len(s.state.Skills)
			s.state.Skills = append(s.state.Skills, def)
		}
	}
}

func (s *Store) syncAgentsFromTeamsLocked() {
	seen := map[string]bool{}
	for _, a := range s.state.Agents {
		key := strings.ToLower(strings.TrimSpace(a.Name))
		if key != "" {
			seen[key] = true
		}
	}
	for _, team := range s.state.Teams {
		for _, agent := range team.Agents {
			key := strings.ToLower(strings.TrimSpace(agent.Name))
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			copyAgent := agent
			if copyAgent.ID == "" {
				copyAgent.ID = uuid.NewString()
			}
			_ = s.normalizeSkillRefs(&copyAgent)
			s.state.Agents = append(s.state.Agents, copyAgent)
		}
	}
}

func seedAgents() []Agent {
	return []Agent{}
}

func seedSkills() []Skill {
	return []Skill{}
}

// normalizeSkillRefs resolves skill references that may be either skill IDs or
// legacy capability names and stores them as IDs.
func (s *Store) normalizeSkillRefs(agent *Agent) error {
	out := make([]string, 0, len(agent.Skills))
	seen := map[string]bool{}
	for _, ref := range agent.Skills {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		id := ""
		for _, skill := range s.state.Skills {
			if skill.ID == ref {
				id = skill.ID
				break
			}
		}
		if id == "" {
			for _, skill := range s.state.Skills {
				if strings.EqualFold(skill.Name, ref) {
					id = skill.ID
					break
				}
			}
		}
		if id == "" {
			for _, def := range DefaultSkills() {
				if strings.EqualFold(def.Name, ref) {
					newSkill := def
					if newSkill.ID == "" {
						newSkill.ID = uuid.NewString()
					}
					s.state.Skills = append(s.state.Skills, newSkill)
					id = newSkill.ID
					break
				}
			}
		}
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	agent.Skills = out
	return nil
}

func agentsFromTeams(teams []Team) []Agent {
	seen := map[string]bool{}
	var agents []Agent
	for _, team := range teams {
		for _, agent := range team.Agents {
			if agent.Name == "" || seen[agent.Name] {
				continue
			}
			seen[agent.Name] = true
			if agent.ID == "" {
				agent.ID = uuid.NewString()
			}
			agents = append(agents, agent)
		}
	}
	return agents
}

// TeamIDForRoomAgent returns the ID of a team invited to the room that
// contains an agent with the given name, or "" when not found.
func (s *Store) TeamIDForRoomAgent(roomID, agentName string) string {
	if agentName == "" {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.teamIDForRoomAgentLocked(roomID, agentName)
}

func (s *Store) teamIDForRoomAgentLocked(roomID, agentName string) string {
	invited := map[string]bool{}
	for _, room := range s.state.Rooms {
		if room.ID != roomID {
			continue
		}
		for _, id := range room.TeamIDs {
			invited[id] = true
		}
	}
	for _, team := range s.state.Teams {
		if !invited[team.ID] {
			continue
		}
		for _, a := range team.Agents {
			if a.Name == agentName {
				return team.ID
			}
		}
	}
	return ""
}

// RoomAgentNames lists the names of all agents belonging to teams invited to
// the room. Used for friendly delegation errors.
func (s *Store) RoomAgentNames(roomID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.roomAgentNamesLocked(roomID)
}

func (s *Store) roomAgentNamesLocked(roomID string) []string {
	invited := map[string]bool{}
	for _, room := range s.state.Rooms {
		if room.ID != roomID {
			continue
		}
		for _, id := range room.TeamIDs {
			invited[id] = true
		}
	}
	var names []string
	for _, team := range s.state.Teams {
		if !invited[team.ID] {
			continue
		}
		for _, a := range team.Agents {
			names = append(names, a.Name)
		}
	}
	return names
}

// ReviewerForRoom returns a teammate who should review work in the room —
// preferring an agent whose role/description mentions review — while skipping
// the given agent name. Returns "" when no candidate exists.
func (s *Store) ReviewerForRoom(roomID, exclude string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	invited := map[string]bool{}
	for _, room := range s.state.Rooms {
		if room.ID != roomID {
			continue
		}
		for _, id := range room.TeamIDs {
			invited[id] = true
		}
	}
	type cand struct{ name, role, desc string }
	var fallback []cand
	for _, team := range s.state.Teams {
		if !invited[team.ID] {
			continue
		}
		for _, a := range team.Agents {
			if a.Name == "" || a.Name == exclude {
				continue
			}
			low := strings.ToLower(a.Role + " " + a.Description)
			c := cand{a.Name, a.Role, a.Description}
			if strings.Contains(low, "review") || strings.Contains(low, "verif") || strings.Contains(low, "quality") {
				return c.name
			}
			fallback = append(fallback, c)
		}
	}
	if len(fallback) > 0 {
		return fallback[0].name
	}
	return ""
}

// EnqueueAutoStart marks a task for automatic start once run capacity frees,
// and immediately nudges the runtime to try starting it now.
func (s *Store) EnqueueAutoStart(taskID string) {
	s.enqueueAutoStart(taskID, true)
}

// requeueAutoStart re-queues a task without re-triggering the auto-start hook.
// Used by the drain loop when capacity is momentarily full, so it does not spin.
func (s *Store) requeueAutoStart(taskID string) {
	s.enqueueAutoStart(taskID, false)
}

func (s *Store) enqueueAutoStart(taskID string, trigger bool) {
	if taskID == "" {
		return
	}
	s.mu.Lock()
	for _, id := range s.autoStart {
		if id == taskID {
			s.mu.Unlock()
			return
		}
	}
	s.autoStart = append(s.autoStart, taskID)
	fn := s.onAutoStart
	s.mu.Unlock()
	if trigger && fn != nil {
		go fn()
	}
}

// SetOnAutoStart registers a callback fired whenever a task is enqueued for
// auto-start. The runtime uses it to drain the queue without waiting for a run
// to finish.
func (s *Store) SetOnAutoStart(fn func()) {
	s.mu.Lock()
	s.onAutoStart = fn
	s.mu.Unlock()
}

// PopAutoStart returns and removes the oldest pending auto-start task id.
func (s *Store) PopAutoStart() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.autoStart) == 0 {
		return ""
	}
	id := s.autoStart[0]
	s.autoStart = s.autoStart[1:]
	return id
}

func (s *Store) Snapshot() WorkspaceState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, _ := json.Marshal(s.state)
	var copy WorkspaceState
	_ = json.Unmarshal(data, &copy)

	if s.db != nil {
		if plans, err := s.db.ListPlans(); err == nil {
			copy.Plans = plans
		}
		if panels, err := s.db.ListPanels("", true); err == nil {
			copy.Panels = panels
		}
		if approvals, err := s.db.ListApprovals(""); err == nil {
			copy.Approvals = approvals
		}
		if artifacts, err := s.db.ListArtifacts(""); err == nil {
			copy.Artifacts = artifacts
		}
	}
	return copy
}

// RunStatus returns the current status of a run, or "" if the run is unknown.
func (s *Store) RunStatus(runID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, run := range s.state.Runs {
		if run.ID == runID {
			return run.Status
		}
	}
	return ""
}

// HasRunningRunForTask reports whether a run is currently in flight for a task.
func (s *Store) HasRunningRunForTask(taskID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, run := range s.state.Runs {
		if run.TaskID == taskID && run.Status == "running" {
			return true
		}
	}
	return false
}

// FirstEnabledProviderSecret returns the first enabled provider that has a saved
// credential, with its secret. Used by optional model-backed routing.
func (s *Store) FirstEnabledProviderSecret() (ProviderConfig, string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.state.Providers {
		if !p.Enabled {
			continue
		}
		if secret, err := s.loadProviderCredential(s.state.ProjectPath, p.ID); err == nil && secret != "" {
			return p, secret, true
		}
	}
	return ProviderConfig{}, "", false
}

// RoomRecentContext renders the most recent room chat and answered questions as
// compact context so a chat run remembers what was said even across messages.
func (s *Store) RoomRecentContext(roomID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var room *Room
	for i := range s.state.Rooms {
		if s.state.Rooms[i].ID == roomID {
			room = &s.state.Rooms[i]
			break
		}
	}
	if room == nil {
		return ""
	}
	var b strings.Builder
	taken := 0
	for i := len(room.Messages) - 1; i >= 0 && taken < 10; i-- {
		m := room.Messages[i]
		// System/activity notices (delegations, retries, "task finished") are
		// progress chips, not conversation. Including them used to attribute
		// every ✅/🚀 line to "User" and burn the 10-message window in busy
		// rooms, pushing the actual thread out of the model's context.
		if m.Role == "system" || m.Role == "activity" {
			continue
		}
		author := "User"
		if m.Role == "agent" {
			author = m.AgentName
			if author == "" {
				author = "Agent"
			}
		}
		content := m.Content
		if r := []rune(content); len(r) > 500 {
			content = string(r[:500]) + "…"
		}
		if content == "" {
			continue
		}
		fmt.Fprintf(&b, "**%s:** %s\n\n", author, content)
		taken++
	}
	for _, q := range room.Questions {
		if q.Answer == "" {
			continue
		}
		fmt.Fprintf(&b, "**Question:** %s\n**Answer:** %s\n\n", q.Prompt, q.Answer)
	}
	out := strings.TrimSpace(b.String())
	if r := []rune(out); len(r) > 6000 {
		out = string(r[:6000]) + "\n…"
	}
	return out
}

func (s *Store) CreateRoom(name, goal string, teamIDs []string) (Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = strings.TrimSpace(name)
	if name == "" {
		return Room{}, errors.New("room name is required")
	}
	valid := make(map[string]bool, len(s.state.Teams))
	for _, team := range s.state.Teams {
		valid[team.ID] = true
	}
	seen := make(map[string]bool, len(teamIDs))
	for _, id := range teamIDs {
		if !valid[id] {
			return Room{}, fmt.Errorf("unknown team id %q", id)
		}
		if !seen[id] {
			seen[id] = true
		}
	}
	ids := make([]string, 0, len(seen))
	for _, id := range teamIDs {
		if seen[id] {
			ids = append(ids, id)
			delete(seen, id)
		}
	}
	r := Room{ID: uuid.NewString(), Name: name, Goal: strings.TrimSpace(goal), TeamIDs: ids, Status: "planning", Messages: []ChatMessage{}, Questions: []AgentQuestion{}, CreatedAt: time.Now().UTC()}
	previous := s.state.Rooms
	s.state.Rooms = append([]Room{r}, previous...)
	if err := s.save(); err != nil {
		s.state.Rooms = previous
		return Room{}, err
	}
	return r, nil
}

// DeleteRoom removes a room together with its tasks and runs. Runs for tasks
// inside the room are removed too so the room's history disappears atomically.
func (s *Store) DeleteRoom(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previousRooms := s.state.Rooms
	previousTasks := s.state.Tasks
	previousRuns := s.state.Runs

	filtered := make([]Room, 0, len(previousRooms))
	found := false
	for _, room := range previousRooms {
		if room.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, room)
	}
	if !found {
		return fmt.Errorf("unknown room id %q", id)
	}

	tasks := make([]Task, 0, len(previousTasks))
	for _, task := range previousTasks {
		if task.RoomID != id {
			tasks = append(tasks, task)
		}
	}
	runs := make([]AgentRun, 0, len(previousRuns))
	for _, run := range previousRuns {
		if run.RoomID != id {
			runs = append(runs, run)
		}
	}

	s.state.Rooms = filtered
	s.state.Tasks = tasks
	s.state.Runs = runs
	if err := s.save(); err != nil {
		s.state.Rooms = previousRooms
		s.state.Tasks = previousTasks
		s.state.Runs = previousRuns
		return err
	}
	return nil
}

func (s *Store) AppendRoomMessage(roomID, role, content string) (ChatMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	role = strings.TrimSpace(role)
	content = strings.TrimSpace(content)
	if role != "user" {
		return ChatMessage{}, errors.New("only user chat messages are accepted until an agent runtime is connected")
	}
	if content == "" {
		return ChatMessage{}, errors.New("message cannot be empty")
	}
	if len(content) > 16000 {
		return ChatMessage{}, errors.New("message exceeds the 16000 character limit")
	}
	for i := range s.state.Rooms {
		if s.state.Rooms[i].ID != roomID {
			continue
		}
		if role == "user" {
			g := strings.TrimSpace(s.state.Rooms[i].Goal)
			if g == "" || strings.EqualFold(g, "no goal provided") || strings.EqualFold(g, "no goal provided.") {
				s.state.Rooms[i].Goal = DeriveRoomGoal(content)
			}
		}
		message := NewChatMessage(role, content)
		previous := s.state.Rooms[i].Messages
		s.state.Rooms[i].Messages = append(previous, message)
		if s.db != nil {
			_ = s.db.RecordActivity(roomID, "", "chat", content, map[string]any{"role": role})
		}
		if err := s.save(); err != nil {
			s.state.Rooms[i].Messages = previous
			return ChatMessage{}, err
		}
		return message, nil
	}
	return ChatMessage{}, fmt.Errorf("unknown room id %q", roomID)
}

// UpdateRoomTeams replaces a room's invited teams. Unknown team IDs are
// rejected and duplicates are collapsed.
func (s *Store) UpdateRoomTeams(roomID string, teamIDs []string) (Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	valid := make(map[string]bool, len(s.state.Teams))
	for _, team := range s.state.Teams {
		valid[team.ID] = true
	}
	seen := make(map[string]bool, len(teamIDs))
	ids := make([]string, 0, len(teamIDs))
	for _, id := range teamIDs {
		if !valid[id] {
			return Room{}, fmt.Errorf("unknown team id %q", id)
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for i := range s.state.Rooms {
		if s.state.Rooms[i].ID != roomID {
			continue
		}
		// Block removing a team that still has running work in this room.
		current := make(map[string]bool, len(s.state.Rooms[i].TeamIDs))
		for _, id := range s.state.Rooms[i].TeamIDs {
			current[id] = true
		}
		var removed []string
		for id := range current {
			if !seen[id] {
				removed = append(removed, id)
			}
		}
		if len(removed) > 0 {
			removedSet := make(map[string]bool, len(removed))
			for _, id := range removed {
				removedSet[id] = true
			}
			if name := teamWithRunningRuns(&s.state, roomID, removedSet); name != "" {
				return Room{}, fmt.Errorf("team %q still has running work in this room — pause or cancel it before removing the team", name)
			}
		}
		previous := s.state.Rooms[i].TeamIDs
		s.state.Rooms[i].TeamIDs = ids
		if err := s.save(); err != nil {
			s.state.Rooms[i].TeamIDs = previous
			return Room{}, err
		}
		return s.state.Rooms[i], nil
	}
	return Room{}, fmt.Errorf("unknown room id %q", roomID)
}

// teamWithRunningRuns returns the name of a removed team that still has a run
// in flight in the room, or "" when no removed team has running work.
func teamWithRunningRuns(state *WorkspaceState, roomID string, removed map[string]bool) string {
	taskTeam := map[string]string{}
	for _, task := range state.Tasks {
		if task.RoomID == roomID && task.TeamID != "" {
			taskTeam[task.ID] = task.TeamID
		}
	}
	for _, run := range state.Runs {
		if run.RoomID != roomID || run.Status != "running" {
			continue
		}
		if tid, ok := taskTeam[run.TaskID]; ok && removed[tid] {
			for _, tm := range state.Teams {
				if tm.ID == tid {
					return tm.Name
				}
			}
			return tid
		}
	}
	return ""
}

// AddRoomQuestion records a decision request from an agent.
func (s *Store) AddRoomQuestion(roomID string, question AgentQuestion) (AgentQuestion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	question.Prompt = strings.TrimSpace(question.Prompt)
	if question.Prompt == "" {
		return AgentQuestion{}, errors.New("question prompt is required")
	}
	if len(question.Prompt) > 4000 {
		return AgentQuestion{}, errors.New("question prompt exceeds 4000 characters")
	}
	if question.ID == "" {
		question.ID = uuid.NewString()
	}
	if question.CreatedAt.IsZero() {
		question.CreatedAt = time.Now().UTC()
	}
	for i := range s.state.Rooms {
		if s.state.Rooms[i].ID != roomID {
			continue
		}
		previous := s.state.Rooms[i].Questions
		s.state.Rooms[i].Questions = append([]AgentQuestion{question}, previous...)
		if err := s.save(); err != nil {
			s.state.Rooms[i].Questions = previous
			return AgentQuestion{}, err
		}
		return question, nil
	}
	return AgentQuestion{}, fmt.Errorf("unknown room id %q", roomID)
}

// AnswerRoomQuestion stores a human answer and appends it to the room chat so
// the decision stays in the transcript.
func (s *Store) AnswerRoomQuestion(roomID, questionID, answer string) (AgentQuestion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return AgentQuestion{}, errors.New("answer cannot be empty")
	}
	if len(answer) > 8000 {
		return AgentQuestion{}, errors.New("answer exceeds 8000 characters")
	}
	for i := range s.state.Rooms {
		if s.state.Rooms[i].ID != roomID {
			continue
		}
		room := &s.state.Rooms[i]
		for j := range room.Questions {
			if room.Questions[j].ID != questionID {
				continue
			}
			if room.Questions[j].Answer != "" {
				return AgentQuestion{}, errors.New("question has already been answered")
			}
			previousQuestions := append([]AgentQuestion(nil), room.Questions...)
			previousMessages := room.Messages
			room.Questions[j].Answer = answer
			room.Questions[j].AnsweredAt = time.Now().UTC()
			room.Messages = append(previousMessages, NewChatMessage("user", answer))
			if err := s.save(); err != nil {
				room.Questions = previousQuestions
				room.Messages = previousMessages
				return AgentQuestion{}, err
			}
			return room.Questions[j], nil
		}
		return AgentQuestion{}, fmt.Errorf("unknown question id %q", questionID)
	}
	return AgentQuestion{}, fmt.Errorf("unknown room id %q", roomID)
}

// ImportTeam clones a template into a new team. Team members are copies; the
// global agent library is left untouched to avoid duplication.
func (s *Store) ImportTeam(templateID, name string) (Team, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = strings.TrimSpace(name)
	if name == "" {
		return Team{}, errors.New("team name is required")
	}
	var source *TeamTemplate
	for i := range s.state.Templates {
		if s.state.Templates[i].ID == templateID {
			source = &s.state.Templates[i]
			break
		}
	}
	if source == nil {
		return Team{}, fmt.Errorf("unknown team template %q", templateID)
	}
	data, err := json.Marshal(source)
	if err != nil {
		return Team{}, err
	}
	var cloned TeamTemplate
	if err := json.Unmarshal(data, &cloned); err != nil {
		return Team{}, err
	}
	t := Team{
		ID: uuid.NewString(), TemplateID: cloned.ID, Name: name,
		Purpose: cloned.Purpose, MainAgent: cloned.MainAgent,
		Agents: cloned.Agents, Instructions: cloned.Instructions,
		CreatedAt: time.Now().UTC(),
	}
	for i := range t.Agents {
		t.Agents[i].Tools = NormalizeToolIDs(t.Agents[i].Tools)
		_ = s.normalizeSkillRefs(&t.Agents[i])
		if t.Agents[i].ID == "" {
			t.Agents[i].ID = uuid.NewString()
		}
	}
	previousTeams := s.state.Teams
	previousAgents := s.state.Agents
	s.state.Teams = append([]Team{t}, previousTeams...)
	s.syncAgentsFromTeamsLocked()
	s.syncSkillsFromDefaultLocked()
	if err := s.save(); err != nil {
		s.state.Teams = previousTeams
		s.state.Agents = previousAgents
		return Team{}, err
	}
	return t, nil
}

// DeleteTeam removes a team by ID. Reusable agent profiles in the library remain
// intact so users can continue using or assigning those agent profiles.
func (s *Store) DeleteTeam(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("team id is required")
	}
	found := false
	next := s.state.Teams[:0]
	for _, t := range s.state.Teams {
		if t.ID == id {
			found = true
			continue
		}
		next = append(next, t)
	}
	if !found {
		return fmt.Errorf("unknown team id %q", id)
	}
	previous := s.state.Teams
	s.state.Teams = next
	if err := s.save(); err != nil {
		s.state.Teams = previous
		return err
	}
	return nil
}

// ResetGlobalCollections clears user-level collections so users can start
// clean. Templates and providers are kept; agents/teams are the common reset.
func (s *Store) ResetGlobalCollections(agents, teams bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if agents {
		s.state.Agents = []Agent{}
	}
	if teams {
		s.state.Teams = []Team{}
	}
	return s.save()
}

func normalizeSettings(v Settings) Settings {
	if v.Theme != "light" && v.Theme != "dark" && v.Theme != "system" {
		v.Theme = "dark"
	}
	if v.EffortMode != "manual" && v.EffortMode != "automatic" {
		v.EffortMode = "automatic"
	}
	if v.DefaultEffort != "low" && v.DefaultEffort != "medium" && v.DefaultEffort != "high" {
		v.DefaultEffort = "medium"
	}
	if v.MaxConcurrentRuns <= 1 {
		// 1 was the legacy default but was never actually enforced (a fixed
		// 8-slot semaphore was). Default to 4 so spawned/delegated agents can
		// run in parallel; the user can lower this deliberately from Settings.
		v.MaxConcurrentRuns = 4
	}
	if v.MaxConcurrentRuns > 8 {
		v.MaxConcurrentRuns = 8
	}
	if v.MaxWorkersPerTask <= 0 {
		v.MaxWorkersPerTask = 1
	}
	if v.MaxWorkersPerTask > 8 {
		v.MaxWorkersPerTask = 8
	}
	if v.MaxDelegationDepth < 0 {
		v.MaxDelegationDepth = 0
	}
	if v.MaxDelegationDepth > 5 {
		v.MaxDelegationDepth = 5
	}
	if v.MaxRetries < 0 {
		v.MaxRetries = 0
	}
	if v.MaxRetries > 5 {
		v.MaxRetries = 5
	}
	if v.MaxIterations <= 0 {
		// Action-round budget per run. 30 has been the implicit default; keep it
		// but make it tunable so a task can be given more room without editing
		// code.
		v.MaxIterations = 30
	}
	if v.MaxIterations > 200 {
		v.MaxIterations = 200
	}
	if v.ReadOnlyBudget <= 0 {
		// Consecutive read-only rounds allowed before the loop governor forces
		// the agent to act or summarize. Deliberately generous so ordinary
		// exploration is never cut short.
		v.ReadOnlyBudget = 40
	}
	if v.ReadOnlyBudget > 500 {
		v.ReadOnlyBudget = 500
	}
	if v.MaxExecutionSeconds <= 0 {
		v.MaxExecutionSeconds = 600
	}
	if v.MaxExecutionSeconds > 3600 {
		v.MaxExecutionSeconds = 3600
	}
	if v.BudgetUSD < 0 || math.IsNaN(v.BudgetUSD) || math.IsInf(v.BudgetUSD, 0) {
		v.BudgetUSD = 0
	}
	if v.CompactionThreshold <= 0 {
		// Summarize the conversation once a single prompt exceeds this many
		// tokens. High enough that ordinary runs never pay for it.
		v.CompactionThreshold = 120000
	}
	if v.CompactionThreshold > 1000000 {
		v.CompactionThreshold = 1000000
	}
	if v.CompactionRetention <= 0 {
		v.CompactionRetention = 24
	}
	if v.CompactionRetention > 200 {
		v.CompactionRetention = 200
	}
	return v
}

func (s *Store) UpdateTeam(team Team) (Team, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	team.ID = strings.TrimSpace(team.ID)
	team.Name = strings.TrimSpace(team.Name)
	team.Purpose = strings.TrimSpace(team.Purpose)
	team.MainAgent = strings.TrimSpace(team.MainAgent)
	if team.ID == "" || team.Name == "" || team.MainAgent == "" || len(team.Agents) == 0 {
		return Team{}, errors.New("team id, name, main agent, and at least one agent are required")
	}
	found := false
	for i := range s.state.Teams {
		if s.state.Teams[i].ID == team.ID {
			if team.CreatedAt.IsZero() {
				team.CreatedAt = s.state.Teams[i].CreatedAt
			}
			found = true
			break
		}
	}
	if !found {
		return Team{}, fmt.Errorf("unknown team id %q", team.ID)
	}
	team.MainAgent = strings.TrimSpace(team.MainAgent)
	for i := range team.Agents {
		if err := s.normalizeSkillRefs(&team.Agents[i]); err != nil {
			return Team{}, err
		}
	}
	for _, agent := range team.Agents {
		if agent.ProviderID == "" {
			continue
		}
		valid := false
		for _, configured := range s.state.Providers {
			if configured.ID == agent.ProviderID {
				valid = true
				break
			}
		}
		if !valid {
			return Team{}, fmt.Errorf("unknown provider id %q", agent.ProviderID)
		}
	}
	mainFound := false
	seen := map[string]bool{}
	for i := range team.Agents {
		a := &team.Agents[i]
		a.Name, a.Role = strings.TrimSpace(a.Name), strings.TrimSpace(a.Role)
		if a.Name == "" || a.Role == "" || seen[a.Name] {
			return Team{}, errors.New("agents must have unique names and non-empty names and roles")
		}
		seen[a.Name] = true
		a.Tools = NormalizeToolIDs(a.Tools)
		if a.Name == team.MainAgent {
			mainFound = true
		}
		if a.ID == "" {
			a.ID = uuid.NewString()
		}
	}
	if !mainFound {
		return Team{}, errors.New("main agent must match one of the team's agent names")
	}
	previous := s.state.Teams
	s.state.Teams = append([]Team(nil), previous...)
	for i := range s.state.Teams {
		if s.state.Teams[i].ID == team.ID {
			s.state.Teams[i] = team
			break
		}
	}
	if err := s.save(); err != nil {
		s.state.Teams = previous
		return Team{}, err
	}
	return team, nil
}

// ListMCPServers returns the global MCP server definitions.
func (s *Store) ListMCPServers() []MCPServer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]MCPServer(nil), s.state.MCPServers...)
}

// GetMCPServer returns one global MCP server definition.
func (s *Store) GetMCPServer(id string) (MCPServer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, srv := range s.state.MCPServers {
		if srv.ID == id {
			return srv, true
		}
	}
	return MCPServer{}, false
}

// SaveMCPServer creates or updates a global MCP server definition.
func (s *Store) SaveMCPServer(srv MCPServer) (MCPServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	srv.Name = strings.TrimSpace(srv.Name)
	if srv.Name == "" {
		return MCPServer{}, errors.New("mcp server name is required")
	}
	srv.Type = strings.ToLower(strings.TrimSpace(srv.Type))
	if srv.Type != "stdio" && srv.Type != "http" {
		return MCPServer{}, errors.New("mcp server type must be stdio or http")
	}
	if srv.Type == "http" && strings.TrimSpace(srv.URL) == "" {
		return MCPServer{}, errors.New("mcp http server requires a URL")
	}
	if srv.Type == "stdio" && strings.TrimSpace(srv.Command) == "" {
		return MCPServer{}, errors.New("mcp stdio server requires a command")
	}
	for i := range s.state.MCPServers {
		if s.state.MCPServers[i].ID == srv.ID {
			previous := s.state.MCPServers
			srv.ID = s.state.MCPServers[i].ID
			s.state.MCPServers[i] = srv
			if err := s.save(); err != nil {
				s.state.MCPServers = previous
				return MCPServer{}, err
			}
			return srv, nil
		}
	}
	if srv.ID == "" {
		srv.ID = uuid.NewString()
	}
	previous := s.state.MCPServers
	s.state.MCPServers = append(s.state.MCPServers, srv)
	if err := s.save(); err != nil {
		s.state.MCPServers = previous
		return MCPServer{}, err
	}
	return srv, nil
}

// DeleteMCPServer removes a global MCP server and drops it from all rooms.
func (s *Store) DeleteMCPServer(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.state.MCPServers {
		if s.state.MCPServers[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("unknown mcp server id %q", id)
	}
	previousServers := s.state.MCPServers
	s.state.MCPServers = append(s.state.MCPServers[:idx], s.state.MCPServers[idx+1:]...)
	// detach from rooms
	prevRooms := append([]Room(nil), s.state.Rooms...)
	for i := range s.state.Rooms {
		filtered := s.state.Rooms[i].MCPServerIDs[:0]
		for _, sid := range s.state.Rooms[i].MCPServerIDs {
			if sid != id {
				filtered = append(filtered, sid)
			}
		}
		s.state.Rooms[i].MCPServerIDs = filtered
	}
	if err := s.save(); err != nil {
		s.state.MCPServers = previousServers
		s.state.Rooms = prevRooms
		return err
	}
	return nil
}

// MCPServersByIDs resolves enabled server definitions for the given ids.
func (s *Store) MCPServersByIDs(ids []string) []MCPServer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []MCPServer
	for _, srv := range s.state.MCPServers {
		if want[srv.ID] && srv.Enabled {
			out = append(out, srv)
		}
	}
	return out
}

// SetRoomMCP replaces the room's enabled MCP server ids.
func (s *Store) SetRoomMCP(roomID string, ids []string) (Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	valid := map[string]bool{}
	for _, srv := range s.state.MCPServers {
		valid[srv.ID] = true
	}
	seen := map[string]bool{}
	var clean []string
	for _, id := range ids {
		if !valid[id] {
			return Room{}, fmt.Errorf("unknown mcp server id %q", id)
		}
		if !seen[id] {
			seen[id] = true
			clean = append(clean, id)
		}
	}
	for i := range s.state.Rooms {
		if s.state.Rooms[i].ID != roomID {
			continue
		}
		previous := s.state.Rooms[i]
		s.state.Rooms[i].MCPServerIDs = clean
		if err := s.save(); err != nil {
			s.state.Rooms[i] = previous
			return Room{}, err
		}
		return s.state.Rooms[i], nil
	}
	return Room{}, fmt.Errorf("unknown room id %q", roomID)
}

func (s *Store) SaveSkill(skill Skill) (Skill, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	skill.ID = strings.TrimSpace(skill.ID)
	skill.Name = strings.TrimSpace(skill.Name)
	skill.Description = strings.TrimSpace(skill.Description)
	skill.Instructions = strings.TrimSpace(skill.Instructions)
	if skill.Mode != SkillModeActive && skill.Mode != SkillModePassive {
		if skill.Enabled {
			skill.Mode = SkillModeActive
		} else {
			skill.Mode = SkillModePassive
		}
	}
	skill.Enabled = skill.Mode == SkillModeActive
	files, err := normalizeSkillFiles(skill.Files)
	if err != nil {
		return Skill{}, err
	}
	skill.Files = files
	if skill.Instructions == "" {
		skill.Instructions = instructionsFromFiles(files)
	}
	if skill.Name == "" || (skill.Instructions == "" && len(skill.Files) == 0) || len(skill.Name) > 100 || len(skill.Instructions) > 16000 {
		return Skill{}, errors.New("skill name and instructions are required (maximum 16000 instruction characters)")
	}
	if skill.ID == "" {
		skill.ID = uuid.NewString()
	}
	if skill.Version <= 0 {
		skill.Version = 1
	}
	previous := s.state.Skills
	s.state.Skills = append([]Skill(nil), previous...)
	updated := false
	for i := range s.state.Skills {
		if s.state.Skills[i].ID == skill.ID {
			skill.Version = s.state.Skills[i].Version + 1
			s.state.Skills[i] = skill
			updated = true
			break
		}
	}
	if !updated {
		s.state.Skills = append([]Skill{skill}, s.state.Skills...)
	}
	if err := s.save(); err != nil {
		s.state.Skills = previous
		return Skill{}, err
	}
	return skill, nil
}

// normalizeSkillFiles validates and normalizes a skill bundle. Paths must be
// relative, cannot escape the bundle root, and total content is capped so a
// workspace file cannot grow without bound.
func normalizeSkillFiles(files []SkillFile) ([]SkillFile, error) {
	if len(files) > 500 {
		return nil, errors.New("a skill may contain at most 500 files")
	}
	out := make([]SkillFile, 0, len(files))
	seen := map[string]bool{}
	total := 0
	for _, file := range files {
		path := strings.TrimSpace(strings.ReplaceAll(file.Path, "\\", "/"))
		path = strings.TrimPrefix(path, "./")
		if path == "" || len(path) > 300 {
			return nil, errors.New("skill file paths must be non-empty and at most 300 characters")
		}
		if strings.HasPrefix(path, "/") || path == ".." || strings.HasPrefix(path, "../") || strings.Contains(path, "/../") {
			return nil, fmt.Errorf("skill file path %q must stay inside the skill root", path)
		}
		if seen[path] {
			return nil, fmt.Errorf("duplicate skill file path %q", path)
		}
		seen[path] = true
		if len(file.Content) > 262144 {
			return nil, fmt.Errorf("skill file %q exceeds the 256 KiB limit", path)
		}
		total += len(file.Content)
		if total > 1048576 {
			return nil, errors.New("skill bundle exceeds the 1 MiB total limit")
		}
		out = append(out, SkillFile{Path: path, Content: file.Content})
	}
	return out, nil
}

func instructionsFromFiles(files []SkillFile) string {
	for _, file := range files {
		base := file.Path
		if idx := strings.LastIndex(base, "/"); idx >= 0 {
			base = base[idx+1:]
		}
		if strings.EqualFold(base, "SKILL.md") || strings.EqualFold(base, "README.md") {
			return strings.TrimSpace(file.Content)
		}
	}
	for _, file := range files {
		if strings.HasSuffix(strings.ToLower(file.Path), ".md") {
			return strings.TrimSpace(file.Content)
		}
	}
	return ""
}

// roleTypeLabel maps a role type to a human-readable title, used when an agent
// is saved with a role type but no explicit role title.
func roleTypeLabel(roleType string) string {
	switch roleType {
	case "coordinator":
		return "Coordinator"
	case "specialist":
		return "Specialist"
	case "reviewer":
		return "Reviewer"
	case "custom":
		return "Custom agent"
	}
	return ""
}

// SaveAgent creates or updates an agent library profile. Agent names are the
// identity used by teams and tasks, so they must be unique.
func (s *Store) SaveAgent(agent Agent) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	agent.Name = strings.TrimSpace(agent.Name)
	agent.Role = strings.TrimSpace(agent.Role)
	agent.Description = strings.TrimSpace(agent.Description)
	agent.Instructions = strings.TrimSpace(agent.Instructions)
	agent.Model = strings.TrimSpace(agent.Model)
	agent.Tools = NormalizeToolIDs(agent.Tools)
	agent.AvatarSeed = strings.TrimSpace(agent.AvatarSeed)
	agent.AvatarStyle = strings.TrimSpace(agent.AvatarStyle)
	agent.RoleType = strings.ToLower(strings.TrimSpace(agent.RoleType))
	switch agent.RoleType {
	case "", "coordinator", "specialist", "reviewer", "custom":
	default:
		return Agent{}, errors.New("role type must be coordinator, specialist, reviewer, or custom")
	}
	// A role title is required for display; when the user only picked a role
	// from the dropdown, derive the title from it.
	if agent.Role == "" {
		agent.Role = roleTypeLabel(agent.RoleType)
	}
	if agent.Name == "" || agent.Role == "" {
		return Agent{}, errors.New("agent name and role are required")
	}
	if len(agent.Name) > 80 || len(agent.Role) > 160 || len(agent.Instructions) > 16000 {
		return Agent{}, errors.New("agent name, role, or instructions exceed the allowed length")
	}
	if len(agent.AvatarSeed) > 200 || len(agent.AvatarStyle) > 60 {
		return Agent{}, errors.New("avatar seed or style exceed the allowed length")
	}
	if agent.CanSpawn != "" && agent.CanSpawn != "request" && agent.CanSpawn != "limited" {
		return Agent{}, errors.New("spawn authority must be request or limited")
	}
	if agent.MaxIterations < 0 || agent.MaxIterations > 200 {
		return Agent{}, errors.New("agent max iterations must be between 0 and 200")
	}
	if agent.ReadOnlyBudget < 0 || agent.ReadOnlyBudget > 500 {
		return Agent{}, errors.New("agent read-only budget must be between 0 and 500")
	}
	if agent.ID == "" {
		agent.ID = uuid.NewString()
	}
	// Give new agents (and agents saved without one) a random avatar. An
	// existing seed/style is preserved so re-saving never re-rolls it.
	assignAvatar(&agent)
	if err := s.normalizeSkillRefs(&agent); err != nil {
		return Agent{}, err
	}
	if agent.ProviderID != "" {
		valid := false
		for _, provider := range s.state.Providers {
			if provider.ID == agent.ProviderID {
				valid = true
				break
			}
		}
		if !valid {
			return Agent{}, fmt.Errorf("unknown provider id %q", agent.ProviderID)
		}
	}
	for i := range s.state.Agents {
		if s.state.Agents[i].ID != agent.ID && strings.EqualFold(s.state.Agents[i].Name, agent.Name) {
			return Agent{}, fmt.Errorf("an agent named %q already exists", agent.Name)
		}
	}
	previous := s.state.Agents
	s.state.Agents = append([]Agent(nil), previous...)
	updated := false
	for i := range s.state.Agents {
		if s.state.Agents[i].ID == agent.ID {
			s.state.Agents[i] = agent
			updated = true
			break
		}
	}
	if !updated {
		s.state.Agents = append([]Agent{agent}, s.state.Agents...)
	}
	if err := s.save(); err != nil {
		s.state.Agents = previous
		return Agent{}, err
	}
	return agent, nil
}

func (s *Store) DeleteAgent(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.state.Agents
	filtered := make([]Agent, 0, len(previous))
	found := false
	for _, agent := range previous {
		if agent.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, agent)
	}
	if !found {
		return fmt.Errorf("unknown agent id %q", id)
	}
	s.state.Agents = filtered
	if err := s.save(); err != nil {
		s.state.Agents = previous
		return err
	}
	return nil
}

func (s *Store) DeleteSkill(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previousSkills := s.state.Skills
	previousAgents := s.state.Agents
	previousTeams := s.state.Teams
	filtered := make([]Skill, 0, len(previousSkills))
	for _, skill := range previousSkills {
		if skill.ID != id {
			filtered = append(filtered, skill)
		}
	}
	if len(filtered) == len(previousSkills) {
		return fmt.Errorf("unknown skill id %q", id)
	}
	// Strip dangling references from agent profiles and team rosters.
	s.state.Agents = append([]Agent(nil), previousAgents...)
	for i := range s.state.Agents {
		s.state.Agents[i].Skills = withoutSkillRef(s.state.Agents[i].Skills, id)
	}
	s.state.Teams = append([]Team(nil), previousTeams...)
	for i := range s.state.Teams {
		s.state.Teams[i].Agents = append([]Agent(nil), previousTeams[i].Agents...)
		for j := range s.state.Teams[i].Agents {
			s.state.Teams[i].Agents[j].Skills = withoutSkillRef(s.state.Teams[i].Agents[j].Skills, id)
		}
	}
	s.state.Skills = filtered
	if err := s.save(); err != nil {
		s.state.Skills = previousSkills
		s.state.Agents = previousAgents
		s.state.Teams = previousTeams
		return err
	}
	return nil
}

func withoutSkillRef(refs []string, id string) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref != id {
			out = append(out, ref)
		}
	}
	return out
}

func (s *Store) UpdateSettings(settings Settings) (Settings, error) {
	if settings.Theme != "light" && settings.Theme != "dark" && settings.Theme != "system" {
		return Settings{}, errors.New("theme must be light, dark, or system")
	}
	if settings.EffortMode != "manual" && settings.EffortMode != "automatic" {
		return Settings{}, errors.New("effort mode must be manual or automatic")
	}
	if settings.DefaultEffort != "low" && settings.DefaultEffort != "medium" && settings.DefaultEffort != "high" {
		return Settings{}, errors.New("default effort must be low, medium, or high")
	}
	// Older clients omit the loop-budget fields; treat 0 as "use the default"
	// so they keep working instead of failing validation.
	if settings.MaxIterations == 0 {
		settings.MaxIterations = 30
	}
	if settings.ReadOnlyBudget == 0 {
		settings.ReadOnlyBudget = 40
	}
	if settings.CompactionThreshold == 0 {
		settings.CompactionThreshold = 120000
	}
	if settings.CompactionRetention == 0 {
		settings.CompactionRetention = 24
	}
	if settings.MaxConcurrentRuns < 1 || settings.MaxConcurrentRuns > 8 || settings.MaxWorkersPerTask < 1 || settings.MaxWorkersPerTask > 8 || settings.MaxDelegationDepth < 0 || settings.MaxDelegationDepth > 5 || settings.MaxRetries < 0 || settings.MaxRetries > 5 || settings.MaxIterations < 1 || settings.MaxIterations > 200 || settings.ReadOnlyBudget < 1 || settings.ReadOnlyBudget > 500 || settings.CompactionThreshold < 1000 || settings.CompactionThreshold > 1000000 || settings.CompactionRetention < 1 || settings.CompactionRetention > 200 || settings.MaxExecutionSeconds < 10 || settings.MaxExecutionSeconds > 3600 || settings.BudgetUSD < 0 || math.IsNaN(settings.BudgetUSD) || math.IsInf(settings.BudgetUSD, 0) {
		return Settings{}, errors.New("policy values are outside supported limits")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.state.Settings
	s.state.Settings = settings
	if err := s.save(); err != nil {
		s.state.Settings = previous
		return Settings{}, err
	}
	return settings, nil
}

func (s *Store) UpsertProvider(config ProviderConfig) (ProviderConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	config.ID = strings.TrimSpace(config.ID)
	config.Name = strings.TrimSpace(config.Name)
	if err := validateProviderInput(config.ID, config.Name, config.Protocol, config.BaseURL, config.DefaultModel); err != nil {
		return ProviderConfig{}, err
	}
	for _, r := range config.ID {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return ProviderConfig{}, errors.New("provider id must contain only lowercase letters, digits, hyphens, or underscores")
		}
	}
	if len(config.Models) == 0 {
		config.Models = []string{config.DefaultModel}
	}
	if len(config.Models) > 100 {
		return ProviderConfig{}, errors.New("provider may declare at most 100 models")
	}
	for _, model := range config.Models {
		if strings.TrimSpace(model) == "" || len(model) > 200 {
			return ProviderConfig{}, errors.New("model IDs must be non-empty and at most 200 characters")
		}
	}
	foundModel := false
	for _, model := range config.Models {
		if strings.TrimSpace(model) == config.DefaultModel {
			foundModel = true
		}
	}
	if !foundModel {
		return ProviderConfig{}, errors.New("default model must be present in models")
	}
	config.HasAPIKey = false // computed from the separate credential file on read
	previous := s.state.Providers
	s.state.Providers = append([]ProviderConfig(nil), previous...)
	updated := false
	for i := range s.state.Providers {
		if s.state.Providers[i].ID == config.ID {
			config.HasAPIKey = s.state.Providers[i].HasAPIKey
			s.state.Providers[i] = config
			updated = true
			break
		}
	}
	if !updated {
		s.state.Providers = append([]ProviderConfig{config}, s.state.Providers...)
	}
	if err := s.save(); err != nil {
		s.state.Providers = previous
		return ProviderConfig{}, err
	}
	return config, nil
}

func (s *Store) SetProviderSecretFlag(providerID string, has bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Providers {
		if s.state.Providers[i].ID == providerID {
			s.state.Providers[i].HasAPIKey = has
			return
		}
	}
}

func (s *Store) CreateTask(task Task) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task.RoomID = strings.TrimSpace(task.RoomID)
	task.Title = strings.TrimSpace(task.Title)
	task.Description = strings.TrimSpace(task.Description)
	if task.RoomID == "" || task.Title == "" || len(task.Title) > 200 || len(task.Description) > 16000 {
		return Task{}, errors.New("room and task title are required; title max 200 and description max 16000 characters")
	}
	roomFound := false
	for _, room := range s.state.Rooms {
		if room.ID == task.RoomID {
			roomFound = true
			break
		}
	}
	if !roomFound {
		return Task{}, fmt.Errorf("unknown room id %q", task.RoomID)
	}
	if task.AgentName != "" && task.TeamID == "" {
		return Task{}, errors.New("an assigned agent requires an assigned team")
	}
	if task.TeamID != "" {
		valid := false
		for _, team := range s.state.Teams {
			if team.ID == task.TeamID {
				for _, room := range s.state.Rooms {
					if room.ID == task.RoomID {
						for _, invited := range room.TeamIDs {
							if invited == team.ID {
								valid = true
							}
						}
					}
				}
			}
		}
		if !valid {
			return Task{}, errors.New("task team must be invited to its room")
		}
	}
	if task.TeamID != "" {
		for _, team := range s.state.Teams {
			if team.ID != task.TeamID {
				continue
			}
			if task.AgentName == "" {
				task.AgentName = team.MainAgent
			} else {
				found := false
				for _, agent := range team.Agents {
					if agent.Name == task.AgentName {
						found = true
						break
					}
				}
				if !found {
					return Task{}, errors.New("assigned agent must belong to the selected team")
				}
			}
		}
	}
	if canon, ok := NormalizePriority(task.Priority); ok {
		task.Priority = canon
	} else {
		return Task{}, errors.New("priority must be P0, P1, P2, or P3")
	}
	if task.Effort == "" {
		task.Effort = s.state.Settings.DefaultEffort
	}
	if task.Effort != "low" && task.Effort != "medium" && task.Effort != "high" {
		return Task{}, errors.New("effort must be low, medium, or high")
	}
	if task.Status == "" {
		task.Status = "todo"
	}
	if task.Status != "todo" && task.Status != "blocked" {
		return Task{}, errors.New("new task status must be todo or blocked")
	}
	if task.ID == "" {
		task.ID = uuid.NewString()
	}
	for _, existing := range s.state.Tasks {
		if existing.ID == task.ID {
			return Task{}, errors.New("duplicate task id")
		}
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now().UTC()
	}
	task.UpdatedAt = time.Now().UTC()
	deps := make([]string, 0, len(task.Dependencies))
	seen := map[string]bool{}
	for _, depID := range task.Dependencies {
		if depID == task.ID || seen[depID] {
			if depID == task.ID {
				return Task{}, errors.New("task cannot depend on itself")
			}
			continue
		}
		var dep *Task
		for i := range s.state.Tasks {
			if s.state.Tasks[i].ID == depID {
				dep = &s.state.Tasks[i]
				break
			}
		}
		if dep == nil || dep.RoomID != task.RoomID {
			return Task{}, fmt.Errorf("dependency %q must be an existing task in the same room", depID)
		}
		if dep.Status != "done" {
			task.Status = "blocked"
		}
		seen[depID] = true
		deps = append(deps, depID)
	}
	task.Dependencies = deps
	previous := s.state.Tasks
	s.state.Tasks = append([]Task{task}, previous...)
	if err := s.save(); err != nil {
		s.state.Tasks = previous
		return Task{}, err
	}
	return task, nil
}

// FindOpenTaskByTitle returns an existing, not-yet-finished task in the room
// assigned to the same agent with the same normalized title. It is used to stop
// agents from creating the exact same card twice (a common source of duplicate
// Kanban cards).
func (s *Store) FindOpenTaskByTitle(roomID, agentName, title string) (Task, bool) {
	norm := normalizeTitleKey(title)
	if norm == "" {
		return Task{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.state.Tasks {
		t := s.state.Tasks[i]
		if t.RoomID != roomID || t.AgentName != agentName {
			continue
		}
		if t.Status == "done" || t.Status == "cancelled" {
			continue
		}
		if normalizeTitleKey(t.Title) == norm {
			return t, true
		}
	}
	return Task{}, false
}

func normalizeTitleKey(title string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(title)), " "))
}

// maxReworkCycles bounds how many times a task may be sent back for rework
// before escalating to a human.
const maxReworkCycles = 3

// ReopenForRework returns a task to in_progress (bumping its retry counter)
// so its implementer can address reviewer feedback. It returns ok=false and
// nil error when the rework budget is exhausted.
func (s *Store) ReopenForRework(taskID string) (Task, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.state.Tasks {
		if s.state.Tasks[i].ID == taskID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Task{}, false, fmt.Errorf("unknown task id %q", taskID)
	}
	task := &s.state.Tasks[idx]
	if task.RetryCount >= maxReworkCycles {
		return *task, false, nil
	}
	// dependency guard (same as UpdateTaskStatus going in_progress)
	for _, depID := range task.Dependencies {
		for _, dep := range s.state.Tasks {
			if dep.ID == depID && dep.Status != "done" {
				return Task{}, false, fmt.Errorf("task is blocked by incomplete dependency %q", depID)
			}
		}
	}
	previous := append([]Task(nil), s.state.Tasks...)
	task.Status = "in_progress"
	task.RetryCount++
	task.UpdatedAt = time.Now().UTC()
	if err := s.save(); err != nil {
		s.state.Tasks = previous
		return Task{}, false, err
	}
	return *task, true, nil
}

func (s *Store) UpdateTaskStatus(taskID, status string, approved bool) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	validStatus := map[string]bool{"todo": true, "blocked": true, "in_progress": true, "review": true, "done": true, "cancelled": true, "paused": true}
	if !validStatus[status] {
		return Task{}, errors.New("invalid task status")
	}
	idx := -1
	for i := range s.state.Tasks {
		if s.state.Tasks[i].ID == taskID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Task{}, fmt.Errorf("unknown task id %q", taskID)
	}
	previous := append([]Task(nil), s.state.Tasks...)
	task := &s.state.Tasks[idx]
	if status == "in_progress" {
		for _, depID := range task.Dependencies {
			for _, dep := range s.state.Tasks {
				if dep.ID == depID && dep.Status != "done" {
					return Task{}, fmt.Errorf("task is blocked by incomplete dependency %q", depID)
				}
			}
		}
		if s.state.Settings.RequireRunApproval && !task.Approved && !approved {
			return Task{}, errors.New("task requires approval before it can start")
		}
	}
	if approved {
		task.Approved = true
	}
	task.Status = status
	task.UpdatedAt = time.Now().UTC()
	if err := s.save(); err != nil {
		s.state.Tasks = previous
		return Task{}, err
	}
	return *task, nil
}

// SetTaskSeverity updates a task's issue severity (info|warning|blocker).
func (s *Store) SetTaskSeverity(taskID, severity string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.state.Tasks {
		if s.state.Tasks[i].ID == taskID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Task{}, fmt.Errorf("unknown task id %q", taskID)
	}
	previous := append([]Task(nil), s.state.Tasks...)
	task := &s.state.Tasks[idx]
	task.Severity = normalizeSeverity(severity)
	task.UpdatedAt = time.Now().UTC()
	if err := s.save(); err != nil {
		s.state.Tasks = previous
		return Task{}, err
	}
	return *task, nil
}

// TaskDetailsPatch carries the editable fields for UpdateTaskDetails. A nil
// field is left unchanged; a non-nil pointer (even to "") sets it.
type TaskDetailsPatch struct {
	Title        *string   `json:"title,omitempty"`
	Description  *string   `json:"description,omitempty"`
	Priority     *string   `json:"priority,omitempty"`
	Effort       *string   `json:"effort,omitempty"`
	AgentName    *string   `json:"agentName,omitempty"`
	Dependencies *[]string `json:"dependencies,omitempty"`
}

// UpdateTaskDetails applies an edit to a task's title/description/priority/
// effort/assignee/dependencies. Status changes stay in UpdateTaskStatus so the
// two concerns do not interfere.
func (s *Store) UpdateTaskDetails(taskID string, patch TaskDetailsPatch) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.state.Tasks {
		if s.state.Tasks[i].ID == taskID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Task{}, fmt.Errorf("unknown task id %q", taskID)
	}
	previous := append([]Task(nil), s.state.Tasks...)
	task := &s.state.Tasks[idx]

	if patch.Title != nil {
		title := strings.TrimSpace(*patch.Title)
		if title == "" || len(title) > 200 {
			return Task{}, errors.New("task title is required and must be at most 200 characters")
		}
		task.Title = title
	}
	if patch.Description != nil {
		desc := strings.TrimSpace(*patch.Description)
		if len(desc) > 16000 {
			return Task{}, errors.New("description must be at most 16000 characters")
		}
		task.Description = desc
	}
	if patch.Priority != nil {
		canon, ok := NormalizePriority(*patch.Priority)
		if !ok {
			return Task{}, errors.New("priority must be P0, P1, P2, or P3")
		}
		task.Priority = canon
	}
	if patch.Effort != nil {
		effort := strings.TrimSpace(*patch.Effort)
		if effort != "" && effort != "low" && effort != "medium" && effort != "high" {
			return Task{}, errors.New("effort must be low, medium, or high")
		}
		if effort != "" {
			task.Effort = effort
		}
	}
	if patch.AgentName != nil {
		agentName := strings.TrimSpace(*patch.AgentName)
		if agentName != "" {
			ok := false
			for _, team := range s.state.Teams {
				if team.ID != task.TeamID {
					continue
				}
				for _, a := range team.Agents {
					if a.Name == agentName {
						ok = true
						break
					}
				}
			}
			if !ok {
				return Task{}, errors.New("assigned agent must belong to the task's team")
			}
		}
		task.AgentName = agentName
	}
	if patch.Dependencies != nil {
		deps := make([]string, 0, len(*patch.Dependencies))
		seen := map[string]bool{}
		for _, depID := range *patch.Dependencies {
			if depID == task.ID || seen[depID] {
				continue
			}
			var dep *Task
			for i := range s.state.Tasks {
				if s.state.Tasks[i].ID == depID {
					dep = &s.state.Tasks[i]
					break
				}
			}
			if dep == nil || dep.RoomID != task.RoomID {
				return Task{}, fmt.Errorf("dependency %q must be an existing task in the same room", depID)
			}
			seen[depID] = true
			deps = append(deps, depID)
		}
		task.Dependencies = deps
	}
	task.UpdatedAt = time.Now().UTC()
	if err := s.save(); err != nil {
		s.state.Tasks = previous
		return Task{}, err
	}
	return *task, nil
}

func (s *Store) PrepareTaskRun(taskID string) (Task, Room, *Team, ProviderConfig, string, Settings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var task *Task
	for i := range s.state.Tasks {
		if s.state.Tasks[i].ID == taskID {
			copy := s.state.Tasks[i]
			task = &copy
			break
		}
	}
	if task == nil {
		return Task{}, Room{}, nil, ProviderConfig{}, "", Settings{}, fmt.Errorf("unknown task id %q", taskID)
	}
	if task.Status != "todo" && task.Status != "blocked" && task.Status != "paused" {
		return Task{}, Room{}, nil, ProviderConfig{}, "", Settings{}, errors.New("only todo, paused, or dependency-ready tasks can be run")
	}
	if task.Status == "blocked" {
		return Task{}, Room{}, nil, ProviderConfig{}, "", Settings{}, errors.New("task is blocked")
	}
	for _, depID := range task.Dependencies {
		for _, dep := range s.state.Tasks {
			if dep.ID == depID && dep.Status != "done" {
				return Task{}, Room{}, nil, ProviderConfig{}, "", Settings{}, fmt.Errorf("task is blocked by incomplete dependency %q", depID)
			}
		}
	}
	if s.state.Settings.RequireRunApproval && !task.Approved {
		return Task{}, Room{}, nil, ProviderConfig{}, "", Settings{}, errors.New("task requires approval before it can run")
	}
	var room *Room
	for i := range s.state.Rooms {
		if s.state.Rooms[i].ID == task.RoomID {
			copy := s.state.Rooms[i]
			room = &copy
			break
		}
	}
	if room == nil {
		return Task{}, Room{}, nil, ProviderConfig{}, "", Settings{}, errors.New("task's room no longer exists")
	}
	var team *Team
	if task.TeamID != "" {
		for i := range s.state.Teams {
			if s.state.Teams[i].ID == task.TeamID {
				copy := s.state.Teams[i]
				team = &copy
				break
			}
		}
	}
	var config *ProviderConfig
	agentModel := ""
	if team != nil {
		target := task.AgentName
		if target == "" {
			target = team.MainAgent
		}
		for _, agent := range team.Agents {
			if agent.Name != target {
				continue
			}
			agentModel = strings.TrimSpace(agent.Model)
			if agent.ProviderID == "" {
				break
			}
			for i := range s.state.Providers {
				if s.state.Providers[i].ID == agent.ProviderID && s.state.Providers[i].Enabled {
					copy := s.state.Providers[i]
					config = &copy
					break
				}
			}
			break
		}
		// Fall back to any agent on the team that names an enabled provider.
		if config == nil {
			for _, agent := range team.Agents {
				if agent.ProviderID == "" {
					continue
				}
				for i := range s.state.Providers {
					if s.state.Providers[i].ID == agent.ProviderID && s.state.Providers[i].Enabled {
						copy := s.state.Providers[i]
						config = &copy
						if agentModel == "" {
							agentModel = strings.TrimSpace(agent.Model)
						}
						break
					}
				}
				if config != nil {
					break
				}
			}
		}
	}
	if config == nil {
		for i := range s.state.Providers {
			if s.state.Providers[i].Enabled {
				copy := s.state.Providers[i]
				config = &copy
				break
			}
		}
	}
	if config == nil {
		return Task{}, Room{}, nil, ProviderConfig{}, "", Settings{}, errors.New("no enabled provider is configured")
	}
	// An agent may pin a model that differs from the provider default.
	if agentModel != "" {
		config.DefaultModel = agentModel
	}
	secret, err := s.loadProviderCredential(s.state.ProjectPath, config.ID)
	if err != nil {
		return Task{}, Room{}, nil, ProviderConfig{}, "", Settings{}, err
	}
	if !config.HasAPIKey && secret == "" {
		return Task{}, Room{}, nil, ProviderConfig{}, "", Settings{}, fmt.Errorf("provider %q has no saved API key", config.ID)
	}
	return *task, *room, team, *config, secret, s.state.Settings, nil
}

func (s *Store) MarkTaskRunning(taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Tasks {
		if s.state.Tasks[i].ID != taskID {
			continue
		}
		task := &s.state.Tasks[i]
		startable := task.Status == "todo" || task.Status == "paused"
		// Hidden chat threads may also resume from "review" (defensive: after a
		// completed run they reset to todo, but never block a user prompt).
		if task.Hidden && task.Status == "review" {
			startable = true
		}
		if !startable {
			return fmt.Errorf("task cannot start from status %q", task.Status)
		}
		for _, depID := range task.Dependencies {
			for _, dep := range s.state.Tasks {
				if dep.ID == depID && dep.Status != "done" {
					return fmt.Errorf("task is blocked by incomplete dependency %q", depID)
				}
			}
		}
		task.Status, task.UpdatedAt = "in_progress", time.Now().UTC()
		if err := s.save(); err != nil {
			task.Status = "todo"
			return err
		}
		return nil
	}
	return fmt.Errorf("unknown task id %q", taskID)
}

func (s *Store) CreateRun(run AgentRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.state.Runs {
		if existing.ID == run.ID {
			return errors.New("duplicate run id")
		}
	}
	previous := s.state.Runs
	s.state.Runs = append([]AgentRun{run}, previous...)
	if err := s.save(); err != nil {
		s.state.Runs = previous
		return err
	}
	return nil
}

func (s *Store) AppendRunEvent(runID, eventType, message string) error {
	if len(message) > 4000 {
		message = message[:4000]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Runs {
		if s.state.Runs[i].ID != runID {
			continue
		}
		if s.state.Runs[i].Status != "running" {
			return nil
		}
		previous := s.state.Runs[i].Events
		event := RunEvent{ID: uuid.NewString(), Type: eventType, Message: message, CreatedAt: time.Now().UTC()}
		s.state.Runs[i].Events = append(append([]RunEvent(nil), previous...), event)
		if err := s.save(); err != nil {
			s.state.Runs[i].Events = previous
			return err
		}
		return nil
	}
	return fmt.Errorf("unknown run id %q", runID)
}

func (s *Store) FinishRun(run AgentRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Runs {
		if s.state.Runs[i].ID != run.ID {
			continue
		}
		if s.state.Runs[i].Status != "running" {
			return nil
		}
		run.Events = append([]RunEvent(nil), s.state.Runs[i].Events...)
		if len(run.Events) == 0 || run.Events[len(run.Events)-1].Type != run.Status {
			run.Events = append(run.Events, RunEvent{ID: uuid.NewString(), Type: run.Status, Message: run.Error, CreatedAt: time.Now().UTC()})
		}
		previous := s.state.Runs[i]
		var taskIndex = -1
		var previousTask Task
		for j := range s.state.Tasks {
			if s.state.Tasks[j].ID == run.TaskID {
				taskIndex = j
				previousTask = s.state.Tasks[j]
				break
			}
		}
		s.state.Runs[i] = run
		if taskIndex >= 0 {
			s.state.Tasks[taskIndex].LastRunID = run.ID
			s.state.Tasks[taskIndex].UpdatedAt = run.EndedAt
			switch run.Status {
			case "completed":
				// Visible Kanban work moves to Review gate; hidden chat threads
				// must return to todo so the next prompt can reuse the thread.
				if s.state.Tasks[taskIndex].Hidden {
					s.state.Tasks[taskIndex].Status = "todo"
				} else {
					s.state.Tasks[taskIndex].Status = "review"
				}
			case "paused":
				// A paused run keeps its progress; the task stays in_progress so
				// Continue can resume the saved conversation.
				if s.state.Tasks[taskIndex].Status == "todo" {
					s.state.Tasks[taskIndex].Status = "in_progress"
				}
			case "cancelled", "timed_out", "failed", "blocked":
				s.state.Tasks[taskIndex].Status = "todo"
			}
		}
		if err := s.save(); err != nil {
			s.state.Runs[i] = previous
			if taskIndex >= 0 {
				s.state.Tasks[taskIndex] = previousTask
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("unknown run id %q", run.ID)
}

func (s *Store) save() error {
	if err := writeJSONAtomic(s.path, s.state); err != nil {
		return err
	}
	if err := s.saveGlobal(); err != nil {
		return err
	}
	if fn := s.onChange; fn != nil {
		go fn()
	}
	return nil
}

// writeJSONAtomic writes v to path via a temp file + rename, 0600.
func writeJSONAtomic(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create Foreman data directory: %w", err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Foreman data: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".foreman-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary data file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write data: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync data: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close data: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("persist data: %w", err)
	}
	return nil
}

// DB returns the underlying SQLite DB if open.
func (s *Store) DB() *DB {
	return s.db
}

// SavePlan saves a plan to SQLite and syncs the room triage tier.
func (s *Store) SavePlan(p PlanRecord) error {
	if s.db == nil {
		return nil
	}
	if err := s.db.SavePlan(p); err != nil {
		return err
	}
	s.mu.Lock()
	for i := range s.state.Rooms {
		if s.state.Rooms[i].ID == p.RoomID {
			s.state.Rooms[i].TriageTier = p.Tier
			_ = s.save()
			break
		}
	}
	s.mu.Unlock()
	return nil
}

// GetPlan retrieves a room's plan.
func (s *Store) GetPlan(roomID string) (*PlanRecord, error) {
	if s.db == nil {
		return nil, nil
	}
	return s.db.GetPlan(roomID)
}

// ListArtifacts retrieves artifacts for a room.
func (s *Store) ListArtifacts(roomID string) ([]ArtifactRecord, error) {
	if s.db == nil {
		return []ArtifactRecord{}, nil
	}
	return s.db.ListArtifacts(roomID)
}

// SaveArtifact persists an artifact.
func (s *Store) SaveArtifact(a ArtifactRecord) error {
	if s.db == nil {
		return nil
	}
	return s.db.SaveArtifact(a)
}

// ListApprovals returns pending approvals for a room.
// GetApproval returns a single approval by id.
func (s *Store) GetApproval(id string) (ApprovalRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.state.Approvals {
		if a.ID == id {
			return a, true
		}
	}
	return ApprovalRecord{}, false
}

// ListApprovals returns approvals for a room.
func (s *Store) ListApprovals(roomID string) ([]ApprovalRecord, error) {
	if s.db == nil {
		return []ApprovalRecord{}, nil
	}
	return s.db.ListApprovals(roomID)
}

// CreateApproval creates a new approval item.
func (s *Store) CreateApproval(app ApprovalRecord) (string, error) {
	if s.db == nil {
		return "", nil
	}
	return s.db.CreateApproval(app)
}

// ResolveApproval approves or rejects an item.
func (s *Store) ResolveApproval(id, status string) error {
	if s.db == nil {
		return nil
	}
	return s.db.ResolveApproval(id, status)
}

// ListPanels lists panels for a room.
func (s *Store) ListPanels(roomID string, includePending bool) ([]PanelRecord, error) {
	if s.db == nil {
		return []PanelRecord{}, nil
	}
	return s.db.ListPanels(roomID, includePending)
}

// RegisterPanel registers a dynamic UI panel.
func (s *Store) RegisterPanel(p PanelRecord) (string, error) {
	if s.db == nil {
		return "", nil
	}
	return s.db.RegisterPanel(p)
}

// RecordActivity logs a compact activity message in room chat.
func (s *Store) RecordActivity(roomID, agentName, activityType, content string, metadata map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Rooms {
		if s.state.Rooms[i].ID == roomID {
			// Activity is written to the SQLite audit log only; the chat surface
			// uses AppendRoomNotice (single, curated line per action) to avoid
			// double bubbles for the same event.
			if s.db != nil {
				_ = s.db.RecordActivity(roomID, agentName, activityType, content, metadata)
			}
			return nil
		}
	}
	return fmt.Errorf("room not found: %s", roomID)
}

// SetOnChange registers a callback invoked after the store persists state.
func (s *Store) SetOnChange(fn func()) { s.onChange = fn }

// Close closes the underlying SQLite database.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// DeleteTask removes a task card from state and SQLite.
func (s *Store) DeleteTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Tasks {
		if s.state.Tasks[i].ID == id {
			previous := s.state.Tasks
			s.state.Tasks = append(s.state.Tasks[:i], s.state.Tasks[i+1:]...)
			if err := s.save(); err != nil {
				s.state.Tasks = previous
				return err
			}
			if s.db != nil {
				_ = s.db.DeleteTask(id)
			}
			return nil
		}
	}
	return fmt.Errorf("unknown task id %q", id)
}

// DeletePlan removes a room's plan.
func (s *Store) DeletePlan(roomID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.state.Plans
	var filtered []PlanRecord
	for _, p := range s.state.Plans {
		if p.RoomID != roomID {
			filtered = append(filtered, p)
		}
	}
	s.state.Plans = filtered
	if err := s.save(); err != nil {
		s.state.Plans = previous
		return err
	}
	if s.db != nil {
		_ = s.db.DeletePlan(roomID)
	}
	return nil
}

// DeleteArtifact removes an artifact.
func (s *Store) DeleteArtifact(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Artifacts {
		if s.state.Artifacts[i].ID == id {
			previous := s.state.Artifacts
			s.state.Artifacts = append(s.state.Artifacts[:i], s.state.Artifacts[i+1:]...)
			if err := s.save(); err != nil {
				s.state.Artifacts = previous
				return err
			}
			if s.db != nil {
				_ = s.db.DeleteArtifact(id)
			}
			return nil
		}
	}
	return fmt.Errorf("unknown artifact id %q", id)
}
