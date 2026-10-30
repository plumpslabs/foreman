package foreman

import (
	"errors"
	"strings"
	"testing"
	"time"
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

func TestNormalizePriority(t *testing.T) {
	cases := map[string]string{
		"p0": "p0", "urgent": "p0", "critical": "p0", "blocker": "p0",
		"p1": "p1", "high": "p1",
		"p2": "p2", "normal": "p2", "medium": "p2", "": "p2",
		"P3": "p3", "low": "p3",
	}
	for in, want := range cases {
		got, ok := NormalizePriority(in)
		if !ok || got != want {
			t.Errorf("NormalizePriority(%q) = (%q, %v), want (%q, true)", in, got, ok, want)
		}
	}
	if _, ok := NormalizePriority("bogus"); ok {
		t.Error("NormalizePriority(bogus) should be invalid")
	}
	if PriorityRank("p0") >= PriorityRank("p3") {
		t.Error("P0 must sort before P3")
	}
}

func TestBackoffAndRetryAfter(t *testing.T) {
	if d := backoffForAttempt(errors.New("boom"), 1); d <= 0 {
		t.Fatalf("expected positive backoff, got %v", d)
	}
	// Exponential growth, capped at 30s.
	if d := backoffForAttempt(errors.New("boom"), 3); d != 8*time.Second {
		t.Fatalf("attempt 3 backoff = %v, want 8s", d)
	}
	if d := backoffForAttempt(errors.New("boom"), 9); d != 30*time.Second {
		t.Fatalf("backoff should cap at 30s, got %v", d)
	}
	// Retry-After hint wins over exponential backoff.
	if d := backoffForAttempt(errors.New("rate limited: retry-after: 20"), 1); d != 20*time.Second {
		t.Fatalf("retry-after backoff = %v, want 20s", d)
	}
	if secs, ok := parseRetryAfter(errors.New("429 too many requests, retry after 5 seconds")); !ok || secs != 5 {
		t.Fatalf("parseRetryAfter = (%d, %v), want (5, true)", secs, ok)
	}
	if _, ok := parseRetryAfter(errors.New("no hint here")); ok {
		t.Error("parseRetryAfter should not match without a hint")
	}
}

func TestConversationIDStablePerRoomAndAgent(t *testing.T) {
	a := conversationID("room-1", Task{AgentName: "Nova"})
	b := conversationID("room-1", Task{AgentName: "Nova"})
	if a != b || a != "room-1:Nova" {
		t.Fatalf("conversationID = %q, want stable room-1:Nova", a)
	}
	if conversationID("room-1", Task{}) != "room-1" {
		t.Fatal("empty agent should scope to the room")
	}
}
