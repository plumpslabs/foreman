package foreman

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestMain isolates the user-level global profile so package tests never touch
// the real ~/.foreman/global.json.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "foreman-global-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("FOREMAN_GLOBAL_PATH", filepath.Join(tmp, "global.json"))
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

// openStoreForTest opens a store with an isolated global profile so tests never
// share (or clobber) each other's global collections.
func openStoreForTest(t *testing.T, project string) (*Store, error) {
	t.Helper()
	t.Setenv("FOREMAN_GLOBAL_PATH", filepath.Join(t.TempDir(), "global.json"))
	return OpenStore(project)
}

func TestTeamWithRunningRunsBlocksKick(t *testing.T) {
	state := &WorkspaceState{
		Rooms: []Room{{ID: "r1", TeamIDs: []string{"t1"}}},
		Teams: []Team{{ID: "t1", Name: "Fixes"}},
		Tasks: []Task{{ID: "k1", RoomID: "r1", TeamID: "t1"}},
		Runs:  []AgentRun{{ID: "run1", TaskID: "k1", RoomID: "r1", Status: "running"}},
	}
	if got := teamWithRunningRuns(state, "r1", map[string]bool{"t1": true}); got != "Fixes" {
		t.Fatalf("expected blocked team name, got %q", got)
	}
	state.Runs[0].Status = "completed"
	if got := teamWithRunningRuns(state, "r1", map[string]bool{"t1": true}); got != "" {
		t.Fatalf("expected no block when run finished, got %q", got)
	}
	state.Runs[0].Status = "running"
	if got := teamWithRunningRuns(state, "r1", map[string]bool{"t9": true}); got != "" {
		t.Fatalf("expected no block for unrelated team, got %q", got)
	}
}

func TestDefaultTemplatesIncludeEveryday(t *testing.T) {
	var found bool
	for _, tmpl := range DefaultTemplates() {
		if tmpl.ID != "everyday" {
			continue
		}
		found = true
		if tmpl.MainAgent != "Orion" || len(tmpl.Agents) != 4 {
			t.Fatalf("everyday template malformed: main=%s agents=%d", tmpl.MainAgent, len(tmpl.Agents))
		}
		for _, a := range tmpl.Agents {
			if len(NormalizeToolIDs(a.Tools)) == 0 {
				t.Fatalf("everyday agent %q has no resolvable tools: %#v", a.Name, a.Tools)
			}
		}
	}
	if !found {
		t.Fatal("DefaultTemplates missing 'everyday' template")
	}
}

func TestOpenStoreSeedsAndPersistsWorkspace(t *testing.T) {
	project := t.TempDir()
	store, err := openStoreForTest(t, project)
	if err != nil {
		t.Fatal(err)
	}
	state := store.Snapshot()
	if state.SchemaVersion != SchemaVersion || len(state.Templates) != 5 {
		t.Fatalf("unexpected initial state: schema=%d templates=%d", state.SchemaVersion, len(state.Templates))
	}
	path := filepath.Join(project, ".foreman", "workspace.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("workspace file mode = %o, want 600", info.Mode().Perm())
	}

	team, err := store.ImportTeam("quick-fix", "Project Quick Fix")
	if err != nil {
		t.Fatal(err)
	}
	room, err := store.CreateRoom("Fix crash", "Reproduce and fix crash", []string{team.ID, team.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(room.TeamIDs) != 1 || room.TeamIDs[0] != team.ID {
		t.Fatalf("room team IDs were not deduplicated: %#v", room.TeamIDs)
	}

	reopened, err := openStoreForTest(t, project)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Snapshot()
	if len(got.Teams) != 1 || len(got.Rooms) != 1 || got.Rooms[0].ID != room.ID {
		t.Fatalf("workspace did not recover persisted records: %#v", got)
	}
	if got.Rooms[0].Messages == nil {
		t.Fatal("room message list should be initialized for new rooms")
	}
	message, err := reopened.AppendRoomMessage(room.ID, "user", "Keep this request in the room")
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := openStoreForTest(t, project)
	if err != nil {
		t.Fatal(err)
	}
	if messages := reloaded.Snapshot().Rooms[0].Messages; len(messages) != 1 || messages[0].ID != message.ID {
		t.Fatalf("room chat did not persist across reload: %#v", messages)
	}
}

func TestImportedTeamIsIndependentFromTemplate(t *testing.T) {
	store, err := openStoreForTest(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	team, err := store.ImportTeam("frontend-team", "My Feature Team")
	if err != nil {
		t.Fatal(err)
	}
	team.Agents[0].Name = "Custom Lead"
	team.Agents[1].Role = "Custom role"
	state := store.Snapshot()
	if state.Templates[1].MainAgent != "Nova" || state.Templates[1].Agents[1].Role == "Custom role" {
		t.Fatal("editing imported team unexpectedly changed the source template")
	}
}

func TestCreateRoomRejectsUnknownTeamAndEmptyName(t *testing.T) {
	store, err := openStoreForTest(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRoom("  ", "", nil); err == nil {
		t.Fatal("expected empty room name to fail")
	}
	if _, err := store.CreateRoom("Room", "", []string{"missing"}); err == nil {
		t.Fatal("expected unknown team ID to fail")
	}
	if len(store.Snapshot().Rooms) != 0 {
		t.Fatal("invalid room was persisted")
	}
	team, err := store.ImportTeam("quick-fix", "Valid Team")
	if err != nil {
		t.Fatal(err)
	}
	room, err := store.CreateRoom("Valid Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendRoomMessage(room.ID, "assistant", "pretending to run"); err == nil {
		t.Fatal("assistant message must be rejected until a real runtime exists")
	}
	if _, err := store.AppendRoomMessage("missing", "user", "Hello"); err == nil {
		t.Fatal("unknown room must be rejected")
	}
}

func TestOpenStoreRejectsDifferentProjectAndUnsupportedSchema(t *testing.T) {
	project := t.TempDir()
	store, err := openStoreForTest(t, project)
	if err != nil {
		t.Fatal(err)
	}
	_ = store
	other := t.TempDir()
	if _, err := openStoreForTest(t, other); err != nil {
		t.Fatalf("separate empty project should initialize: %v", err)
	}

	path := filepath.Join(project, ".foreman", "workspace.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state WorkspaceState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	state.SchemaVersion++
	data, _ = json.Marshal(state)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openStoreForTest(t, project); err == nil {
		t.Fatal("expected unsupported schema to fail")
	}
}

func TestAgentLibraryCRUD(t *testing.T) {
	store, err := openStoreForTest(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	initial := len(store.Snapshot().Agents)
	agent, err := store.SaveAgent(Agent{
		Name:          "Sentinel",
		Role:          "Security reviewer",
		Description:   "Reviews changes for security regressions.",
		Instructions:  "Read only. Report findings with evidence.",
		Tools:         []string{"read", "search"},
		RequireReview: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if agent.ID == "" {
		t.Fatal("agent id was not assigned")
	}
	if got := len(store.Snapshot().Agents); got != initial+1 {
		t.Fatalf("agent count = %d, want %d", got, initial+1)
	}
	agent.Role = "Security and privacy reviewer"
	updated, err := store.SaveAgent(agent)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Role != "Security and privacy reviewer" {
		t.Fatalf("agent role was not updated: %q", updated.Role)
	}
	if _, err := store.SaveAgent(Agent{Name: "Sentinel", Role: "duplicate"}); err == nil {
		t.Fatal("duplicate agent names must be rejected")
	}
	if err := store.DeleteAgent(agent.ID); err != nil {
		t.Fatal(err)
	}
	if got := len(store.Snapshot().Agents); got != initial {
		t.Fatalf("agent count after delete = %d, want %d", got, initial)
	}
	if err := store.DeleteAgent("missing"); err == nil {
		t.Fatal("deleting an unknown agent must fail")
	}
}

func TestSkillBundleStoresFilesAndMode(t *testing.T) {
	store, err := openStoreForTest(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	skill, err := store.SaveSkill(Skill{
		Name: "Release checklist",
		Mode: SkillModeActive,
		Files: []SkillFile{
			{Path: "SKILL.md", Content: "# Release checklist\n\nRun every check."},
			{Path: "references/commands.md", Content: "go test ./...\nmake check"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if skill.Mode != SkillModeActive || !skill.Enabled {
		t.Fatalf("active skill mode was not applied: %#v", skill)
	}
	if skill.Instructions == "" {
		t.Fatal("instructions should be derived from SKILL.md")
	}
	if len(skill.Files) != 2 {
		t.Fatalf("files = %d, want 2", len(skill.Files))
	}
	passive, err := store.SaveSkill(Skill{ID: skill.ID, Name: skill.Name, Mode: SkillModePassive,
		Files: []SkillFile{{Path: "SKILL.md", Content: "reference"}}})
	if err != nil {
		t.Fatal(err)
	}
	if passive.Enabled {
		t.Fatal("passive skill must not be marked enabled")
	}
	if _, err := store.SaveSkill(Skill{Name: "escape", Files: []SkillFile{{Path: "../secret.md", Content: "x"}}}); err == nil {
		t.Fatal("paths escaping the skill root must be rejected")
	}
}

func TestSchemaV2MigratesToAgentLibrary(t *testing.T) {
	// Use a fresh global profile so the legacy migration seeds it (and is not
	// overlaid by another test's shared global).
	t.Setenv("FOREMAN_GLOBAL_PATH", filepath.Join(t.TempDir(), "global.json"))
	project := t.TempDir()
	legacy := WorkspaceState{
		SchemaVersion: 2,
		ProjectPath:   project,
		Rooms:         []Room{},
		Teams: []Team{{
			ID: "tm", TemplateID: "quick-fix", Name: "Legacy Team", Purpose: "p", MainAgent: "Legacy Lead",
			Agents: []Agent{{Name: "Legacy Lead", Role: "coordinator"}},
		}},
		Templates: DefaultTemplates(),
		Providers: []ProviderConfig{},
		Tasks:     []Task{},
		Runs:      []AgentRun{},
		Skills:    []Skill{},
		Settings:  normalizeSettings(Settings{}),
	}
	dir := filepath.Join(project, ".foreman")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	store, err := openStoreForTest(t, project)
	if err != nil {
		t.Fatal(err)
	}
	state := store.Snapshot()
	if state.SchemaVersion != SchemaVersion {
		t.Fatalf("schema = %d, want %d", state.SchemaVersion, SchemaVersion)
	}
	if len(state.Agents) != 1 || state.Agents[0].Name != "Legacy Lead" || state.Agents[0].ID == "" {
		t.Fatalf("legacy team agents were not imported into the library: %#v", state.Agents)
	}
}

func TestProviderLifecycleAndCredentialRetention(t *testing.T) {
	store, err := openStoreForTest(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := ProviderConfig{
		ID: "tokenharbor", Name: "TokenHarbor", Protocol: "openai-compatible",
		BaseURL: "https://tokenharbor.ai/v1", Models: []string{"deepseek-v4.1-flash:free", "claude-sonnet-5"},
		DefaultModel: "deepseek-v4.1-flash:free", Enabled: true,
	}
	first, err := store.SaveProvider(config, "thk_test_key")
	if err != nil {
		t.Fatal(err)
	}
	if !first.HasAPIKey {
		t.Fatal("provider should report a saved key")
	}
	// Saving without a new key must keep the stored credential.
	second, err := store.SaveProvider(config, "")
	if err != nil {
		t.Fatal(err)
	}
	if !second.HasAPIKey {
		t.Fatal("blank key save should retain the existing credential")
	}
	secret, err := store.loadProviderCredential(store.Snapshot().ProjectPath, "tokenharbor")
	if err != nil || secret != "thk_test_key" {
		t.Fatalf("credential was not retained: %q %v", secret, err)
	}

	agent, err := store.SaveAgent(Agent{Name: "Scout", Role: "r", ProviderID: "tokenharbor"})
	if err != nil {
		t.Fatal(err)
	}
	if agent.ProviderID != "tokenharbor" {
		t.Fatalf("agent provider = %q", agent.ProviderID)
	}
	if err := store.DeleteProvider("tokenharbor"); err != nil {
		t.Fatal(err)
	}
	state := store.Snapshot()
	if len(state.Providers) != 0 {
		t.Fatalf("providers after delete = %d", len(state.Providers))
	}
	for _, item := range state.Agents {
		if item.ProviderID == "tokenharbor" {
			t.Fatal("agent still references the deleted provider")
		}
	}
	if secret, _ := store.loadProviderCredential(state.ProjectPath, "tokenharbor"); secret != "" {
		t.Fatal("credential was not removed with the provider")
	}
	if err := store.DeleteProvider("missing"); err == nil {
		t.Fatal("deleting an unknown provider must fail")
	}
}

func TestRoomTeamUpdatesAndQuestions(t *testing.T) {
	store, err := openStoreForTest(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	team, err := store.ImportTeam("quick-fix", "Focused Team")
	if err != nil {
		t.Fatal(err)
	}
	room, err := store.CreateRoom("Interactive", "goal", nil)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpdateRoomTeams(room.ID, []string{team.ID, team.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.TeamIDs) != 1 || updated.TeamIDs[0] != team.ID {
		t.Fatalf("room team ids = %#v", updated.TeamIDs)
	}
	if _, err := store.UpdateRoomTeams(room.ID, []string{"missing"}); err == nil {
		t.Fatal("unknown team must be rejected")
	}

	question, err := store.AddRoomQuestion(room.ID, AgentQuestion{Prompt: "Proceed with the refactor?", Options: []string{"Yes", "No"}})
	if err != nil {
		t.Fatal(err)
	}
	if question.ID == "" || len(question.Options) != 2 {
		t.Fatalf("unexpected question: %#v", question)
	}
	answered, err := store.AnswerRoomQuestion(room.ID, question.ID, "Yes")
	if err != nil {
		t.Fatal(err)
	}
	if answered.Answer != "Yes" || answered.AnsweredAt.IsZero() {
		t.Fatalf("answer not stored: %#v", answered)
	}
	state := store.Snapshot()
	if len(state.Rooms[0].Messages) != 1 || state.Rooms[0].Messages[0].Content != "Yes" {
		t.Fatalf("answer was not appended to chat: %#v", state.Rooms[0].Messages)
	}
	if _, err := store.AnswerRoomQuestion(room.ID, question.ID, "No"); err == nil {
		t.Fatal("answering twice must fail")
	}
}

func TestParseAgentQuestions(t *testing.T) {
	message := "I inspected the code.\n\n```question\nProceed with the schema change?\n- Yes, migrate\n- No, stop\n```\n"
	questions := parseAgentQuestions(message)
	if len(questions) != 1 {
		t.Fatalf("questions = %d, want 1", len(questions))
	}
	if questions[0].Prompt != "Proceed with the schema change?" {
		t.Fatalf("prompt = %q", questions[0].Prompt)
	}
	if len(questions[0].Options) != 2 || questions[0].Options[1] != "No, stop" {
		t.Fatalf("options = %#v", questions[0].Options)
	}
}
