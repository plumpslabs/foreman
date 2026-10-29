package foreman

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed bridge/agent_bridge.py
var bridgeScript []byte

// EngineTool is one OpenHands tool to load for a run.
type EngineTool struct {
	Name   string `json:"name"`
	Module string `json:"module"`
	Class  string `json:"class"`
}

// RunControl lets the runtime ask a live bridge run to pause gracefully.
type RunControl struct {
	mu    sync.Mutex
	pause func()
}

func (c *RunControl) setPause(fn func()) {
	c.mu.Lock()
	c.pause = fn
	c.mu.Unlock()
}

// Pause asks the bridge to stop at the next step boundary and keep state.
func (c *RunControl) Pause() {
	c.mu.Lock()
	fn := c.pause
	c.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// sdkImportProbe fails unless the OpenHands SDK packages are importable.
const sdkImportProbe = "import openhands.sdk, openhands.tools; print('ok')"

// sdkInstalled probes whether the OpenHands SDK is importable by this engine's
// Python. It returns true plus the probe output when the import succeeds.
func (e *Engine) sdkInstalled() (bool, string) {
	cmd := exec.Command(e.python, "-c", sdkImportProbe)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return err == nil, strings.TrimSpace(out.String())
}

// installSDK installs the OpenHands SDK into the engine's Python via pip. On
// PEP 668 "externally-managed-environment" errors it retries with
// --break-system-packages. It returns the combined pip output.
func (e *Engine) installSDK(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, e.python, args...)
		var b bytes.Buffer
		cmd.Stdout = &b
		cmd.Stderr = &b
		err := cmd.Run()
		return b.String(), err
	}
	out, err := run("-m", "pip", "install", "--upgrade", "openhands-sdk", "openhands-tools")
	if err != nil && strings.Contains(strings.ToLower(out), "externally-managed") {
		note := "\n[retried with --break-system-packages]\n"
		out2, err2 := run("-m", "pip", "install", "--upgrade", "--break-system-packages",
			"openhands-sdk", "openhands-tools")
		return out + note + out2, err2
	}
	return out, err
}

// EngineRequest describes one bounded OpenHands run executed by the bridge.
// JSON tags must match the config keys read by bridge/agent_bridge.py.
type EngineRequest struct {
	Workspace            string       `json:"workspace"`
	Prompt               string       `json:"prompt"`
	Protocol             string       `json:"protocol"`
	BaseURL              string       `json:"baseUrl"`
	APIKey               string       `json:"apiKey"`
	Model                string       `json:"model"`
	Tools                []EngineTool `json:"tools"`
	PersistenceDir       string       `json:"persistenceDir,omitempty"`
	ConversationID       string       `json:"conversationId,omitempty"`
	MaxIterations        int          `json:"maxIterations"`
	BudgetUSD            float64      `json:"budgetUsd"`
	AllowProjectCommands bool         `json:"allowProjectCommands"`
}

// EngineEvent is one streamed update from the bridge process.
type EngineEvent struct {
	Type    string  `json:"type"`
	Message string  `json:"message"`
	Status  string  `json:"status"`
	Tokens  int     `json:"tokens"`
	Cost    float64 `json:"cost"`
}

// EngineResult is the terminal outcome of a bridge run.
type EngineResult struct {
	Status  string
	Message string
	Tokens  int
	CostUSD float64
}

// Engine runs OpenHands bridge processes for the Foreman runtime.
type Engine struct {
	python string
}

// NewEngine resolves the Python interpreter used for the bridge. The
// FOREMAN_PYTHON environment variable overrides auto-detection so users and
// tests can pin a specific interpreter (e.g. a venv with openhands installed).
func NewEngine() *Engine {
	if py := strings.TrimSpace(os.Getenv("FOREMAN_PYTHON")); py != "" {
		return &Engine{python: py}
	}
	for _, candidate := range []string{"python3", "python"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return &Engine{python: path}
		}
	}
	return &Engine{}
}

// materializeBridge writes the embedded bridge script into a private temp
// directory so Python can execute it from disk.
func (e *Engine) materializeBridge() (string, func(), error) {
	dir, err := os.MkdirTemp("", "foreman-bridge-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	scriptPath := filepath.Join(dir, "agent_bridge.py")
	if err := os.WriteFile(scriptPath, bridgeScript, 0600); err != nil {
		cleanup()
		return "", nil, err
	}
	return scriptPath, cleanup, nil
}

// Run executes one bridge run without pause control.
func (e *Engine) Run(ctx context.Context, req EngineRequest, emit func(EngineEvent)) (EngineResult, error) {
	return e.run(ctx, req, emit, nil)
}

// RunControlled executes one bridge run and wires pause signals to control.
func (e *Engine) RunControlled(ctx context.Context, req EngineRequest, emit func(EngineEvent), control *RunControl) (EngineResult, error) {
	return e.run(ctx, req, emit, control)
}

// run executes one bridge run, forwarding events to emit until the process
// finishes or ctx is cancelled. It returns the terminal result or an error
// when the bridge never produced one.
func (e *Engine) run(ctx context.Context, req EngineRequest, emit func(EngineEvent), control *RunControl) (EngineResult, error) {
	if e.python == "" {
		return EngineResult{}, errors.New("no Python interpreter found; install Python 3 or set FOREMAN_PYTHON")
	}
	scriptPath, cleanup, err := e.materializeBridge()
	if err != nil {
		return EngineResult{}, fmt.Errorf("prepare engine bridge: %w", err)
	}
	defer cleanup()

	payload, err := json.Marshal(req)
	if err != nil {
		return EngineResult{}, err
	}
	// Serialize event emission so the stdout scanner and the stderr streamer
	// never invoke the caller's emit concurrently.
	var emitMu sync.Mutex
	safeEmit := func(ev EngineEvent) {
		if emit == nil {
			return
		}
		emitMu.Lock()
		defer emitMu.Unlock()
		emit(ev)
	}
	cmd := exec.CommandContext(ctx, e.python, scriptPath)
	cmd.Dir = req.Workspace
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	// Bound pipe waits after the bridge exits: child processes inherited from
	// the bridge could otherwise keep stdout open and stall the event reader.
	cmd.WaitDelay = 3 * time.Second

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return EngineResult{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return EngineResult{}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = newStderrStreamer(&stderr, 8<<10, func(line string) {
		safeEmit(EngineEvent{Type: "status", Message: "[engine] " + line})
	})

	if err := cmd.Start(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return EngineResult{Status: statusFromContext(ctx)}, ctx.Err()
		}
		return EngineResult{}, fmt.Errorf("start engine bridge (%s): %w", e.python, err)
	}
	// A pause request sends SIGTERM so the bridge can stop at a step boundary
	// and persist its conversation state instead of being killed.
	if control != nil {
		control.setPause(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGTERM)
			}
		})
	}

	// Feed the config line, then close stdin so the bridge sees EOF.
	writeErr := make(chan error, 1)
	go func() {
		defer stdin.Close()
		_, werr := stdin.Write(append(payload, '\n'))
		writeErr <- werr
	}()

	var (
		mu      sync.Mutex
		result  *EngineResult
		lastErr error
		wg      sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for scanner.Scan() {
			var ev EngineEvent
			if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
				continue
			}
			if ev.Type == "result" {
				mu.Lock()
				result = &EngineResult{Status: ev.Status, Message: ev.Message, Tokens: ev.Tokens, CostUSD: ev.Cost}
				mu.Unlock()
				continue
			}
			if ev.Message != "" {
				safeEmit(ev)
			}
		}
		if err := scanner.Err(); err != nil {
			mu.Lock()
			lastErr = err
			mu.Unlock()
		}
	}()

	waitErr := cmd.Wait()
	wg.Wait()
	<-writeErr

	mu.Lock()
	defer mu.Unlock()
	if ctx.Err() != nil {
		return EngineResult{Status: statusFromContext(ctx)}, ctx.Err()
	}
	if result != nil {
		return *result, nil
	}
	detail := strings.TrimSpace(stderr.String())
	if detail == "" && lastErr != nil {
		detail = lastErr.Error()
	}
	if detail == "" && waitErr != nil {
		detail = waitErr.Error()
	}
	if detail == "" {
		detail = "bridge exited without a result"
	}
	return EngineResult{Status: "failed"}, fmt.Errorf("engine bridge failed: %s", detail)
}

func statusFromContext(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timed_out"
	}
	return "cancelled"
}

// limitedWriter keeps only the first max bytes of stderr for diagnostics.
type limitedWriter struct {
	buf *bytes.Buffer
	max int
}

// stderrStreamer captures the bridge's diagnostics tail for error reports and
// forwards each complete line to emit as a status event. os/exec copies the
// child's stderr from a single goroutine, so synchronous emission is ordered.
type stderrStreamer struct {
	mu   sync.Mutex
	buf  *bytes.Buffer
	max  int
	rest []byte
	emit func(string)
}

func newStderrStreamer(buf *bytes.Buffer, max int, emit func(string)) *stderrStreamer {
	return &stderrStreamer{buf: buf, max: max, emit: emit}
}

func (s *stderrStreamer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if remaining := s.max - s.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			s.buf.Write(p[:remaining])
		} else {
			s.buf.Write(p)
		}
	}
	s.rest = append(s.rest, p...)
	var lines []string
	for {
		idx := bytes.IndexByte(s.rest, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSpace(string(s.rest[:idx]))
		s.rest = s.rest[idx+1:]
		if line != "" {
			lines = append(lines, line)
		}
	}
	if emit := s.emit; emit != nil {
		for _, line := range lines {
			emit(line)
		}
	}
	return len(p), nil
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if remaining := l.max - l.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			l.buf.Write(p[:remaining])
			return len(p), nil
		}
		l.buf.Write(p)
	}
	return len(p), nil
}

var _ = io.Discard
