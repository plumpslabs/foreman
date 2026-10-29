package loop

import (
	"context"
	"strings"
	"sync"
	"testing"

	bcontext "github.com/plumpslabs/bro-code/internal/context"
	"github.com/plumpslabs/bro-code/internal/provider"
	"github.com/plumpslabs/bro-code/internal/tool"
)

func TestEngineEventStreamAndRunTask(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&stubTool{name: "read_file"})

	adapter := &scriptedAdapter{
		responses: []provider.CompletionResponse{
			{
				Reasoning: "Investigating the request...",
				ToolCalls: []provider.ToolCall{
					{
						ID:        "tc-1",
						Name:      "read_file",
						Arguments: `{"path":"main.go"}`,
					},
				},
			},
			{
				Content: "Refactored successfully.",
			},
		},
	}

	ctxMgr := bcontext.NewManager("test_sess", nil, 10000)
	eng := NewEngine(adapter, reg, ctxMgr, "test-model")

	var (
		mu     sync.Mutex
		events []AgentEvent
	)

	eng.Subscribe(func(ev AgentEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev)
	})

	result, err := eng.RunTask(context.Background(), "refactor the core")
	if err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	if !result.Success {
		t.Errorf("expected result.Success == true, got %v", result.Success)
	}
	if !strings.Contains(result.Message, "Refactored") {
		t.Errorf("unexpected message: %s", result.Message)
	}

	mu.Lock()
	defer mu.Unlock()

	hasTurnStart := false
	hasThinking := false
	hasToolCall := false
	hasToolResult := false
	hasTurnComplete := false

	for _, ev := range events {
		switch ev.Type {
		case EventTurnStart:
			hasTurnStart = true
		case EventThinking:
			hasThinking = true
		case EventToolCall:
			hasToolCall = true
		case EventToolResult:
			hasToolResult = true
		case EventTurnComplete:
			hasTurnComplete = true
		}
	}

	if !hasTurnStart {
		t.Error("missing EventTurnStart")
	}
	if !hasThinking {
		t.Error("missing EventThinking")
	}
	if !hasToolCall {
		t.Error("missing EventToolCall")
	}
	if !hasToolResult {
		t.Error("missing EventToolResult")
	}
	if !hasTurnComplete {
		t.Error("missing EventTurnComplete")
	}
}
