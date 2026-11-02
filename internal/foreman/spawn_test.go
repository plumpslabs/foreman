package foreman

import "testing"

// TestRoomAgentCanSpawnPolicy covers the data path behind spawn_agent: the
// default ("") and the legacy "limited" both allow direct spawning, while an
// agent explicitly set to "request" is the only one that must route through
// request_spawn. Membership is room-scoped: an agent from a team that is not
// invited to the room must not be reported as spannable.
func TestRoomAgentCanSpawnPolicy(t *testing.T) {
	st := testStore(t)
	team, err := st.ImportTeam("quick-fix", "Quick Fix")
	if err != nil {
		t.Fatal(err)
	}
	room, err := st.CreateRoom("Spawn Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}

	// Grant one agent explicit "request" authority through the library profile
	// (SaveAgent propagates to every team roster). Everyone else keeps the
	// default, which is open by default.
	lib := libraryAgent(t, st, "Implementer")
	lib.CanSpawn = "request"
	if _, err := st.SaveAgent(lib); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	if got := st.RoomAgentCanSpawn(room.ID, "Implementer"); got != "request" {
		t.Fatalf("request agent authority = %q, want request", got)
	}

	// A default-profile agent (no explicit authority) resolves to "".
	var defaultName string
	for _, a := range st.Snapshot().Teams[0].Agents {
		if a.Name != "Implementer" {
			defaultName = a.Name
			break
		}
	}
	if defaultName == "" {
		t.Fatal("could not find a default-profile agent on the team")
	}
	if got := st.RoomAgentCanSpawn(room.ID, defaultName); got != "" {
		t.Fatalf("default agent authority = %q, want \"\" (open by default)", got)
	}

	// An agent from a team not invited to the room is unknown in that room.
	otherTeam, err := st.ImportTeam("everyday", "Everyday Ops")
	if err != nil {
		t.Fatal(err)
	}
	if got := st.RoomAgentCanSpawn(room.ID, otherTeam.Agents[0].Name); got != "" {
		t.Fatalf("uninvited agent authority = %q, want \"\"", got)
	}
}
