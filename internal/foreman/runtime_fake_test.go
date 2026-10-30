package foreman

import (
	"context"
	"testing"
	"time"

	"go.uber.org/goleak"
)

type fakeEngine struct {
	results  []EngineResult
	errs     []error
	emit     []EngineEvent
	calls    int
	panicOn  bool
	panicReq *EngineRequest
}

func (f *fakeEngine) RunControlled(ctx context.Context, req EngineRequest, emit func(EngineEvent), control *RunControl) (EngineResult, error) {
	f.calls++
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
		i := f.calls - 1
		if i >= len(f.results) {
			i = len(f.results) - 1
		}
		res = f.results[i]
	}
	var err error
	if len(f.errs) > 0 {
		i := f.calls - 1
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
