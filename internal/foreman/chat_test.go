package foreman

import (
	"strings"
	"testing"
)

func TestClassifyChatMessage(t *testing.T) {
	cases := []struct {
		content string
		want    chatKind
	}{
		{"halo", chatLight},
		{"Hello!", chatLight},
		{"selamat pagi", chatLight},
		{"oke", chatLight},
		{"siap", chatLight},
		{"@Atlas halo", chatLight},
		{"@Quick Coordinator halo", chatLight},
		{"halo kak, tolong cek error di app.js", chatAgent},
		{"buatkan landing page", chatAgent},
		{"test", chatAgent},
		{"deploy", chatAgent},
		{"rebase ke main", chatAgent},
		{"", chatAgent},
		{"@Quick Coordinator", chatLight}, // mention-only → generic local reply
	}
	for _, c := range cases {
		if got := classifyChatMessage(c.content); got != c.want {
			t.Fatalf("classifyChatMessage(%q) = %v, want %v", c.content, got, c.want)
		}
	}
}

func TestChatThreadMultiTeamMentionAndReuse(t *testing.T) {
	store, err := openStoreForTest(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	quick, err := store.ImportTeam("quick-fix", "Quick")
	if err != nil {
		t.Fatal(err)
	}
	everyday, err := store.ImportTeam("everyday", "Everyday")
	if err != nil {
		t.Fatal(err)
	}
	room, err := store.CreateRoom("R", "g", []string{quick.ID, everyday.ID})
	if err != nil {
		t.Fatal(err)
	}

	// Mention resolves across teams: Vera belongs to the everyday template.
	task1, err := store.GetOrCreateChatThread(room.ID, "@Vera review diffs")
	if err != nil {
		t.Fatal(err)
	}
	if task1.AgentName != "Vera" || task1.TeamID != everyday.ID {
		t.Fatalf("mention not resolved across teams: %#v", task1)
	}

	// Later messages to the same agent reuse the same hidden thread so memory
	// persists (per-agent history).
	task2, err := store.GetOrCreateChatThread(room.ID, "@Vera lanjutkan reviewnya")
	if err != nil {
		t.Fatal(err)
	}
	if task2.ID != task1.ID {
		t.Fatalf("expected thread reuse (%s != %s)", task1.ID, task2.ID)
	}

	// A different agent gets its own thread (per-agent history).
	task3, err := store.GetOrCreateChatThread(room.ID, "@Ferro cek error")
	if err != nil {
		t.Fatal(err)
	}
	if task3.ID == task1.ID || task3.AgentName != "Ferro" {
		t.Fatalf("expected separate thread for another agent: %#v", task3)
	}

	hidden := 0
	for _, tk := range store.Snapshot().Tasks {
		if tk.Hidden {
			hidden++
		}
	}
	if hidden != 2 {
		t.Fatalf("expected two hidden chat threads (one per agent), got %d", hidden)
	}
}

func TestParseChatMentionLongestMatchWins(t *testing.T) {
	team := &Team{
		MainAgent: "Atlas",
		Agents: []Agent{
			{Name: "Quick Coordinator"},
			{Name: "Implementer"},
			{Name: "Atlas"},
		},
	}
	cases := []struct {
		content string
		agent   string
		rest    string
	}{
		{"@Quick Coordinator test siapa kamu", "Quick Coordinator", "test siapa kamu"},
		{"@Atlas halo", "Atlas", "halo"},
		{"hello without mention", "", "hello without mention"},
		{"@nobody hi", "", "@nobody hi"},
		{"@Implementer", "Implementer", ""},
	}
	for _, c := range cases {
		agent, rest := parseChatMention(c.content, team)
		if agent != c.agent || rest != c.rest {
			t.Fatalf("parseChatMention(%q) = (%q, %q), want (%q, %q)", c.content, agent, rest, c.agent, c.rest)
		}
	}
}

func TestParseChatMentionNilTeam(t *testing.T) {
	if agent, rest := parseChatMention("@Atlas hi", nil); agent != "" || rest != "@Atlas hi" {
		t.Fatalf("expected no mention for nil team, got (%q, %q)", agent, rest)
	}
}

func TestResolveChatMentions(t *testing.T) {
	team1 := &Team{
		ID:        "everyday",
		Name:      "Everyday Ops",
		MainAgent: "Orion",
		Agents: []Agent{
			{Name: "Orion", Role: "Main agent"},
			{Name: "Ferro", Role: "Code debugger"},
			{Name: "Vera", Role: "Code reviewer"},
		},
	}
	team2 := &Team{
		ID:        "frontend",
		Name:      "Frontend Team",
		MainAgent: "Iris",
		Agents: []Agent{
			{Name: "Iris", Role: "UI lead"},
			{Name: "Pixel", Role: "CSS specialist"},
		},
	}
	teams := []*Team{team1, team2}

	// 1. Single agent mention
	tm, ag, prompt := resolveChatMentions("@Ferro tolong perbaiki test", teams)
	if tm.ID != "everyday" || ag != "Ferro" || prompt != "tolong perbaiki test" {
		t.Fatalf("unexpected single agent mention: tm=%v ag=%s prompt=%s", tm.ID, ag, prompt)
	}

	// 2. Team mention via #
	tm, ag, prompt = resolveChatMentions("#Frontend Team buatkan navbar", teams)
	if tm.ID != "frontend" || ag != "Iris" || prompt != "buatkan navbar" {
		t.Fatalf("unexpected team mention: tm=%v ag=%s prompt=%s", tm.ID, ag, prompt)
	}

	// 3. Multi-agent mention with MainAgent
	tm, ag, prompt = resolveChatMentions("@Orion @Ferro tolong investigasi bug", teams)
	if tm.ID != "everyday" || ag != "Orion" || !strings.Contains(prompt, "tolong investigasi bug") {
		t.Fatalf("unexpected multi-agent mention: tm=%v ag=%s prompt=%s", tm.ID, ag, prompt)
	}

	// 4. Multi-agent mention with only specialists
	tm, ag, prompt = resolveChatMentions("@Ferro @Vera tolong fix dan review", teams)
	if tm.ID != "everyday" || ag != "Orion" || !strings.Contains(prompt, "tolong fix dan review") {
		t.Fatalf("unexpected specialists mention: tm=%v ag=%s prompt=%s", tm.ID, ag, prompt)
	}

	// 5. No mention (defaults to Lead Team's MainAgent)
	tm, ag, prompt = resolveChatMentions("halo apa kabar", teams)
	if tm.ID != "everyday" || ag != "Orion" || prompt != "halo apa kabar" {
		t.Fatalf("unexpected no-mention: tm=%v ag=%s prompt=%s", tm.ID, ag, prompt)
	}
}
