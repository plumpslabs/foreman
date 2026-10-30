package foreman

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

type fakeEngine struct {
	mu       sync.Mutex
	results  []EngineResult
	errs     []error
	emit     []EngineEvent
	models   []string // model requested on each call, in order
	calls    int
	panicOn  bool
	panicReq *EngineRequest
}

// modelCalls returns the models requested so far, in order.
func (f *fakeEngine) modelCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.models...)
}

func (f *fakeEngine) RunControlled(ctx context.Context, req EngineRequest, emit func(EngineEvent), control *RunControl) (EngineResult, error) {
	f.mu.Lock()
	f.calls++
	f.models = append(f.models, req.Model)
	calls := f.calls
	f.mu.Unlock()
	if f.panicOn {
		f.panicReq = &req
		panic("fake engine panic")
	}
	if f.emit != nil {
		for _, ev := range f.emit {
			if emit != nil {
				emit(ev)
			}
		}
	}
	res := EngineResult{Status: "completed", Message: "done"}
	if len(f.results) > 0 {
		i := calls - 1
		if i >= len(f.results) {
			i = len(f.results) - 1
		}
		res = f.results[i]
	}
	var err error
	if len(f.errs) > 0 {
		i := calls - 1
		if i >= len(f.errs) {
			i = len(f.errs) - 1
		}
		err = f.errs[i]
	}
	return res, err
}

func (f *fakeEngine) sdkInstalledCached() (bool, string) { return true, "" }
func (f *fakeEngine) diagnostics() EngineDiag            { return EngineDiag{} }
func (f *fakeEngine) installSDK(ctx context.Context) (string, error) {
	return "", nil
}

func waitRunFinished(t *testing.T, rt *Runtime, taskID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !rt.store.HasRunningRunForTask(taskID) {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("run did not finish in time")
}

func ensureFakeProvider(t *testing.T, st *Store) {
	t.Helper()
	_, err := st.SaveProvider(ProviderConfig{
		ID: "fake", Name: "Fake", Protocol: "openai-compatible",
		BaseURL: "http://127.0.0.1:9", Models: []string{"fake-model"},
		DefaultModel: "fake-model", Enabled: true,
	}, "fake-key")
	if err != nil {
		t.Fatalf("upsert provider: %v", err)
	}
}

func TestRuntimeFakeEngineCompleted(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"))
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
	room, err := st.CreateRoom("Fake Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	task, err := st.GetOrCreateChatThread(room.ID, "please do a fake task")
	if err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(st)
	rt.SetEngine(&fakeEngine{})
	defer rt.stopWatchdog()
	run, err := rt.StartTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRunFinished(t, rt, task.ID)

	if run.Status != "running" {
		t.Fatalf("StartTask should return a running run, got %s", run.Status)
	}
	stored := rt.store.RunStatus(run.ID)
	if stored != "completed" {
		t.Fatalf("expected completed, got %s", stored)
	}
	// hidden chat task resets to todo after completion
	var found bool
	for _, tk := range st.Snapshot().Tasks {
		if tk.ID == task.ID && tk.Status == "todo" {
			found = true
		}
	}
	if !found {
		t.Fatal("hidden task should reset to todo")
	}
	// agent reply appended
	var reply bool
	for _, room2 := range st.Snapshot().Rooms {
		for _, m := range room2.Messages {
			if m.Role == "agent" && m.Content == "done" {
				reply = true
			}
		}
	}
	if !reply {
		t.Fatal("expected agent reply 'done' in chat")
	}
}

// A delegated (Kanban) task must leave its report in the room chat. Before
// this, only the status notice was posted — the executor's actual work lived
// in the run record and the Artifacts tab, so to everyone watching the room the
// task looked like it vanished the moment it completed.
func TestRuntimeDelegatedResultPostedToChat(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"))
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
	room, err := st.CreateRoom("Delegated Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	task, err := st.CreateTask(Task{
		RoomID: room.ID, TeamID: team.ID, Title: "Fix the waiting color",
		Description: "check getWaitingSide", Priority: "p2", Effort: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}

	rt := NewRuntime(st)
	rt.SetEngine(&fakeEngine{results: []EngineResult{
		{Status: "completed", Message: "**Root cause**\n\nThe `||` keeps stale state."},
	}})
	defer rt.stopWatchdog()

	run, err := rt.StartTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRunFinished(t, rt, task.ID)
	if got := rt.store.RunStatus(run.ID); got != "completed" {
		t.Fatalf("run status = %s, want completed", got)
	}

	var reply string
	var notice bool
	for _, rm := range st.Snapshot().Rooms {
		if rm.ID != room.ID {
			continue
		}
		for _, m := range rm.Messages {
			if m.Role == "agent" && m.AgentName == task.AgentName {
				reply = m.Content
			}
			if m.Role == "system" && m.ActivityType == "run.finished" {
				notice = true
			}
		}
	}
	if reply == "" {
		t.Fatal("delegated task result never reached the room chat")
	}
	if !strings.Contains(reply, "## Result · Fix the waiting color") {
		t.Fatalf("reply is missing the task header: %q", reply)
	}
	if !strings.Contains(reply, "Root cause") {
		t.Fatalf("reply is missing the executor's report: %q", reply)
	}
	if !notice {
		t.Fatal("the status notice must still be posted alongside the report")
	}
}

// The runtime's placeholder message carries no information, so it must not be
// posted as if the executor had reported something.
func TestRuntimeDelegatedPlaceholderNotPosted(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"))
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
	room, err := st.CreateRoom("Quiet Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	task, err := st.CreateTask(Task{
		RoomID: room.ID, TeamID: team.ID, Title: "Say nothing",
		Description: "empty report", Priority: "p2", Effort: "medium",
	})
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

	for _, rm := range st.Snapshot().Rooms {
		if rm.ID != room.ID {
			continue
		}
		for _, m := range rm.Messages {
			if m.Role == "agent" {
				t.Fatalf("placeholder result must not be posted, got %q", m.Content)
			}
		}
	}
}

// Long reports are truncated in chat so one verbose task cannot flood the room.
func TestDelegatedResultMessageTruncatesLongReports(t *testing.T) {
	long := strings.Repeat("evidence line\n", 800) // ~11k runes
	msg := delegatedResultMessage(Task{Title: "Big refactor"}, long)
	if r := []rune(msg); len(r) > 5000 {
		t.Fatalf("truncated message too long: %d runes", len(r))
	}
	if !strings.Contains(msg, "truncated") {
		t.Fatal("truncated message must point at Runs & activity")
	}
	short := delegatedResultMessage(Task{Title: "Small fix"}, "done, see diff")
	if !strings.Contains(short, "done, see diff") || strings.Contains(short, "truncated") {
		t.Fatalf("short report must be passed through unchanged: %q", short)
	}
}

// RoomRecentContext feeds every run prompt, so notices must stay out of it:
// they used to be attributed to "User" and consume the 10-message window.
func TestRoomRecentContextSkipsNotices(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	team, err := st.ImportTeam("quick-fix", "Quick Fix")
	if err != nil {
		t.Fatal(err)
	}
	room, err := st.CreateRoom("Context Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.AppendRoomMessage(room.ID, "user", "check the ticket colors")
	if err != nil {
		t.Fatal(err)
	}
	_ = st.AppendRoomNotice(room.ID, "Orion", "run.started", "🚀 @Orion started working on: colors")
	_ = st.AppendRoomNotice(room.ID, "Orion", "run.finished", "✅ Task colors finished — status: completed")
	_ = st.AppendAgentReply(room.ID, "Ferro", "Fixed getWaitingSide; unit test added")

	ctx := st.RoomRecentContext(room.ID)
	if !strings.Contains(ctx, "**User:** check the ticket colors") {
		t.Fatalf("user message missing from context: %q", ctx)
	}
	if !strings.Contains(ctx, "**Ferro:** Fixed getWaitingSide") {
		t.Fatalf("agent reply missing from context: %q", ctx)
	}
	if strings.Contains(ctx, "started working on") || strings.Contains(ctx, "finished — status") {
		t.Fatalf("notices must not enter the prompt context: %q", ctx)
	}
}

func TestRuntimeFakeEnginePanicMarksFailed(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"))
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ensureFakeProvider(t, st)
	team, _ := st.ImportTeam("quick-fix", "Quick Fix")
	room, _ := st.CreateRoom("Panic Room", "", []string{team.ID})
	task, _ := st.GetOrCreateChatThread(room.ID, "do a panic")
	rt := NewRuntime(st)
	rt.SetEngine(&fakeEngine{panicOn: true})
	defer rt.stopWatchdog()
	run, err := rt.StartTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRunFinished(t, rt, task.ID)
	if got := rt.store.RunStatus(run.ID); got != "failed" {
		t.Fatalf("expected failed after panic, got %s", got)
	}
}
