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
	mu    sync.RWMutex
	path  string
	state WorkspaceState
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
			if err := s.save(); err != nil {
				return nil, err
			}
		}
		return s, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("open Foreman workspace: %w", err)
	}

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
	if err := s.save(); err != nil {
		return nil, err
	}
	return s, nil
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
	s.state.Settings = normalizeSettings(s.state.Settings)
	return migrated, nil
}

func seedAgents() []Agent {
	agents := DefaultAgents()
	for i := range agents {
		if agents[i].ID == "" {
			agents[i].ID = uuid.NewString()
		}
	}
	return agents
}

func seedSkills() []Skill {
	skills := DefaultSkills()
	for i := range skills {
		skills[i].ID = uuid.NewString()
	}
	return skills
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
			return fmt.Errorf("unknown skill %q", ref)
		}
		if !seen[id] {
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

func (s *Store) Snapshot() WorkspaceState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, _ := json.Marshal(s.state)
	var copy WorkspaceState
	_ = json.Unmarshal(data, &copy)
	return copy
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
		message := NewChatMessage(role, content)
		previous := s.state.Rooms[i].Messages
		s.state.Rooms[i].Messages = append(previous, message)
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
	previous := s.state.Teams
	s.state.Teams = append([]Team{t}, previous...)
	if err := s.save(); err != nil {
		s.state.Teams = previous
		return Team{}, err
	}
	return t, nil
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
	if v.MaxConcurrentRuns <= 0 {
		v.MaxConcurrentRuns = 1
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
	if v.MaxExecutionSeconds <= 0 {
		v.MaxExecutionSeconds = 600
	}
	if v.MaxExecutionSeconds > 3600 {
		v.MaxExecutionSeconds = 3600
	}
	if v.BudgetUSD < 0 || math.IsNaN(v.BudgetUSD) || math.IsInf(v.BudgetUSD, 0) {
		v.BudgetUSD = 0
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
	if agent.Name == "" || agent.Role == "" {
		return Agent{}, errors.New("agent name and role are required")
	}
	if len(agent.Name) > 80 || len(agent.Role) > 160 || len(agent.Instructions) > 16000 {
		return Agent{}, errors.New("agent name, role, or instructions exceed the allowed length")
	}
	if agent.ID == "" {
		agent.ID = uuid.NewString()
	}
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
	if settings.MaxConcurrentRuns < 1 || settings.MaxConcurrentRuns > 8 || settings.MaxWorkersPerTask < 1 || settings.MaxWorkersPerTask > 8 || settings.MaxDelegationDepth < 0 || settings.MaxDelegationDepth > 5 || settings.MaxRetries < 0 || settings.MaxRetries > 5 || settings.MaxExecutionSeconds < 10 || settings.MaxExecutionSeconds > 3600 || settings.BudgetUSD < 0 || math.IsNaN(settings.BudgetUSD) || math.IsInf(settings.BudgetUSD, 0) {
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
	if task.Priority == "" {
		task.Priority = "normal"
	}
	if task.Priority != "low" && task.Priority != "normal" && task.Priority != "high" && task.Priority != "urgent" {
		return Task{}, errors.New("priority must be low, normal, high, or urgent")
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

func (s *Store) UpdateTaskStatus(taskID, status string, approved bool) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	validStatus := map[string]bool{"todo": true, "blocked": true, "in_progress": true, "review": true, "done": true, "cancelled": true}
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
	if task.Status != "todo" && task.Status != "blocked" {
		return Task{}, Room{}, nil, ProviderConfig{}, "", Settings{}, errors.New("only todo or dependency-ready tasks can be run")
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
		if task.Status != "todo" {
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
				s.state.Tasks[taskIndex].Status = "review"
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
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return fmt.Errorf("create Foreman data directory: %w", err)
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Foreman workspace: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".workspace-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary workspace file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write workspace: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync workspace: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close workspace: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("persist workspace: %w", err)
	}
	return nil
}
