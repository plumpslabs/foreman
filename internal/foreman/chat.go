package foreman

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// parseChatMention resolves a leading "@agent" mention in a chat message against
// the invited team's roster. Names may contain spaces, so the longest matching
// agent name wins. Returns the agent name ("" if none) and the remaining prompt.
func parseChatMention(content string, team *Team) (agent string, rest string) {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "@") || team == nil {
		return "", content
	}
	best := ""
	bestLen := 0
	for _, a := range team.Agents {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			continue
		}
		if content == "@"+name {
			best = name
			bestLen = len(name)
			break
		}
		if strings.HasPrefix(content, "@"+name+" ") && len(name) > bestLen {
			best = name
			bestLen = len(name)
		}
	}
	if best == "" {
		return "", content
	}
	rest = strings.TrimSpace(content[bestLen+1:])
	return best, rest
}

// chatKind routes a user message either to the full agent loop or to the
// lightweight local reply path.
type chatKind int

const (
	chatAgent chatKind = iota
	chatLight
)

// actionableIntentWords are short imperative words that signal real work;
// messages containing them always go to the agent loop. This is deliberately a
// small stoplist: everything else that is short and purely alphabetic (small
// talk in any language: "halo", "hi", "oke", "selamat pagi", …) is treated as
// a light chat message, so we never need to enumerate greetings.
var actionableIntentWords = map[string]bool{
	"buat": true, "buatkan": true, "buatlah": true, "bikin": true,
	"tolong": true, "perbaiki": true, "bantu": true, "tulis": true,
	"cek": true, "cari": true, "ganti": true, "hapus": true,
	"tambah": true, "analisis": true, "ringkas": true, "jelaskan": true,
	"review": true, "test": true, "jalankan": true, "running": true,
	"pause": true, "stop": true, "lanjut": true, "deploy": true,
	"commit": true, "push": true, "pull": true, "install": true,
	"fix": true, "refactor": true, "rebase": true, "merge": true,
}

// classifyChatMessage decides whether a message is light small talk (answered
// locally, no agent run) or a real request (agent loop). The rule is purely
// structural: after stripping a leading @mention, a message of at most 4 short
// alphabetic words that contains no actionable intent word is light.
func classifyChatMessage(content string) chatKind {
	t := strings.ToLower(strings.TrimSpace(content))
	t = strings.Trim(t, "!?.,;: ")
	if strings.HasPrefix(t, "@") {
		if i := strings.IndexByte(t, ' '); i >= 0 {
			t = strings.TrimSpace(t[i+1:])
		} else {
			t = ""
		}
	}
	if t == "" {
		return chatAgent
	}
	words := strings.Fields(t)
	if len(words) == 0 || len(words) > 4 {
		return chatAgent
	}
	for _, w := range words {
		w = strings.Trim(w, "!?.,;:")
		if w == "" {
			continue
		}
		if actionableIntentWords[w] {
			return chatAgent
		}
		if len([]rune(w)) > 12 {
			return chatAgent
		}
		for _, r := range w {
			if !unicode.IsLetter(r) {
				return chatAgent
			}
		}
	}
	return chatLight
}

// resolveChatMentions scans content for leading/inline @agent and #team mentions across all invited teams.
func resolveChatMentions(content string, teams []*Team) (team *Team, agentName string, prompt string) {
	if len(teams) == 0 {
		return nil, "", content
	}

	trimmed := strings.TrimSpace(content)
	var matchedTeams []*Team
	var matchedAgents []string

	// Greedily consume leading mentions (@agent or #team)
	for {
		trimmed = strings.TrimSpace(trimmed)
		if strings.HasPrefix(trimmed, "#") {
			target := strings.TrimPrefix(trimmed, "#")
			var bestTeam *Team
			bestLen := 0
			for _, tm := range teams {
				name := strings.TrimSpace(tm.Name)
				if name != "" {
					if target == name || strings.HasPrefix(target, name+" ") {
						if len(name) > bestLen {
							bestTeam = tm
							bestLen = len(name)
						}
					}
				}
				id := strings.TrimSpace(tm.ID)
				if id != "" {
					if target == id || strings.HasPrefix(target, id+" ") {
						if len(id) > bestLen {
							bestTeam = tm
							bestLen = len(id)
						}
					}
				}
			}
			if bestTeam != nil {
				matchedTeams = append(matchedTeams, bestTeam)
				trimmed = strings.TrimSpace(target[bestLen:])
				continue
			}
		}

		if strings.HasPrefix(trimmed, "@") {
			target := strings.TrimPrefix(trimmed, "@")
			bestAgent := ""
			var bestTeam *Team
			bestLen := 0
			for _, tm := range teams {
				for _, a := range tm.Agents {
					name := strings.TrimSpace(a.Name)
					if name == "" {
						continue
					}
					if target == name || strings.HasPrefix(target, name+" ") {
						if len(name) > bestLen {
							bestAgent = name
							bestTeam = tm
							bestLen = len(name)
						}
					}
				}
			}
			if bestAgent != "" {
				matchedAgents = append(matchedAgents, bestAgent)
				if bestTeam != nil {
					matchedTeams = append(matchedTeams, bestTeam)
				}
				trimmed = strings.TrimSpace(target[bestLen:])
				continue
			}
		}

		break
	}

	cleanPrompt := strings.TrimSpace(trimmed)
	if cleanPrompt == "" {
		cleanPrompt = strings.TrimSpace(content)
	}

	// 1. Pick target team
	if len(matchedTeams) > 0 {
		team = matchedTeams[0]
	} else {
		team = teams[0]
	}

	// 2. Pick target agent
	if len(matchedAgents) == 1 {
		// Single agent mentioned directly
		agentName = matchedAgents[0]
		for _, tm := range teams {
			for _, a := range tm.Agents {
				if a.Name == agentName {
					team = tm
					break
				}
			}
		}
		prompt = cleanPrompt
		return team, agentName, prompt
	}

	if len(matchedAgents) > 1 {
		// Multi-agent mention: check if any is a MainAgent / Coordinator
		for _, ag := range matchedAgents {
			for _, tm := range teams {
				if tm.MainAgent == ag {
					agentName = ag
					team = tm
					break
				}
			}
			if agentName != "" {
				break
			}
		}
		// If neither is MainAgent, let the team coordinator handle orchestration
		if agentName == "" {
			agentName = team.MainAgent
		}

		tagList := make([]string, len(matchedAgents))
		for i, a := range matchedAgents {
			tagList[i] = "@" + a
		}

		guidance := fmt.Sprintf("[Multi-Agent Orchestration: User tagged %s. As team coordinator (%s), decompose this request, assign tasks to the tagged specialist agents, and orchestrate execution.]\n\n%s",
			strings.Join(tagList, ", "), agentName, cleanPrompt)

		return team, agentName, guidance
	}

	// No agents mentioned specifically; if a #team was mentioned, use its MainAgent
	agentName = team.MainAgent
	prompt = cleanPrompt
	return team, agentName, prompt
}

// RoomMainAgentName returns the main agent of the room's first invited team.
func (s *Store) RoomMainAgentName(roomID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, room := range s.state.Rooms {
		if room.ID != roomID || len(room.TeamIDs) == 0 {
			continue
		}
		for _, team := range s.state.Teams {
			if team.ID == room.TeamIDs[0] {
				return team.MainAgent
			}
		}
		break
	}
	return ""
}

// GetOrCreateChatThread returns the room's hidden chat task, creating it on
// first use. Later messages reuse the same thread so the conversation keeps its
// memory (the persisted OpenHands conversation resumes) instead of starting
// fresh per message. The mentioned agent is resolved across ALL invited teams.
func (s *Store) GetOrCreateChatThread(roomID, content string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var room *Room
	for i := range s.state.Rooms {
		if s.state.Rooms[i].ID == roomID {
			room = &s.state.Rooms[i]
			break
		}
	}
	if room == nil {
		return Task{}, errors.New("unknown room")
	}
	if len(room.TeamIDs) == 0 {
		return Task{}, errors.New("invite a team to this room before chatting with an agent")
	}

	// Resolve mentions (@agent and #team) across all invited teams;
	// fall back to the first team's main agent.
	var invitedTeams []*Team
	for _, teamID := range room.TeamIDs {
		for i := range s.state.Teams {
			if s.state.Teams[i].ID == teamID {
				invitedTeams = append(invitedTeams, &s.state.Teams[i])
				break
			}
		}
	}
	if len(invitedTeams) == 0 {
		return Task{}, errors.New("the invited team no longer exists")
	}

	team, agentName, prompt := resolveChatMentions(content, invitedTeams)

	// Reuse the existing hidden thread for THIS agent when it is idle — a fresh
	// "todo" thread, or one paused/continued from a prior message — so a room
	// keeps a single conversation per agent instead of spawning duplicate
	// threads. A thread that is actively running is guarded separately in
	// StartChatRun; review/done threads are not reused.
	for i := range s.state.Tasks {
		t := &s.state.Tasks[i]
		if t.RoomID == roomID && t.Hidden && t.AgentName == agentName &&
			(t.Status == "todo" || t.Status == "in_progress" || t.Status == "paused") {
			previous := s.state.Tasks[i].Description
			t.Description = prompt
			t.UpdatedAt = time.Now().UTC()
			if err := s.save(); err != nil {
				s.state.Tasks[i].Description = previous
				return Task{}, err
			}
			return *t, nil
		}
	}

	title := prompt
	if len([]rune(title)) > 80 {
		title = string([]rune(title)[:80]) + "…"
	}
	task := Task{
		ID:           uuid.NewString(),
		RoomID:       roomID,
		TeamID:       team.ID,
		AgentName:    agentName,
		Title:        title,
		Description:  prompt,
		Status:       "todo",
		Priority:     "normal",
		Effort:       "medium",
		Dependencies: []string{},
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
		Hidden:       true,
	}
	previous := s.state.Tasks
	s.state.Tasks = append([]Task{task}, previous...)
	if err := s.save(); err != nil {
		s.state.Tasks = previous
		return Task{}, err
	}
	return task, nil
}

// AppendRoomNotice records a lightweight system/agent action notice in the
// room chat so users see progress (task created, delegation, errors) without
// watching the Kanban or activity log. Notices render as compact chips.
func (s *Store) AppendRoomNotice(roomID, agentName, activityType, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	for i := range s.state.Rooms {
		if s.state.Rooms[i].ID != roomID {
			continue
		}
		if len(content) > 2000 {
			content = content[:2000]
		}
		message := NewChatMessage("system", content)
		message.AgentName = agentName
		message.ActivityType = activityType
		previous := s.state.Rooms[i].Messages
		s.state.Rooms[i].Messages = append(previous, message)
		if err := s.save(); err != nil {
			s.state.Rooms[i].Messages = previous
			return err
		}
		return nil
	}
	return fmt.Errorf("unknown room id %q", roomID)
}

// AppendAgentReply records an agent's answer in the room chat.
func (s *Store) AppendAgentReply(roomID, agentName, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	for i := range s.state.Rooms {
		if s.state.Rooms[i].ID != roomID {
			continue
		}
		if len(content) > 16000 {
			content = content[:16000]
		}
		message := NewChatMessage("agent", content)
		message.AgentName = agentName
		previous := s.state.Rooms[i].Messages
		s.state.Rooms[i].Messages = append(previous, message)
		if err := s.save(); err != nil {
			s.state.Rooms[i].Messages = previous
			return err
		}
		return nil
	}
	return fmt.Errorf("unknown room id %q", roomID)
}

// StartChatRun reuses the room's chat thread (for memory) and starts a run for
// the mentioned agent (or the team's main agent by default). If a chat run is
// already active for the room, it refuses so two prompts cannot interleave.
func (rt *Runtime) StartChatRun(parent context.Context, roomID, content string) (AgentRun, error) {
	task, err := rt.store.GetOrCreateChatThread(roomID, content)
	if err != nil {
		return AgentRun{}, err
	}
	if rt.store.HasRunningRunForTask(task.ID) {
		return AgentRun{}, errors.New("agent is still busy with the previous message — wait for it to finish, or stop the run first")
	}
	return rt.StartTask(parent, task.ID)
}
