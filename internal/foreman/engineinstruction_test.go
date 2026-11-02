package foreman

import (
	"context"
	"iter"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// capturingLLM records the last LLM request so a test can assert which
// instructions actually reached the model, and replies with a single text
// turn so the runner completes without tool calls.
type capturingLLM struct {
	sysPrompt string
}

func (c *capturingLLM) Name() string { return "capturing" }

func (c *capturingLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	if req != nil && req.Config != nil && req.Config.SystemInstruction != nil {
		var b strings.Builder
		for _, p := range req.Config.SystemInstruction.Parts {
			b.WriteString(p.Text)
		}
		c.sysPrompt = b.String()
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "done"}}},
		}, nil)
	}
}

// runAgentOnce drives a one-shot runner run over the agent and returns the
// run error, if any.
func runAgentOnce(t *testing.T, ag agent.Agent, llm *capturingLLM, prompt string) error {
	t.Helper()
	r, err := runner.New(runner.Config{
		AppName:           "foreman",
		Agent:             ag,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	msg := &genai.Content{Role: "user", Parts: []*genai.Part{{Text: prompt}}}
	var runErr error
	for _, e := range r.Run(context.Background(), "user", "sess-instr", msg, agent.RunConfig{}) {
		if e != nil {
			runErr = e
		}
	}
	return runErr
}

// TestNewPromptAgentKeepsBracePlaceholders guards against the regression where
// the composed prompt was passed as a plain ADK Instruction. ADK treats every
// `{identifier}` in an Instruction as a session-state placeholder and the run
// died with "failed to append instructions: ... state key does not exist"
// because Foreman has no such state keys. The prompt must reach the model
// verbatim including braces.
func TestNewPromptAgentKeepsBracePlaceholders(t *testing.T) {
	prompt := `# Quick Coordinator
Return JSON like {"success": false, "error": "why"}.
Webhook paths look like /{roomID}/tasks and /{teamID}.
Template: finish_task(status="{status_now}", summary="...").
Also {quick_task} and {attempt_floor} are literal words here.`
	llm := &capturingLLM{}
	ag, err := newPromptAgent(prompt, llm, nil)
	if err != nil {
		t.Fatalf("newPromptAgent: %v", err)
	}
	if err := runAgentOnce(t, ag, llm, prompt); err != nil {
		t.Fatalf("runner run: %v", err)
	}
	if !strings.Contains(llm.sysPrompt, "{quick_task}") {
		t.Fatalf("placeholder braces were templated away: %q", llm.sysPrompt)
	}
	for _, literal := range []string{`{"success": false, "error": "why"}`, "/{roomID}/tasks", "{status_now}"} {
		if !strings.Contains(llm.sysPrompt, literal) {
			t.Fatalf("missing literal %q in instructions: %q", literal, llm.sysPrompt)
		}
	}
}

// TestPlainInstructionWithBracesDocumentsTheFailure keeps the old broken path
// visible in tests: passing the same prompt as a plain Instruction (not through
// a provider) must fail — otherwise the guard above would pass against a
// no-op implementation and the next refactor could reintroduce the bug.
func TestPlainInstructionWithBracesDocumentsTheFailure(t *testing.T) {
	prompt := "Template: finish_task(status=\"{status_now}\", summary=\"...\"). Also {quick_task}."
	llm := &capturingLLM{}
	ag, err := llmagent.New(llmagent.Config{
		Name:        "foreman_agent",
		Model:       llm,
		Instruction: prompt,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
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
	msg := &genai.Content{Role: "user", Parts: []*genai.Part{{Text: prompt}}}
	err = nil
	for _, runErr := range r.Run(context.Background(), "user", "sess-plain", msg, agent.RunConfig{}) {
		if runErr != nil {
			if strings.Contains(runErr.Error(), "state key does not exist") {
				return // the trap is real and documented
			}
			err = runErr
		}
	}
	if err == nil {
		t.Fatal("plain Instruction should have failed with 'state key does not exist' — the guard test above must stay paired with this proof")
	}
	t.Fatalf("unexpected failure shape: %v", err)
}
