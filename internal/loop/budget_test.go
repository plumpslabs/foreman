package loop

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"

	bcontext "github.com/plumpslabs/bro-code/internal/context"
	"github.com/plumpslabs/bro-code/internal/provider"
	"github.com/plumpslabs/bro-code/internal/tool"
)

// budgetAdapter reports a large prompt-token usage on every completion so the
// engine's cost accounting accumulates real dollars against "gpt-4o" prices.
// Main-loop requests (Tools non-nil) return a tool call; synth requests
// (Tools nil) answer with content.
type budgetAdapter struct {
	total int
}

func (a *budgetAdapter) Complete(ctx context.Context, req provider.CompletionRequest) (*provider.CompletionResponse, error) {
	a.total++
	u := provider.Usage{PromptTokens: 1_000_000, CompletionTokens: 0, TotalTokens: 1_000_000}
	if len(req.Tools) == 0 {
		return &provider.CompletionResponse{Content: "SYNTHESIZED", Usage: u}, nil
	}
	return &provider.CompletionResponse{
		ToolCalls: []provider.ToolCall{{ID: "t", Name: "list_dir", Arguments: `{"path":"."}`}},
		Usage:     u,
	}, nil
}

// TestCostBudgetStopsTurnGracefully: with gpt-4o pricing ($2.50/M input) and a
// $5 budget, the turn is cut off after two tool completions and answered by a
// bounded synthesis completion — no raw error, cost counter reflects spend.
func TestCostBudgetStopsTurnGracefully(t *testing.T) {
	tools := tool.NewRegistry()
	ctxMgr := bcontext.NewManager("test_sess", nil, 128000)
	adapter := &budgetAdapter{}
	e := NewEngine(adapter, tools, ctxMgr, "gpt-4o")
	e.SetBudgetUSD(5.0)

	res, err := e.RunTurn(context.Background(), "do some work", nil)
	if err != nil {
		t.Fatalf("RunTurn failed: %v", err)
	}
	if !strings.Contains(res, "SYNTHESIZED") {
		t.Fatalf("expected graceful synthesis answer, got %q", res)
	}
	if !strings.Contains(res, "Cost Budget") {
		t.Fatalf("expected budget-abort marker in answer, got %q", res)
	}
	// 2 tool completions + 1 synth completion, each $2.50.
	if got, want := e.CostUSD(), 7.5; math.Abs(got-want) > 1e-3 {
		t.Fatalf("expected cost %.2f, got %.2f (completions=%d)", want, got, adapter.total)
	}
}

// TestNoBudgetStillRunsFullTurn: without a budget the same adapter runs until
// it exhausts the tool-only budget (never blocking on cost).
func TestNoBudgetStillRunsFullTurn(t *testing.T) {
	tools := tool.NewRegistry()
	ctxMgr := bcontext.NewManager("test_sess", nil, 128000)
	adapter := &budgetAdapter{}
	e := NewEngine(adapter, tools, ctxMgr, "gpt-4o")

	_, err := e.RunTurn(context.Background(), "do some work", nil)
	if err != nil {
		t.Fatalf("RunTurn failed: %v", err)
	}
	if adapter.total <= 2 {
		t.Fatalf("expected more than 2 completions without a budget, got %d", adapter.total)
	}
}

type implementationFlowAdapter struct {
	calls int
}

func (a *implementationFlowAdapter) Complete(ctx context.Context, req provider.CompletionRequest) (*provider.CompletionResponse, error) {
	a.calls++
	if len(req.Tools) == 0 {
		return &provider.CompletionResponse{Content: "SYNTHESIS_CALLED"}, nil
	}
	f1 := "test_scratch_1.txt"
	f2 := "test_scratch_2.txt"
	if a.calls == 1 {
		// Round 1: write test_scratch_1.txt
		return &provider.CompletionResponse{
			Reasoning: "writing file1",
			ToolCalls: []provider.ToolCall{
				{ID: "t1", Name: "write_file", Arguments: fmt.Sprintf(`{"path":%q,"content":"data1"}`, f1)},
			},
		}, nil
	}
	if a.calls <= 12 {
		// Rounds 2-12 (11 consecutive reads): read chunks of test_scratch_2.txt
		return &provider.CompletionResponse{
			Reasoning: "reading file2",
			ToolCalls: []provider.ToolCall{
				{ID: "t2", Name: "read_file", Arguments: fmt.Sprintf(`{"path":%q,"start_line":%d,"end_line":%d}`, f2, (a.calls-1)*50, a.calls*50)},
			},
		}, nil
	}
	if a.calls == 13 {
		// Round 13: write test_scratch_2.txt
		return &provider.CompletionResponse{
			Reasoning: "writing file2",
			ToolCalls: []provider.ToolCall{
				{ID: "t3", Name: "write_file", Arguments: fmt.Sprintf(`{"path":%q,"content":"data2"}`, f2)},
			},
		}, nil
	}
	// Round 14: Final answer
	return &provider.CompletionResponse{
		Content: "Implementation finished successfully for all files.",
	}, nil
}

type mockBudgetTool struct {
	name string
}

func (m *mockBudgetTool) Name() string               { return m.name }
func (m *mockBudgetTool) Description() string        { return "mock" }
func (m *mockBudgetTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (m *mockBudgetTool) Execute(ctx context.Context, args string) (string, error) {
	return "ok", nil
}

func TestActiveImplementationNotCutByReadBudget(t *testing.T) {
	defer os.Remove("test_scratch_1.txt")
	defer os.Remove("test_scratch_2.txt")
	tools := tool.NewRegistry()
	tools.Register(&mockBudgetTool{name: "write_file"})
	tools.Register(&mockBudgetTool{name: "read_file"})
	ctxMgr := bcontext.NewManager("test_impl", nil, 128000)
	adapter := &implementationFlowAdapter{}
	e := NewEngine(adapter, tools, ctxMgr, "test-model")
	e.SetMaxIterations(25)

	res, err := e.RunTurn(context.Background(), "implement multi-file feature", nil)
	if err != nil {
		t.Fatalf("RunTurn failed: %v", err)
	}
	if strings.Contains(res, "SYNTHESIS_CALLED") || strings.Contains(res, "TOOL EXPLORATION BUDGET REACHED") {
		t.Fatalf("active implementation turn was prematurely cut off by exploration budget: %q", res)
	}
	if !strings.Contains(res, "Implementation finished successfully") {
		t.Fatalf("expected successful implementation completion, got: %q", res)
	}
	if len(ctxMgr.EditedFiles()) != 2 {
		t.Fatalf("expected 2 edited files tracked in context manager, got %d: %v", len(ctxMgr.EditedFiles()), ctxMgr.EditedFiles())
	}
}
