package foreman

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"go.uber.org/goleak"
)

// The live failure this hardening targets: ADK reports a malformed tool-call
// payload from the model. It must be classified as transient so the retry can
// re-sample the same model instead of burning the fallback chain.
func TestIsTransientModelErrorMalformedToolCall(t *testing.T) {
	err := errors.New(`openai: parse function call args: invalid character 'i' in literal true ` +
		`(expecting 'r') (name "mcp__kuma_kuma_memory", call_id "call_abc")`)
	if !isTransientModelError(err) {
		t.Fatalf("expected malformed tool-call args to be transient, got false for %v", err)
	}
}

func TestIsTransientModelErrorEmptyOutput(t *testing.T) {
	for _, msg := range []string{
		"openai: empty response",
		"openai: response included no output items",
		"response output did not contain text or tool content",
		"unexpected end of json input",
		"unexpected EOF",
		"invalid character 'x' looking for beginning of value",
		"unexpected end of JSON input while decoding",
	} {
		if !isTransientModelError(errors.New(msg)) {
			t.Errorf("expected transient: %q", msg)
		}
	}
}

// Availability failures must keep rotating: re-sampling the same model that is
// rate-limited or overloaded would just fail again.
func TestIsTransientModelErrorAvailabilityIsNotTransient(t *testing.T) {
	for _, msg := range []string{
		"429 rate limit exceeded",
		"openai: quota exceeded",
		"529 overloaded",
		"503 service unavailable",
		"connection refused",
		"i/o timeout",
		"model not found: some/model",
		"401 unauthorized",
		"context deadline exceeded",
	} {
		if isTransientModelError(errors.New(msg)) {
			t.Errorf("expected NOT transient: %q", msg)
		}
	}
}

func TestIsTransientModelErrorNilAndCancellation(t *testing.T) {
	if isTransientModelError(nil) {
		t.Fatal("nil error must not be transient")
	}
	if isTransientModelError(context.Canceled) {
		t.Fatal("context.Canceled must not be transient")
	}
	if isTransientModelError(fmt.Errorf("wrapped: %w", context.DeadlineExceeded)) {
		t.Fatal("wrapped deadline must not be transient")
	}
}

// Unknown errors fall back to rotating, the pre-hardening behaviour.
func TestIsTransientModelErrorUnknownRotates(t *testing.T) {
	if isTransientModelError(errors.New("some entirely new failure mode")) {
		t.Fatal("unknown errors must not be classified as transient")
	}
}

// Attempt 0 is always the primary model.
func TestRetryPickerAttemptZeroIsPrimary(t *testing.T) {
	p := newRetryPicker([]string{"primary", "fallback-a"}, 3)
	got, resampled := p.model(0, false)
	if got != "primary" || resampled {
		t.Fatalf("attempt 0: got (%q, %v), want (primary, false)", got, resampled)
	}
}

// Availability failures rotate exactly as before — that behaviour is what a
// rate-limited primary model depends on.
func TestRetryPickerRotatesOnAvailabilityFailure(t *testing.T) {
	p := newRetryPicker([]string{"primary", "fallback-a", "fallback-b"}, 3)
	want := []string{"primary", "fallback-a", "fallback-b", "primary"}
	for attempt, expect := range want {
		got, resampled := p.model(attempt, false)
		if got != expect {
			t.Errorf("attempt %d availability failure: got %q, want %q", attempt, got, expect)
		}
		if resampled {
			t.Errorf("attempt %d: an availability failure must never be a re-sample", attempt)
		}
	}
}

// The core fix: malformed output re-samples the model that just failed, and the
// rotation that follows still lands on the *next* model in the chain (the
// attempt index would have wrapped back onto the same one).
func TestRetryPickerResamplesThenRotatesInOrder(t *testing.T) {
	p := newRetryPicker([]string{"primary", "fallback-a"}, 3)

	if got, resampled := p.model(0, false); got != "primary" || resampled {
		t.Fatalf("attempt 0: got (%q, %v), want (primary, false)", got, resampled)
	}
	if got, resampled := p.model(1, true); got != "primary" || !resampled {
		t.Fatalf("attempt 1 transient: got (%q, %v), want (primary, true)", got, resampled)
	}
	// Second consecutive transient failure: rotate, and it must be the model we
	// have NOT tried yet.
	if got, resampled := p.model(2, true); got != "fallback-a" || resampled {
		t.Fatalf("attempt 2 transient: got (%q, %v), want (fallback-a, false)", got, resampled)
	}
	if p.transientRetries != 1 {
		t.Fatalf("transient retries must be capped at 1, got %d", p.transientRetries)
	}
}

// With a single retry available (the user's current setting is 1), spending it
// on a same-model re-sample would leave no fallback if the re-sample fails
// too — that is a regression against the old rotate-first behaviour. So the
// last retry is always spent on rotation.
func TestRetryPickerSingleRetryRotatesInsteadOfResampling(t *testing.T) {
	p := newRetryPicker([]string{"primary", "fallback-a"}, 1)

	if got, _ := p.model(0, true); got != "primary" {
		t.Fatalf("attempt 0: got %q, want primary", got)
	}
	got, resampled := p.model(1, true)
	if resampled {
		t.Fatal("with maxRetries=1 the only retry must rotate, not re-sample")
	}
	if got != "fallback-a" {
		t.Fatalf("got %q, want fallback-a", got)
	}
}

// A re-sample is a one-shot allowance: a model that keeps emitting malformed
// output must not be retried with itself forever.
func TestRetryPickerReSamplesOnlyOnce(t *testing.T) {
	p := newRetryPicker([]string{"primary", "fallback-a", "fallback-b"}, 4)

	if _, resampled := p.model(1, true); !resampled {
		t.Fatal("first transient retry should re-sample")
	}
	if _, resampled := p.model(2, true); resampled {
		t.Fatal("second transient failure must rotate")
	}
	if p.transientRetries != 1 {
		t.Fatalf("transient retries = %d, want 1", p.transientRetries)
	}
}

// A re-sample keeps the model the *previous* attempt used, even when that model
// is itself a fallback obtained by an earlier rotation.
func TestRetryPickerReSamplesRotatedModel(t *testing.T) {
	p := newRetryPicker([]string{"primary", "fallback-a"}, 3)

	if got, _ := p.model(0, false); got != "primary" {
		t.Fatalf("attempt 0: got %q, want primary", got)
	}
	if got, resampled := p.model(1, false); got != "fallback-a" || resampled {
		t.Fatalf("attempt 1 rate-limit: got (%q, %v), want (fallback-a, false)", got, resampled)
	}
	got, resampled := p.model(2, true)
	if got != "fallback-a" || !resampled {
		t.Fatalf("attempt 2 transient: got (%q, %v), want (fallback-a, true)", got, resampled)
	}
}

func TestRetryPickerEmptyChain(t *testing.T) {
	p := newRetryPicker(nil, 3)
	if got, _ := p.model(2, true); got != "" {
		t.Fatalf("empty chain must yield an empty model so the caller can fall back, got %q", got)
	}
}

// A single-model chain cannot rotate; the picker must still behave sanely.
func TestRetryPickerSingleModelChain(t *testing.T) {
	p := newRetryPicker([]string{"only"}, 3)
	for attempt := 0; attempt < 4; attempt++ {
		if got, _ := p.model(attempt, attempt%2 == 1); got != "only" {
			t.Fatalf("attempt %d: got %q, want only", attempt, got)
		}
	}
}

// End-to-end through the runtime: a malformed tool-call response re-samples the
// model that just failed, an availability failure rotates to the fallback, and
// with maxRetries=1 (the production setting) a malformed response still
// rotates so the run keeps a real fallback. All three succeed, so the task
// completes instead of blocking.
func TestRuntimeRetryPolicyEndToEnd(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"))

	cases := []struct {
		name       string
		firstErr   error
		maxRetries int // 0 keeps the server default
		wantRetry  string
	}{
		{
			name: "malformed tool-call args re-samples the same model",
			firstErr: errors.New(`openai: parse function call args: invalid character 'i' in literal ` +
				`true (expecting 'r') (name "mcp__kuma_kuma_memory", call_id "call_abc") (retry-after: 1)`),
			wantRetry: "fake-model",
		},
		{
			name:      "rate limit rotates to the fallback model",
			firstErr:  errors.New("429 rate limit exceeded (retry-after: 1)"),
			wantRetry: "fake-fallback",
		},
		{
			name: "single retry still rotates on malformed output",
			firstErr: errors.New(`openai: parse function call args: unexpected end of JSON input ` +
				`(retry-after: 1)`),
			maxRetries: 1,
			wantRetry:  "fake-fallback",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, err := OpenStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			ensureFakeProvider(t, st)
			// Two models so a rotation is observable against a same-model retry.
			if _, err := st.SaveProvider(ProviderConfig{
				ID: "fake", Name: "Fake", Protocol: "openai-compatible",
				BaseURL:      "http://127.0.0.1:9",
				Models:       []string{"fake-model", "fake-fallback"},
				DefaultModel: "fake-model", Enabled: true,
			}, "fake-key"); err != nil {
				t.Fatal(err)
			}
			if tc.maxRetries > 0 {
				settings := st.Snapshot().Settings
				settings.MaxRetries = tc.maxRetries
				if _, err := st.UpdateSettings(settings); err != nil {
					t.Fatalf("set maxRetries=%d: %v", tc.maxRetries, err)
				}
			}
			team, err := st.ImportTeam("quick-fix", "Quick Fix")
			if err != nil {
				t.Fatal(err)
			}
			room, err := st.CreateRoom("Retry Room", "", []string{team.ID})
			if err != nil {
				t.Fatal(err)
			}
			task, err := st.GetOrCreateChatThread(room.ID, "do a flaky task")
			if err != nil {
				t.Fatal(err)
			}

			rt := NewRuntime(st)
			// First call fails, second succeeds.
			fe := &fakeEngine{errs: []error{tc.firstErr, nil}}
			rt.SetEngine(fe)
			defer rt.stopWatchdog()

			run, err := rt.StartTask(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			waitRunFinished(t, rt, task.ID)

			if got := rt.store.RunStatus(run.ID); got != "completed" {
				t.Fatalf("expected the retry to succeed, run status = %s", got)
			}
			models := fe.modelCalls()
			if len(models) != 2 {
				t.Fatalf("expected exactly 2 engine calls (fail + retry), got %d: %v", len(models), models)
			}
			if models[1] != tc.wantRetry {
				t.Fatalf("retry used model %q, want %q", models[1], tc.wantRetry)
			}
			if models[0] != "fake-model" {
				t.Fatalf("first attempt used model %q, want fake-model", models[0])
			}
		})
	}
}
