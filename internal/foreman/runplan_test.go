package foreman

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// keyFailureClassify covers the narrow rule that separates the two rotation
// axes: only credential problems may move the key pointer.
func TestClassifyKeyFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want keyFailureKind
	}{
		{"401", errors.New("openai: 401 Unauthorized"), failureCredential},
		{"403", errors.New("openai: 403 Forbidden"), failureCredential},
		{"explicit rejection", errors.New("provider rejected the API key (HTTP 401)"), failureCredential},
		{"429", errors.New("429 rate limit exceeded"), failureQuota},
		{"quota wording", errors.New("insufficient quota for this account"), failureQuota},
		{"503", errors.New("503 service unavailable"), failureOther},
		{"overloaded", errors.New("model overloaded"), failureOther},
		{"malformed output", errors.New(`openai: parse function call args: unexpected end of JSON input`), failureOther},
		{"nil", nil, failureOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyKeyFailure(tc.err); got != tc.want {
				t.Fatalf("classifyKeyFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// A rejected key must be retired so a 3-account provider does not spend three
// attempts re-learning that the first key is expired.
func TestRotationRetiresRejectedKey(t *testing.T) {
	rot := newRotation(RunPlan{Stages: []RunStage{{
		Provider: ProviderConfig{ID: "p", Name: "P", DefaultModel: "m"},
		Keys:     []RunKey{{Name: "a", Secret: "s1"}, {Name: "b", Secret: "s2"}, {Name: "c", Secret: "s3"}},
		Models:   []string{"m"},
	}}})

	if _, k, _, ok := rot.position(); !ok || k.Name != "a" {
		t.Fatalf("first position should be key a, got %q (ok=%v)", k.Name, ok)
	}
	rot.killCurrent()
	if rot.allKeysRejected() {
		t.Fatal("not every key is dead yet")
	}
	if !rot.nextKeyInStage() {
		t.Fatal("nextKeyInStage should reach b")
	}
	if _, k, _, _ := rot.position(); k.Name != "b" {
		t.Fatalf("current key = %q, want b", k.Name)
	}
	rot.killCurrent()
	if !rot.nextKeyInStage() {
		t.Fatal("nextKeyInStage should reach c")
	}
	rot.killCurrent()
	if !rot.allKeysRejected() {
		t.Fatal("every key should be dead")
	}
	if rot.nextKeyInStage() {
		t.Fatal("nextKeyInStage must fail once every key is dead")
	}
}

// A provider without credentials (local endpoint) still gets one attempt.
func TestRotationEmptyPoolStillAttemptsOnce(t *testing.T) {
	rot := newRotation(RunPlan{Stages: []RunStage{{
		Provider: ProviderConfig{ID: "local", Name: "Local", DefaultModel: "m"},
	}}})
	_, k, model, ok := rot.position()
	if !ok {
		t.Fatal("a keyless provider must still yield one attempt")
	}
	if k.Secret != "" || model != "m" {
		t.Fatalf("unexpected position: key=%+v model=%q", k, model)
	}
}

// Models on the same provider share a stage; models declared by another
// provider start a new stage so the retry can actually reach that endpoint.
func TestRotationWalksStagesAcrossProviders(t *testing.T) {
	rot := newRotation(RunPlan{Stages: []RunStage{
		{
			Provider: ProviderConfig{ID: "a", Name: "A"},
			Keys:     []RunKey{{Name: "a1", Secret: "a-secret"}},
			Models:   []string{"a-model", "a-backup"},
		},
		{
			Provider: ProviderConfig{ID: "b", Name: "B"},
			Keys:     []RunKey{{Name: "b1", Secret: "b-secret"}},
			Models:   []string{"b-model"},
		},
	}})

	want := []struct{ provider, model, secret string }{
		{"a", "a-model", "a-secret"},
		{"a", "a-backup", "a-secret"},
		{"b", "b-model", "b-secret"},
	}
	for i, w := range want {
		st, k, m, ok := rot.position()
		if !ok {
			t.Fatalf("attempt %d: no position available", i)
		}
		if st.Provider.ID != w.provider || m != w.model || k.Secret != w.secret {
			t.Fatalf("attempt %d = (%s, %s, %s), want (%s, %s, %s)",
				i, st.Provider.ID, m, k.Secret, w.provider, w.model, w.secret)
		}
		if i < len(want)-1 && !rot.nextModel() {
			t.Fatalf("attempt %d: nextModel should have a successor", i)
		}
	}
	if rot.nextModel() {
		t.Fatal("no attempt should remain after the last stage")
	}
}

// A malformed response gets exactly one free re-sample per position.
func TestRotationResamplesOnce(t *testing.T) {
	rot := newRotation(RunPlan{Stages: []RunStage{{
		Provider: ProviderConfig{ID: "p", Name: "P"},
		Keys:     []RunKey{{Name: "k", Secret: "s"}},
		Models:   []string{"m1", "m2"},
	}}})
	if !rot.resample() {
		t.Fatal("first malformed response should re-sample")
	}
	if _, _, m, _ := rot.position(); m != "m1" {
		t.Fatalf("re-sample must keep the model, got %q", m)
	}
	if rot.resample() {
		t.Fatal("a position may only re-sample once")
	}
	if !rot.nextModel() {
		t.Fatal("after the free re-sample the model should rotate")
	}
	if _, _, m, _ := rot.position(); m != "m2" {
		t.Fatalf("expected m2 after rotation, got %q", m)
	}
}

// The attempt floor guarantees one attempt per account and per stage.
func TestRotationAttemptFloor(t *testing.T) {
	rot := newRotation(RunPlan{Stages: []RunStage{
		{Provider: ProviderConfig{ID: "a"}, Keys: []RunKey{{Name: "1"}, {Name: "2"}}, Models: []string{"m"}},
		{Provider: ProviderConfig{ID: "b"}, Keys: []RunKey{{Name: "1"}}, Models: []string{"m"}},
	}})
	if got := rot.attemptFloor(); got != 4 {
		t.Fatalf("attemptFloor = %d, want 4 (3 keys + 2 stages - 1)", got)
	}
}

func TestRunPlanRedactsEveryKey(t *testing.T) {
	plan := RunPlan{Stages: []RunStage{
		{Provider: ProviderConfig{ID: "a"}, Keys: []RunKey{{Name: "a", Secret: "secret-one"}}, Models: []string{"m"}},
		{Provider: ProviderConfig{ID: "b"}, Keys: []RunKey{{Name: "b", Secret: "secret-two"}}, Models: []string{"m"}},
	}}
	err := errors.New("request failed with secret-one and secret-two")
	got := safeRuntimeErrorMulti(err, plan.secrets())
	if strings.Contains(got, "secret-one") || strings.Contains(got, "secret-two") {
		t.Fatalf("secrets leaked: %q", got)
	}
	if strings.Count(got, "[REDACTED]") != 2 {
		t.Fatalf("expected both secrets redacted, got %q", got)
	}
	if safeRuntimeError(err, "secret-one") == "" {
		t.Fatal("single-secret helper should still redact")
	}
}

// runKeysLocked must skip accounts the user switched off and accounts whose
// stored secret is empty, so a rotation never wastes an attempt on them.
func TestRunKeysLockedSkipsDisabledAndEmpty(t *testing.T) {
	st, err := openStoreForTest(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := ProviderConfig{
		ID: "zro", Name: "Zro", Protocol: "openai-compatible",
		BaseURL: "https://example.test/v1", DefaultModel: "m1", Enabled: true,
	}
	if _, err := st.SaveProvider(cfg, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveProviderKey("zro", "one", "secret-one", true); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveProviderKey("zro", "two", "secret-two", false); err != nil {
		t.Fatal(err)
	}
	// A key whose stored credential vanished (credentials.json edited by hand,
	// or an old provider record) must be skipped rather than attempted blank.
	if err := st.SaveProviderKey("zro", "three", "secret-three", true); err != nil {
		t.Fatal(err)
	}
	if err := st.saveProviderKey(st.Snapshot().ProjectPath, "zro", "three", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveProviderKey("zro", "four", "secret-four", true); err != nil {
		t.Fatal(err)
	}

	saved := st.ProvidersForAPI()[0]
	keys, err := st.runKeysLocked(saved)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 usable keys, got %+v", keys)
	}
	if keys[0].Name != "one" || keys[0].Secret != "secret-one" {
		t.Fatalf("first key = %+v, want one", keys[0])
	}
	if keys[1].Name != "four" || keys[1].Secret != "secret-four" {
		t.Fatalf("second key = %+v, want four", keys[1])
	}

	// Switching every account off must mean "do not run" — never a silent
	// fallback to the legacy credential the user retired.
	for _, name := range []string{"one", "four"} {
		if err := st.SaveProviderKey("zro", name, "", false); err != nil {
			t.Fatal(err)
		}
	}
	keys, err = st.runKeysLocked(st.ProvidersForAPI()[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected no usable keys when every account is off, got %+v", keys)
	}
}

// A provider with no named keys falls back to the legacy default credential.
func TestRunKeysLockedFallsBackToLegacyDefault(t *testing.T) {
	st, err := openStoreForTest(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := ProviderConfig{
		ID: "legacy", Name: "Legacy", Protocol: "openai-compatible",
		BaseURL: "https://example.test/v1", DefaultModel: "m1", Enabled: true,
	}
	if _, err := st.SaveProvider(cfg, "legacy-secret"); err != nil {
		t.Fatal(err)
	}
	keys, err := st.runKeysLocked(st.ProvidersForAPI()[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Secret != "legacy-secret" || keys[0].Name != "Default" {
		t.Fatalf("expected the legacy default credential, got %+v", keys)
	}
}

// prepareRotationRun builds a provider with the given named keys plus a team,
// room and chat task, so the runtime can be driven end to end.
func prepareRotationRun(t *testing.T, keyNames []string) (*Store, *Runtime, *fakeEngine, Task) {
	t.Helper()
	st, err := openStoreForTest(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveProvider(ProviderConfig{
		ID: "fake", Name: "Fake", Protocol: "openai-compatible",
		BaseURL:      "http://127.0.0.1:9",
		Models:       []string{"fake-model", "fake-fallback"},
		DefaultModel: "fake-model", Enabled: true,
	}, ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range keyNames {
		if err := st.SaveProviderKey("fake", name, "secret-"+name, true); err != nil {
			t.Fatal(err)
		}
	}
	team, err := st.ImportTeam("quick-fix", "Quick Fix")
	if err != nil {
		t.Fatal(err)
	}
	room, err := st.CreateRoom("Rotation Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	task, err := st.GetOrCreateChatThread(room.ID, "rotate the key please")
	if err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(st)
	rt.sleepFn = func(time.Duration) {}
	fe := &fakeEngine{}
	rt.SetEngine(fe)
	return st, rt, fe, task
}

// 401 rotates the account and keeps the model: the model was never the problem.
func TestRuntimeRotatesKeyOnRejectedCredential(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"))
	isolateGlobal(t)

	st, rt, fe, task := prepareRotationRun(t, []string{"one", "two"})
	defer rt.stopWatchdog()
	fe.errs = []error{errors.New("openai: 401 Unauthorized"), nil}

	run, err := rt.StartTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRunFinished(t, rt, task.ID)

	if got := st.RunStatus(run.ID); got != "completed" {
		t.Fatalf("expected the second account to succeed, run status = %s", got)
	}
	models := fe.modelCalls()
	if len(models) != 2 || models[0] != "fake-model" || models[1] != "fake-model" {
		t.Fatalf("key rotation must keep the model steady, got models %v", models)
	}
	keys := fe.keyCalls()
	if len(keys) != 2 || keys[0] != "secret-one" || keys[1] != "secret-two" {
		t.Fatalf("expected secret-one then secret-two, got %v", keys)
	}
}

// 429 is per account: switch account, and the model stays put.
func TestRuntimeRotatesKeyOnQuota(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"))
	isolateGlobal(t)

	st, rt, fe, task := prepareRotationRun(t, []string{"one", "two"})
	defer rt.stopWatchdog()
	fe.errs = []error{errors.New("429 rate limit exceeded"), nil}

	run, err := rt.StartTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRunFinished(t, rt, task.ID)

	if got := st.RunStatus(run.ID); got != "completed" {
		t.Fatalf("expected the second account to succeed, run status = %s", got)
	}
	keys := fe.keyCalls()
	if len(keys) != 2 || keys[1] != "secret-two" {
		t.Fatalf("expected the second account to be tried, got %v", keys)
	}
	if models := fe.modelCalls(); models[1] != "fake-model" {
		t.Fatalf("a quota failure must not rotate the model, got %v", models)
	}
}

// 503 is capacity, not credentials: rotate the model and keep the account.
func TestRuntimeRotatesModelOnOverload(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"))
	isolateGlobal(t)

	st, rt, fe, task := prepareRotationRun(t, []string{"one", "two"})
	defer rt.stopWatchdog()
	fe.errs = []error{errors.New("503 service unavailable"), nil}

	run, err := rt.StartTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRunFinished(t, rt, task.ID)

	if got := st.RunStatus(run.ID); got != "completed" {
		t.Fatalf("expected the fallback model to succeed, run status = %s", got)
	}
	models := fe.modelCalls()
	if len(models) != 2 || models[1] != "fake-fallback" {
		t.Fatalf("expected a model rotation to fake-fallback, got %v", models)
	}
	keys := fe.keyCalls()
	if keys[1] != "secret-one" {
		t.Fatalf("a capacity failure must keep the account, got %v", keys)
	}
}

// A malformed response re-samples the same model on the same account.
func TestRuntimeResamplesSameModelAndKeyOnMalformedOutput(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"))
	isolateGlobal(t)

	st, rt, fe, task := prepareRotationRun(t, []string{"one", "two"})
	defer rt.stopWatchdog()
	fe.errs = []error{errors.New(`openai: parse function call args: unexpected end of JSON input`), nil}

	run, err := rt.StartTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRunFinished(t, rt, task.ID)

	if got := st.RunStatus(run.ID); got != "completed" {
		t.Fatalf("expected the re-sample to succeed, run status = %s", got)
	}
	if models := fe.modelCalls(); models[1] != "fake-model" {
		t.Fatalf("a malformed response must re-sample the same model, got %v", models)
	}
	if keys := fe.keyCalls(); keys[1] != "secret-one" {
		t.Fatalf("a malformed response must keep the same account, got %v", keys)
	}
}

// When every account is rejected the run stops with an explicit message
// instead of exhausting the model chain first.
func TestRuntimeStopsWhenEveryKeyIsRejected(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"))
	isolateGlobal(t)

	st, rt, fe, task := prepareRotationRun(t, []string{"one", "two", "three"})
	defer rt.stopWatchdog()
	fe.errs = []error{
		errors.New("openai: 401 Unauthorized"),
		errors.New("openai: 401 Unauthorized"),
		errors.New("openai: 401 Unauthorized"),
	}

	run, err := rt.StartTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRunFinished(t, rt, task.ID)

	if got := st.RunStatus(run.ID); got != "blocked" {
		t.Fatalf("expected a blocked run, got %s", got)
	}
	if calls := len(fe.keyCalls()); calls != 3 {
		t.Fatalf("expected exactly one attempt per account (3), got %d", calls)
	}
	var final AgentRun
	for _, r := range st.Snapshot().Runs {
		if r.ID == run.ID {
			final = r
		}
	}
	if !strings.Contains(final.Error, "all 3 accounts") {
		t.Fatalf("expected an explicit all-accounts message, got %q", final.Error)
	}
	if strings.Contains(final.Error, "secret-") {
		t.Fatalf("run error leaked a credential: %q", final.Error)
	}
}

// A provider with more accounts than retries still gets one attempt per
// account: the budget is max(retries+1, len(keys)).
func TestRuntimeAttemptBudgetCoversEveryAccount(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"))
	isolateGlobal(t)

	st, rt, fe, task := prepareRotationRun(t, []string{"one", "two", "three", "four"})
	defer rt.stopWatchdog()
	settings := st.Snapshot().Settings
	settings.MaxRetries = 1
	if _, err := st.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}
	// Quota failures keep every key alive, so the loop may visit all four.
	fe.errs = []error{
		errors.New("429 rate limit exceeded"),
		errors.New("429 rate limit exceeded"),
		errors.New("429 rate limit exceeded"),
		nil,
	}

	run, err := rt.StartTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRunFinished(t, rt, task.ID)

	if got := st.RunStatus(run.ID); got != "completed" {
		t.Fatalf("expected the fourth account to succeed, run status = %s", got)
	}
	keys := fe.keyCalls()
	if len(keys) != 4 {
		t.Fatalf("expected 4 attempts (one per account), got %d: %v", len(keys), keys)
	}
	if keys[3] != "secret-four" {
		t.Fatalf("expected the fourth account last, got %v", keys)
	}
}

// A model chain that names a model from a second provider must reach that
// provider's endpoint and key. Sending it to the first provider's base URL is
// the bug this test locks down.
func TestRuntimeFallsBackAcrossProviders(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"))
	isolateGlobal(t)

	st, err := openStoreForTest(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveProvider(ProviderConfig{
		ID: "alpha", Name: "Alpha", Protocol: "openai-compatible",
		BaseURL: "https://alpha.test/v1", Models: []string{"alpha-fast"},
		DefaultModel: "alpha-fast", Enabled: true,
	}, "alpha-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveProvider(ProviderConfig{
		ID: "beta", Name: "Beta", Protocol: "openai-compatible",
		BaseURL: "https://beta.test/v1", Models: []string{"beta-strong"},
		DefaultModel: "beta-strong", Enabled: true,
	}, "beta-key"); err != nil {
		t.Fatal(err)
	}
	// The library profiles pin a chain spanning both providers. Team rosters
	// are reconciled from the library, so the profiles are the source of truth.
	team, err := st.ImportTeam("quick-fix", "Spanning")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range team.Agents {
		lib := libraryAgent(t, st, a.Name)
		lib.ProviderID = "alpha"
		lib.Model = "alpha-fast, beta-strong"
		if _, err := st.SaveAgent(lib); err != nil {
			t.Fatal(err)
		}
	}
	if team, err = st.UpdateTeam(team); err != nil {
		t.Fatal(err)
	}
	room, err := st.CreateRoom("Cross Room", "", []string{team.ID})
	if err != nil {
		t.Fatal(err)
	}
	task, err := st.GetOrCreateChatThread(room.ID, "use both providers")
	if err != nil {
		t.Fatal(err)
	}

	rt := NewRuntime(st)
	rt.sleepFn = func(time.Duration) {}
	defer rt.stopWatchdog()
	fe := &fakeEngine{errs: []error{errors.New("503 service unavailable"), nil}}
	rt.SetEngine(fe)

	run, err := rt.StartTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRunFinished(t, rt, task.ID)
	if got := st.RunStatus(run.ID); got != "completed" {
		t.Fatalf("expected the second provider to succeed, run status = %s", got)
	}
	urls := fe.urlCalls()
	keys := fe.keyCalls()
	models := fe.modelCalls()
	if len(urls) != 2 {
		t.Fatalf("expected 2 attempts, got %d: %v", len(urls), urls)
	}
	if urls[0] != "https://alpha.test/v1" || keys[0] != "alpha-key" || models[0] != "alpha-fast" {
		t.Fatalf("first attempt = (%s, %s, %s), want alpha", urls[0], keys[0], models[0])
	}
	if urls[1] != "https://beta.test/v1" || keys[1] != "beta-key" || models[1] != "beta-strong" {
		t.Fatalf("second attempt = (%s, %s, %s), want beta", urls[1], keys[1], models[1])
	}
}

// buildRunPlan keeps same-provider models in one stage and starts a new stage
// for a model that only another provider declares.
func TestBuildRunPlanGroupsStages(t *testing.T) {
	st, err := openStoreForTest(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveProvider(ProviderConfig{
		ID: "alpha", Name: "Alpha", Protocol: "openai-compatible",
		BaseURL: "https://alpha.test/v1", Models: []string{"a1", "a2"},
		DefaultModel: "a1", Enabled: true,
	}, "alpha-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveProvider(ProviderConfig{
		ID: "beta", Name: "Beta", Protocol: "openai-compatible",
		BaseURL: "https://beta.test/v1", Models: []string{"b1"},
		DefaultModel: "b1", Enabled: true,
	}, "beta-key"); err != nil {
		t.Fatal(err)
	}
	alpha := st.Snapshot().Providers
	var alphaCfg ProviderConfig
	for _, p := range alpha {
		if p.ID == "alpha" {
			alphaCfg = p
		}
	}
	keys, err := st.runKeysLocked(alphaCfg)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := st.buildRunPlan(alphaCfg, keys, []string{"a1", "a2", "b1", "hand-typed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Stages) != 3 {
		t.Fatalf("expected 3 stages (alpha, beta, alpha-unknown), got %d: %+v", len(plan.Stages), plan.Stages)
	}
	if plan.Stages[0].Provider.ID != "alpha" || len(plan.Stages[0].Models) != 2 {
		t.Fatalf("stage 0 = %+v, want alpha with 2 models", plan.Stages[0])
	}
	if plan.Stages[1].Provider.ID != "beta" || plan.Stages[1].Models[0] != "b1" {
		t.Fatalf("stage 1 = %+v, want beta", plan.Stages[1])
	}
	if plan.Stages[2].Provider.ID != "alpha" || plan.Stages[2].Models[0] != "hand-typed" {
		t.Fatalf("stage 2 = %+v, want alpha with the unknown model", plan.Stages[2])
	}
}
