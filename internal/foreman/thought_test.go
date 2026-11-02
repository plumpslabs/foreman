package foreman

import (
	"context"
	"iter"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// thinkingLLM streams a private reasoning part alongside the answer, the way
// reasoning-capable providers do.
type thinkingLLM struct{}

func (c *thinkingLLM) Name() string { return "thinking" }

func (c *thinkingLLM) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{
				{Text: "I should verify with typecheck. I have used 7 rounds.", Thought: true},
				{Text: "Jurnal sudah ditulis: 2 perubahan tercatat."},
			}},
		}, nil)
	}
}

// TestThoughtPartsSurviveTheRunner proves the Thought flag reaches the engine
// intact through a real ADK runner run — the filter would be useless if the
// runner dropped or reset the marker.
func TestThoughtPartsSurviveTheRunner(t *testing.T) {
	ag, err := newPromptAgent("You are a coordinator.", &thinkingLLM{}, nil)
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	r, err := runner.New(runner.Config{
		AppName:           "foreman",
		Agent:             ag,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	msg := &genai.Content{Role: "user", Parts: []*genai.Part{{Text: "journal"}}}
	var answer, reasoning strings.Builder
	sawThought := false
	for ev, runErr := range r.Run(context.Background(), "user", "sess-thought", msg, agent.RunConfig{}) {
		if runErr != nil {
			t.Fatalf("run: %v", runErr)
		}
		if ev == nil || ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if p.Thought {
				sawThought = true
				reasoning.WriteString(p.Text)
				continue
			}
			answer.WriteString(answerText(p))
		}
	}
	if !sawThought {
		t.Fatal("runner dropped the Thought marker; the filter cannot work")
	}
	if strings.Contains(answer.String(), "I have used 7 rounds") {
		t.Errorf("reasoning leaked into the answer: %q", answer.String())
	}
	if !strings.Contains(answer.String(), "Jurnal sudah ditulis") {
		t.Errorf("answer lost: %q", answer.String())
	}
}

// TestThoughtPartsAreNotTheAnswer is the direct regression guard for the leaked
// monologue: the engine's own collector must drop reasoning and keep the answer.
func TestThoughtPartsAreNotTheAnswer(t *testing.T) {
	parts := []*genai.Part{
		{Text: "The user sent /journal. I should verify with typecheck. I have used 7 rounds.", Thought: true},
		{Text: "Jurnal sudah ditulis: 2 perubahan tercatat."},
		{Text: "Let me start by understanding the situation.", Thought: true},
	}
	var got strings.Builder
	for _, p := range parts {
		got.WriteString(answerText(p))
	}
	answer := got.String()
	if strings.Contains(answer, "I have used 7 rounds") || strings.Contains(answer, "Let me start") {
		t.Errorf("private reasoning leaked into the answer: %q", answer)
	}
	if !strings.Contains(answer, "Jurnal sudah ditulis") {
		t.Errorf("the actual answer was dropped: %q", answer)
	}
	if answerText(nil) != "" {
		t.Error("nil part must yield no text")
	}
	if answerText(&genai.Part{Thought: true}) != "" {
		t.Error("a thought part with empty text must yield no text")
	}
}

// TestThoughtFlagIsAvailable documents the assumption the fix relies on: the
// genai Part type carries an explicit Thought marker, so distinguishing
// reasoning from answer needs no string heuristics.
func TestThoughtFlagIsAvailable(t *testing.T) {
	p := &genai.Part{Text: "reasoning", Thought: true}
	if !p.Thought {
		t.Fatal("genai.Part.Thought must be settable; the engine relies on it")
	}
}
