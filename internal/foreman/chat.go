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

// StartChatRun creates a hidden chat task for the message and starts a run for
// the mentioned agent (or the team's main agent by default).
func (rt *Runtime) StartChatRun(parent context.Context, roomID, content string) (AgentRun, error) {
	task, err := rt.store.CreateChatTask(roomID, content)
	if err != nil {
		return AgentRun{}, err
	}
	return rt.StartTask(parent, task.ID)
}
