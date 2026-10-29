package foreman

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeStubPython writes a shell script that behaves like a Python interpreter
// for the bridge protocol: it ignores its script argument, reads one config
// line from stdin, and emits scripted JSON-line events on stdout.
func writeStubPython(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bridge stub test uses a POSIX shell script")
	}
	path := filepath.Join(t.TempDir(), "stub-python")
	script := "#!/bin/sh\ncat > /dev/null\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func withStubPython(t *testing.T, body string) *Engine {
	t.Helper()
	t.Setenv("FOREMAN_PYTHON", writeStubPython(t, body))
	return NewEngine()
}

func TestEngineRunStreamsEventsAndReturnsResult(t *testing.T) {
	engine := withStubPython(t, `echo '{"type":"status","message":"engine: running"}'
echo '{"type":"tool","message":"file edited"}'
echo '{"type":"result","status":"completed","message":"done","tokens":42,"cost":0.5}'`)

	var seen []string
	result, err := engine.Run(context.Background(), EngineRequest{
		Workspace: t.TempDir(),
		Prompt:    "hello",
		Model:     "test-model",
	}, func(ev EngineEvent) { seen = append(seen, ev.Type+":"+ev.Message) })
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if result.Status != "completed" || result.Message != "done" || result.Tokens != 42 || result.CostUSD != 0.5 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(seen) != 2 || seen[0] != "status:engine: running" || seen[1] != "tool:file edited" {
		t.Fatalf("unexpected events: %#v", seen)
	}
}

func TestEngineRunReportsBridgeCrash(t *testing.T) {
	engine := withStubPython(t, `echo "boom" >&2
exit 3`)

	_, err := engine.Run(context.Background(), EngineRequest{Workspace: t.TempDir()}, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected stderr detail in error, got: %v", err)
	}
}

func TestEngineRunHonoursCancellation(t *testing.T) {
	engine := withStubPython(t, `echo '{"type":"status","message":"starting"}'
sleep 30`)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	result, err := engine.Run(ctx, EngineRequest{Workspace: t.TempDir()}, nil)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("run did not stop promptly on cancellation")
	}
	if result.Status != "cancelled" && result.Status != "timed_out" {
		t.Fatalf("unexpected status: %q", result.Status)
	}
}

func TestNewEnginePrefersEnvOverride(t *testing.T) {
	t.Setenv("FOREMAN_PYTHON", "/opt/custom/python3")
	if got := NewEngine().python; got != "/opt/custom/python3" {
		t.Fatalf("python = %q", got)
	}
}
