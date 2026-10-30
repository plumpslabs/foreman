package foreman

import (
	"errors"
	"strings"
	"testing"
)

func TestAutoStartQueueOrderAndDedupe(t *testing.T) {
	st := &Store{}
	st.EnqueueAutoStart("a")
	st.EnqueueAutoStart("b")
	st.EnqueueAutoStart("a") // duplicate ignored
	if got := st.PopAutoStart(); got != "a" {
		t.Fatalf("expected a, got %s", got)
	}
	if got := st.PopAutoStart(); got != "b" {
		t.Fatalf("expected b, got %s", got)
	}
	if got := st.PopAutoStart(); got != "" {
		t.Fatalf("expected empty queue, got %s", got)
	}
}

func TestHumanizeFinalFailure(t *testing.T) {
	task := Task{Title: "Fix bug"}
	settings := Settings{MaxExecutionSeconds: 600}
	cases := []struct {
		status string
		want   []string
	}{
		{"timed_out", []string{"time limit", "600", "narrower prompt"}},
		{"cancelled", []string{"stopped by you", "new prompt"}},
		{"blocked", []string{"Blocked after 3 attempts", "retry"}},
		{"failed", []string{"Failed"}},
	}
	for _, c := range cases {
		msg := humanizeFinalFailure(c.status, errors.New("boom"), task, settings, 3)
		for _, want := range c.want {
			if !strings.Contains(strings.ToLower(msg), strings.ToLower(want)) {
				t.Errorf("status %s: message %q missing %q", c.status, msg, want)
			}
		}
	}
}
