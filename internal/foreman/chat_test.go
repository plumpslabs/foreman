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
