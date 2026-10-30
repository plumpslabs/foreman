package foreman

import "testing"

func TestNormalizeSeverity(t *testing.T) {
	cases := map[string]string{
		"":         "info",
		"info":     "info",
		"INFO":     "info",
		"warning":  "warning",
		"warn":     "warning",
		"yellow":   "warning",
		"blocker":  "blocker",
		"critical": "blocker",
		"high":     "blocker",
		"red":      "blocker",
		"bogus":    "info",
	}
	for in, want := range cases {
		if got := normalizeSeverity(in); got != want {
			t.Errorf("normalizeSeverity(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSeverityRankOrder(t *testing.T) {
	if !(SeverityRank("blocker") < SeverityRank("warning") && SeverityRank("warning") < SeverityRank("info")) {
		t.Fatalf("expected blocker < warning < info ordering")
	}
}

func TestApprovalSeverityPersistedAndSorted(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer st.Close()

	room, err := st.CreateRoom("Severity Room", "", nil)
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}

	// Insert in "wrong" order to prove sorting puts blockers first.
	if _, err := st.CreateApproval(ApprovalRecord{RoomID: room.ID, Type: "question", Description: "info one", Severity: "info"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateApproval(ApprovalRecord{RoomID: room.ID, Type: "review", Description: "blocker one", Severity: "blocker"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateApproval(ApprovalRecord{RoomID: room.ID, Type: "question", Description: "warning one", Severity: "warn"}); err != nil {
		t.Fatal(err)
	}

	list, err := st.ListApprovals(room.ID)
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 approvals, got %d", len(list))
	}
	if list[0].Severity != "blocker" || list[2].Severity != "info" {
		t.Fatalf("approvals not sorted by severity: %+v", list)
	}
	// "warn" must be normalized to "warning" on the way in.
	if list[1].Severity != "warning" {
		t.Fatalf("expected normalized severity warning, got %q", list[1].Severity)
	}
}
