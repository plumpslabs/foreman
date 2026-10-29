package foreman

import (
	"context"
	"errors"
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

// withStubProgram writes an arbitrary executable program (any shebang) to act
// as the "Python" interpreter for the engine. Used when the stub needs signal
// handling that a POSIX shell cannot deliver while a child runs.
func withStubProgram(t *testing.T, prog string) *Engine {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stub-python")
	if err := os.WriteFile(path, []byte(prog), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FOREMAN_PYTHON", path)
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

func TestEngineRunControlledPauseSignalsBridge(t *testing.T) {
	// The stub traps SIGTERM (what a pause sends), reports the paused result,
	// and exits so the engine returns it as a first-class outcome. Real Python
	// is used because a POSIX shell cannot run its trap until a child sleep exits.
	stub := `#!/usr/bin/env python3
import json, os, signal, sys, time

def handler(sig, frame):
    print(json.dumps({"type": "result", "status": "paused",
                      "message": "paused, state saved",
                      "tokens": 7, "cost": 0.01}))
    sys.stdout.flush()
    os._exit(0)

signal.signal(signal.SIGTERM, handler)
print(json.dumps({"type": "status", "message": "starting"}))
sys.stdout.flush()
time.sleep(30)
`
	engine := withStubProgram(t, stub)

	control := &RunControl{}
	started := make(chan struct{})
	start := time.Now()
	done := make(chan struct{})
	var result EngineResult
	var err error
	go func() {
		result, err = engine.RunControlled(context.Background(), EngineRequest{Workspace: t.TempDir()},
			func(ev EngineEvent) {
				if ev.Message == "starting" {
					select {
					case <-started:
					default:
						close(started)
					}
				}
			}, control)
		close(done)
	}()
	// Wait until the stub is alive and has installed its SIGTERM handler before
	// pausing, otherwise the signal can land before Python is ready.
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("stub never reported starting")
	}
	control.Pause()
	<-done
	if err != nil {
		t.Fatalf("pause run failed: %v", err)
	}
	if result.Status != "paused" || result.Message != "paused, state saved" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("run did not stop promptly on pause")
	}
}

func TestEngineSDKInstalledProbe(t *testing.T) {
	missing := withStubProgram(t, `#!/bin/sh
echo "No module named 'openhands'" >&2
exit 1
`)
	ok, detail := missing.sdkInstalled()
	if ok || !strings.Contains(detail, "No module named") {
		t.Fatalf("expected missing SDK probe (ok=%v detail=%q)", ok, detail)
	}

	present := withStubProgram(t, `#!/bin/sh
echo ok
exit 0
`)
	ok, detail = present.sdkInstalled()
	if !ok || !strings.Contains(detail, "ok") {
		t.Fatalf("expected present SDK probe (ok=%v detail=%q)", ok, detail)
	}
}

func TestClassifyInstallError(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{"python-version", "ERROR: Ignored installed version ... Requires-Python >=3.12", "Python >= 3.12"},
		{"externally-managed", "error: externally-managed-environment", "PEP 668"},
		{"no-pip", "No module named pip", "ensurepip"},
		{"permission", "error: could not create '/usr/lib/python3/site-packages': Permission denied", "Izin kurang"},
		{"unknown", "ERROR: No matching distribution found for openhands-sdk", ""},
	}
	for _, c := range cases {
		reason, hint := classifyInstallError(errors.New("exit status 1"), c.out, "Python 3.11.9")
		if c.want == "" {
			if reason != "" {
				t.Fatalf("%s: expected generic reason, got %q", c.name, reason)
			}
			continue
		}
		if !strings.Contains(reason, c.want) && !strings.Contains(hint, c.want) {
			t.Fatalf("%s: reason %q / hint %q do not contain %q", c.name, reason, hint, c.want)
		}
	}
}

func TestNewEnginePrefersEnvOverride(t *testing.T) {
	t.Setenv("FOREMAN_PYTHON", "/opt/custom/python3")
	if got := NewEngine().python; got != "/opt/custom/python3" {
		t.Fatalf("python = %q", got)
	}
}
