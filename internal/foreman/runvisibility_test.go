package foreman

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
)

// The ```question fence is parsed into a question card before the reply is
// posted; leaving it in the text showed the question twice in the bubble.
func TestStripQuestionBlocks(t *testing.T) {
	msg := "Silakan pilih salah satu.\n```question\nWarna mana?\n- Merah\n- Biru\n```\nTerima kasih."
	got := stripQuestionBlocks(msg)
	if strings.Contains(got, "```question") || strings.Contains(got, "Merah") {
		t.Fatalf("question fence survived stripping: %q", got)
	}
	if !strings.Contains(got, "Silakan pilih") || !strings.Contains(got, "Terima kasih") {
		t.Fatalf("surrounding text was lost: %q", got)
	}
	// An unterminated fence must not leak the raw block either.
	got = stripQuestionBlocks("before\n```question\nbroken")
	if strings.Contains(got, "```question") {
		t.Fatalf("unterminated fence leaked: %q", got)
	}
	// Messages without a question pass through untouched.
	if got := stripQuestionBlocks("plain report"); got != "plain report" {
		t.Fatalf("plain message changed: %q", got)
	}
}

// nil Options serialized as "options": null and crashed the dashboard's
// question card (which maps over options with no null guard).
func TestQuestionOptionsNeverNull(t *testing.T) {
	questions := parseAgentQuestions("```question\nLanjut atau stop?\n```")
	if len(questions) != 1 {
		t.Fatalf("parsed %d questions, want 1", len(questions))
	}
	if questions[0].Options == nil {
		t.Fatal("Options is nil — renders as \"options\": null and crashes the card")
	}
	if len(questions[0].Options) != 0 {
		t.Fatalf("Options = %v, want empty", questions[0].Options)
	}
}

// A hidden chat run must not post the placeholder "Agent run finished." as a
// bubble, and must not echo the user's own message back as a "started working
// on" chip (the title of a hidden run is the prompt itself).
func TestHiddenRunPostsNoPlaceholderOrStartedChip(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ensureFakeProvider(t, st)
	team, err := st.ImportTeam("quick-fix", "Quick Fix")
	if err != nil {
		t.Fatal(err)
	}
	room, err := st.CreateRoom("Hidden Run Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	task, err := st.GetOrCreateChatThread(room.ID, "halo, coba balas kosong")
	if err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(st)
	rt.SetEngine(&fakeEngine{results: []EngineResult{{Status: "completed", Message: ""}}})
	defer rt.stopWatchdog()
	if _, err := rt.StartTask(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	waitRunFinished(t, rt, task.ID)

	for _, m := range st.Snapshot().Rooms[0].Messages {
		if m.ActivityType == "run.started" {
			t.Errorf("hidden run posted a run.started chip that echoes the user's own message: %q", m.Content)
		}
		if m.Role == "agent" && strings.Contains(m.Content, "Agent run finished.") {
			t.Errorf("placeholder result posted as a bubble: %q", m.Content)
		}
	}
}

// A question block in the reply must become exactly one question card — not a
// card plus a raw code block in the bubble.
func TestHiddenRunQuestionRendersOnce(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ensureFakeProvider(t, st)
	team, err := st.ImportTeam("quick-fix", "Quick Fix")
	if err != nil {
		t.Fatal(err)
	}
	room, err := st.CreateRoom("Question Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	task, err := st.GetOrCreateChatThread(room.ID, "minta pilihan")
	if err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(st)
	rt.SetEngine(&fakeEngine{results: []EngineResult{{
		Status:  "completed",
		Message: "Silakan pilih.\n```question\nWarna mana?\n- Merah\n- Biru\n```\nTerima kasih.",
	}}})
	defer rt.stopWatchdog()
	if _, err := rt.StartTask(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	waitRunFinished(t, rt, task.ID)

	snap := st.Snapshot()
	if len(snap.Rooms[0].Questions) != 1 {
		t.Fatalf("parsed questions = %d, want 1", len(snap.Rooms[0].Questions))
	}
	q := snap.Rooms[0].Questions[0]
	if len(q.Options) != 2 {
		t.Fatalf("options = %v, want both choices", q.Options)
	}
	if q.AgentName == "" {
		t.Error("question lost the asking agent — the answer cannot be routed back")
	}
	for _, m := range snap.Rooms[0].Messages {
		if m.Role == "agent" && strings.Contains(m.Content, "```question") {
			t.Errorf("raw question fence posted next to the card: %q", m.Content)
		}
	}
}

// The watchdog closes runs whose goroutine died without FinishRun. They used
// to stay "running" until the next restart, pinning the live turn bubble in
// the chat forever and leaving the card stuck mid-flight.
func TestReapStaleRunsClosesSilentRun(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	team, err := st.ImportTeam("quick-fix", "Quick Fix")
	if err != nil {
		t.Fatal(err)
	}
	room, err := st.CreateRoom("Reaper Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	settings := st.Snapshot().Settings
	if settings.MaxExecutionSeconds <= 0 {
		settings.MaxExecutionSeconds = 600
		if _, err := st.UpdateSettings(settings); err != nil {
			t.Fatal(err)
		}
	}
	task, err := st.CreateTask(Task{
		RoomID: room.ID, AgentName: team.MainAgent, TeamID: team.ID,
		Title: "Silent run card", Status: "todo", Priority: "normal", Effort: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateTaskStatus(task.ID, "in_progress", true); err != nil {
		t.Fatal(err)
	}
	run := AgentRun{
		ID: "stale-run", TaskID: task.ID, RoomID: room.ID, Status: "running",
		StartedAt: time.Now().UTC().Add(-time.Duration(settings.MaxExecutionSeconds+300) * time.Second),
	}
	if err := st.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	// A fresh run must be left alone.
	fresh := AgentRun{
		ID: "fresh-run", TaskID: task.ID, RoomID: room.ID, Status: "running",
		StartedAt: time.Now().UTC(),
	}
	if err := st.CreateRun(fresh); err != nil {
		t.Fatal(err)
	}

	rt := &Runtime{store: st, watchdogNotice: map[string]time.Time{}}
	rt.reapStaleRuns()

	if got := st.RunStatus("stale-run"); got != "timed_out" {
		t.Errorf("stale run status = %q, want timed_out", got)
	}
	if got := st.RunStatus("fresh-run"); got != "running" {
		t.Errorf("fresh run was reaped: status = %q", got)
	}
	if got := taskStatus(st, task.ID); got != "todo" {
		t.Errorf("reaped card status = %q, want todo (back on the board)", got)
	}
	var notice bool
	for _, m := range st.Snapshot().Rooms[0].Messages {
		if m.ActivityType == "run.timed_out" {
			notice = true
		}
	}
	if !notice {
		t.Error("no chat notice explains why the run disappeared")
	}
}

// The watchdog ticks every 10s; a stall must not spam the room.
func TestStallNoticeIsRateLimited(t *testing.T) {
	rt := &Runtime{watchdogNotice: map[string]time.Time{}}
	if !rt.shouldNotifyStall("room-1", "Ferro") {
		t.Fatal("first stall must notify")
	}
	if rt.shouldNotifyStall("room-1", "Ferro") {
		t.Fatal("second stall within the window must not notify again")
	}
	if !rt.shouldNotifyStall("room-1", "Orion") {
		t.Fatal("a different agent must notify independently")
	}
	if !rt.shouldNotifyStall("room-2", "Ferro") {
		t.Fatal("a different room must notify independently")
	}
}

// send_message posted with role "assistant", which AppendRoomMessage rejects
// (only "user" is accepted), so the tool errored every time and teammates
// never saw the message.
func TestSendMessagePostsAgentBubble(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	team, err := st.ImportTeam("quick-fix", "Quick Fix")
	if err != nil {
		t.Fatal(err)
	}
	room, err := st.CreateRoom("Send Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	send := findTool(t, st, room.ID, t.TempDir(), team.MainAgent, "send_message")
	result, err := send.Run(&agent.ContextMock{}, map[string]any{
		"content": "@Ferro tolong cek diff yang tadi",
	})
	if err != nil {
		t.Fatalf("send_message failed: %v", err)
	}
	if detail := toolDetail(t, result); detail["status"] != "sent" {
		t.Fatalf("send_message result = %#v", detail)
	}
	var found bool
	for _, m := range st.Snapshot().Rooms[0].Messages {
		if m.Role == "agent" && m.AgentName == team.MainAgent && strings.Contains(m.Content, "tolong cek diff") {
			found = true
		}
	}
	if !found {
		t.Fatal("the message never reached the room chat")
	}
}

// An answer must carry a link back to the question it replies to, so the
// bubble is not a floating user message with no context.
func TestAnswerToQuestionCarriesMetadata(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	team, err := st.ImportTeam("quick-fix", "Quick Fix")
	if err != nil {
		t.Fatal(err)
	}
	room, err := st.CreateRoom("Meta Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	q, err := st.AddRoomQuestion(room.ID, AgentQuestion{
		Prompt: "Proceed with the migration?", Options: []string{"Yes", "No"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AnswerRoomQuestion(room.ID, q.ID, "Yes, go ahead"); err != nil {
		t.Fatal(err)
	}
	var last ChatMessage
	msgs := st.Snapshot().Rooms[0].Messages
	last = msgs[len(msgs)-1]
	if last.Role != "user" || last.Content != "Yes, go ahead" {
		t.Fatalf("answer not appended: %#v", last)
	}
	if last.Metadata == nil || last.Metadata["questionId"] != q.ID {
		t.Fatalf("answer metadata = %v, want questionId %q", last.Metadata, q.ID)
	}
	if last.Metadata["question"] != "Proceed with the migration?" {
		t.Fatalf("answer metadata question = %v", last.Metadata["question"])
	}
}
