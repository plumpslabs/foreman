package foreman

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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

// CreateChatTask records a room chat message as a hidden task so a run can be
// started for it. The task is excluded from the Kanban and task progress views.
func (s *Store) CreateChatTask(roomID, content string) (Task, error) {
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
	var team *Team
	for i := range s.state.Teams {
		if s.state.Teams[i].ID == room.TeamIDs[0] {
			team = &s.state.Teams[i]
			break
		}
	}
	if team == nil {
		return Task{}, errors.New("the invited team no longer exists")
	}

	agentName, prompt := parseChatMention(content, team)
	if agentName == "" {
		agentName = team.MainAgent
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		prompt = strings.TrimSpace(content)
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

// AppendAgentReply records an agent's answer in the room chat.
func (s *Store) AppendAgentReply(roomID, content string) error {
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

// StartChatRun creates a hidden chat task for the message and starts a run for
// the mentioned agent (or the team's main agent by default).
func (rt *Runtime) StartChatRun(parent context.Context, roomID, content string) (AgentRun, error) {
	task, err := rt.store.CreateChatTask(roomID, content)
	if err != nil {
		return AgentRun{}, err
	}
	return rt.StartTask(parent, task.ID)
}
