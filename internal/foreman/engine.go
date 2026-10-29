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
	"time"
)

//go:embed bridge/agent_bridge.py
var bridgeScript []byte

// EngineRequest describes one bounded OpenHands run executed by the bridge.
type EngineRequest struct {
	Workspace            string
	Prompt               string
	Protocol             string
	BaseURL              string
	APIKey               string
	Model                string
	MaxIterations        int
	BudgetUSD            float64
	AllowProjectCommands bool
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

// Run executes one bridge run, forwarding events to emit until the process
// finishes or ctx is cancelled. It returns the terminal result or an error
// when the bridge never produced one.
func (e *Engine) Run(ctx context.Context, req EngineRequest, emit func(EngineEvent)) (EngineResult, error) {
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
	cmd.Stderr = &limitedWriter{buf: &stderr, max: 8 << 10}

	if err := cmd.Start(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return EngineResult{Status: statusFromContext(ctx)}, ctx.Err()
		}
		return EngineResult{}, fmt.Errorf("start engine bridge (%s): %w", e.python, err)
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
			if ev.Message != "" && emit != nil {
				emit(ev)
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
