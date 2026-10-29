package foreman

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenStoreSeedsAndPersistsWorkspace(t *testing.T) {
	project := t.TempDir()
	store, err := OpenStore(project)
	if err != nil {
		t.Fatal(err)
	}
	state := store.Snapshot()
	if state.SchemaVersion != SchemaVersion || len(state.Templates) != 4 {
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

	reopened, err := OpenStore(project)
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
	reloaded, err := OpenStore(project)
	if err != nil {
		t.Fatal(err)
	}
	if messages := reloaded.Snapshot().Rooms[0].Messages; len(messages) != 1 || messages[0].ID != message.ID {
		t.Fatalf("room chat did not persist across reload: %#v", messages)
	}
}

func TestImportedTeamIsIndependentFromTemplate(t *testing.T) {
	store, err := OpenStore(t.TempDir())
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
	store, err := OpenStore(t.TempDir())
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
	store, err := OpenStore(project)
	if err != nil {
		t.Fatal(err)
	}
	_ = store
	other := t.TempDir()
	if _, err := OpenStore(other); err != nil {
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
	if _, err := OpenStore(project); err == nil {
		t.Fatal("expected unsupported schema to fail")
	}
}
