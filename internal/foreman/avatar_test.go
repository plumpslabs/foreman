package foreman

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FOREMAN_GLOBAL_PATH", filepath.Join(dir, "global.json"))
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestAssignAvatarFillsMissingOnly(t *testing.T) {
	a := &Agent{}
	if !assignAvatar(a) {
		t.Fatal("expected assignAvatar to report a change for an empty agent")
	}
	if a.AvatarSeed == "" || a.AvatarStyle == "" {
		t.Fatalf("expected seed and style to be set: %+v", a)
	}
	seed, style := a.AvatarSeed, a.AvatarStyle
	if assignAvatar(a) {
		t.Fatal("assignAvatar must not change an agent that already has an avatar")
	}
	if a.AvatarSeed != seed || a.AvatarStyle != style {
		t.Fatal("existing avatar seed/style must be preserved")
	}
}

func TestStyleForSeedIsStable(t *testing.T) {
	if styleForSeed("abc") != styleForSeed("abc") {
		t.Fatal("styleForSeed must be deterministic")
	}
	if styleForSeed("") != avatarStyles[0] {
		t.Fatal("empty seed should fall back to the first style")
	}
}

func TestAvatarFileNameSanitizes(t *testing.T) {
	name := avatarFileName("../../etc/passwd", "bottts", "seed with spaces")
	if strings.Contains(name, "/") || strings.Contains(name, " ") || strings.Contains(name, "..") {
		t.Fatalf("avatar file name was not sanitized: %q", name)
	}
	if !strings.HasSuffix(name, ".svg") {
		t.Fatalf("avatar file name should end in .svg: %q", name)
	}
}

func TestSaveAgentAssignsAvatar(t *testing.T) {
	st := testStore(t)
	created, err := st.SaveAgent(Agent{Name: "Nova", RoleType: "specialist"})
	if err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	if created.AvatarSeed == "" || created.AvatarStyle == "" {
		t.Fatalf("new agent should get an avatar: %+v", created)
	}
	// The role title is derived from the role type when omitted.
	if created.Role != "Specialist" {
		t.Fatalf("role = %q, want Specialist", created.Role)
	}
	// Re-saving with the returned value preserves the seed.
	created.Description = "change"
	resaved, err := st.SaveAgent(created)
	if err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if resaved.AvatarSeed != created.AvatarSeed {
		t.Fatal("re-saving an agent must not re-roll its avatar")
	}
}

func TestAvatarSVGFetchesAndCaches(t *testing.T) {
	const svg = `<svg xmlns="http://www.w3.org/2000/svg"></svg>`
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte(svg))
	}))
	defer srv.Close()

	prev := diceBearBaseURL
	diceBearBaseURL = srv.URL
	t.Cleanup(func() { diceBearBaseURL = prev })

	st := testStore(t)
	agent, err := st.SaveAgent(Agent{Name: "Nova", Role: "Specialist"})
	if err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	got, err := st.AvatarSVG(agent.ID)
	if err != nil {
		t.Fatalf("AvatarSVG: %v", err)
	}
	if string(got) != svg {
		t.Fatalf("AvatarSVG = %q, want the fetched svg", string(got))
	}
	if hits != 1 {
		t.Fatalf("expected one fetch, got %d", hits)
	}
	// The second read is served from the on-disk cache, not the network.
	got, err = st.AvatarSVG(agent.ID)
	if err != nil {
		t.Fatalf("AvatarSVG (cached): %v", err)
	}
	if hits != 1 {
		t.Fatalf("expected the cached read to skip the network, hits=%d", hits)
	}
	// The cache file exists on disk.
	entries, err := os.ReadDir(st.avatarDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("expected a cached avatar file, err=%v entries=%d", err, len(entries))
	}
}

func TestAvatarSVGUnknownAgent(t *testing.T) {
	st := testStore(t)
	data, err := st.AvatarSVG("does-not-exist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data != nil {
		t.Fatalf("expected no avatar for an unknown agent, got %q", data)
	}
}

func TestShuffleAgentAvatarChangesSeed(t *testing.T) {
	st := testStore(t)
	agent, err := st.SaveAgent(Agent{Name: "Nova", Role: "Specialist"})
	if err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	shuffled, err := st.ShuffleAgentAvatar(agent.ID)
	if err != nil {
		t.Fatalf("ShuffleAgentAvatar: %v", err)
	}
	if shuffled.AvatarSeed == agent.AvatarSeed {
		t.Fatal("shuffle should change the avatar seed")
	}
	if _, _, ok := st.FindAgentAvatar(agent.ID); !ok {
		t.Fatal("shuffled agent should still be resolvable")
	}
}

func TestEnsureAgentAvatarsBackfillsTemplates(t *testing.T) {
	st := testStore(t)
	// Default templates ship with agents; after store open they must all have
	// avatars and resolvable IDs.
	found := false
	for _, tpl := range st.state.Templates {
		for _, a := range tpl.Agents {
			if a.AvatarSeed == "" || a.AvatarStyle == "" || a.ID == "" {
				t.Fatalf("template agent %q was not backfilled: %+v", a.Name, a)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("expected default templates to contain agents")
	}
}
