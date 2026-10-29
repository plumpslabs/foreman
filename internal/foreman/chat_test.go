package foreman

import "testing"

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
