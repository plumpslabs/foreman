package foreman

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/auth"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/mcptoolset"
	"google.golang.org/adk/v2/tool/toolutils"
)

type ADKEngine struct {
	store *Store
}

func allowedADKToolNames(allowed []string) map[string]bool {
	if len(allowed) == 0 {
		return nil
	}
	set := map[string]bool{}
	add := func(names ...string) {
		for _, n := range names {
			set[n] = true
		}
	}
	// core tools always available
	add("finish_task", "list_tasks", "send_message", "ask_human", "escalate", "memory", "repo_map", "request_spawn")
	for _, raw := range allowed {
		switch raw {
		case "file_editor", "read", "edit", "write_tests":
			add("view_file", "create_file", "replace_string", "insert_lines")
		case "grep", "glob", "search":
			add("grep_files", "glob_files", "search_index", "view_file")
		case "terminal", "run_tests", "run_commands":
			add("run_checks", "diagnose")
		case "task_tracker", "delegate", "subagents", "synthesize":
			add("create_task", "update_task", "claim_task", "spawn_agent", "delete_task", "delete_artifact")
		case "apply_patch", "diff_audit":
			add("git_ops")
		case "planning_editor", "workflow", "planning":
			add("create_plan", "delete_plan")
		case "browser", "preview":
			// no native browser tool yet
		case "register_panel", "panel":
			add("register_panel")
		}
	}
	return set
}

func NewADKEngine(store *Store) *ADKEngine {
	return &ADKEngine{store: store}
}

// mcpRenamedTool wraps an MCP tool under a stable, namespaced name so tools
// from different servers cannot collide and routing stays deterministic.
type mcpRenamedTool struct {
	inner  tool.Tool
	prefix string
}

func (t *mcpRenamedTool) Name() string        { return t.prefix }
func (t *mcpRenamedTool) Description() string { return t.inner.Description() }
func (t *mcpRenamedTool) IsLongRunning() bool { return t.inner.IsLongRunning() }

func (t *mcpRenamedTool) Declaration() *genai.FunctionDeclaration {
	d, ok := t.inner.(interface {
		Declaration() *genai.FunctionDeclaration
	})
	if !ok {
		return nil
	}
	orig := d.Declaration()
	if orig == nil {
		return nil
	}
	cp := *orig
	cp.Name = t.prefix
	return &cp
}

func (t *mcpRenamedTool) ProcessRequest(ctx agent.Context, req *model.LLMRequest) error {
	return toolutils.PackTool(req, t)
}

func (t *mcpRenamedTool) Run(ctx agent.Context, args any) (map[string]any, error) {
	r, ok := t.inner.(interface {
		Run(agent.Context, any) (map[string]any, error)
	})
	if !ok {
		return nil, fmt.Errorf("mcp tool is not runnable")
	}
	return r.Run(ctx, args)
}

// sanitizeMCPName makes an MCP server id safe to embed in a tool name.
func sanitizeMCPName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// buildMCPToolset constructs an ADK MCP toolset for the given server config.
func buildMCPToolset(srv MCPServer) (tool.Toolset, error) {
	if srv.Type == "http" {
		cfg := mcptoolset.Config{Endpoint: srv.URL}
		if srv.Token != "" {
			cfg.Auth = auth.StaticToken(srv.Token)
		}
		return mcptoolset.New(cfg)
	}
	args := srv.Args
	if args == nil {
		args = []string{}
	}
	cmd := exec.Command(srv.Command, args...)
	if srv.Cwd != "" {
		cmd.Dir = srv.Cwd
	}
	cmd.Env = os.Environ()
	for k, v := range srv.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return mcptoolset.New(mcptoolset.Config{Transport: &mcp.CommandTransport{Command: cmd}})
}

// testMCPConnection connects to a server and lists its tools, returning the
// number of tools exposed. Used by the Settings "Test connection" action.
func testMCPConnection(srv MCPServer) (int, error) {
	ts, err := buildMCPToolset(srv)
	if err != nil {
		return 0, err
	}
	if c, ok := ts.(io.Closer); ok {
		defer c.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	mock := agent.NewStrictContextMock(ctx)
	tools, err := ts.Tools(&mock)
	if err != nil {
		return 0, err
	}
	return len(tools), nil
}

func (e *ADKEngine) modelFor(req EngineRequest) (model.LLM, error) {
	switch req.Protocol {
	case "gemini", "":
		cfg := &genai.ClientConfig{APIKey: req.APIKey}
		if req.BaseURL != "" {
			cfg.HTTPClient = nil
		}
		m, err := gemini.NewModel(context.Background(), req.Model, cfg)
		if err != nil {
			return nil, err
		}
		return m, nil
	default:
		cfg := &openaimodel.ClientConfig{APIKey: req.APIKey, BaseURL: req.BaseURL}
		m, err := openaimodel.NewModel(context.Background(), req.Model, cfg)
		if err != nil {
			return nil, err
		}
		return m, nil
	}
}

// costEstimate approximates USD from prompt/candidate token counts using
// generic mid-range prices (input $3 / 1M, output $15 / 1M). Used only as a
// guard; real billing comes from the provider.
func costEstimate(prompt, cand int64) float64 {
	const inPer1e6 = 3.0
	const outPer1e6 = 15.0
	return (float64(prompt)*inPer1e6 + float64(cand)*outPer1e6) / 1e6
}

// compactArgs renders tool-call args as a short key=value list for display.
func compactArgs(a any) string {
	if a == nil {
		return ""
	}
	kv := []string{}
	switch v := a.(type) {
	case map[string]any:
		for k, val := range v {
			b, err := json.Marshal(val)
			if err != nil {
				continue
			}
			kv = append(kv, k+"="+string(b))
		}
	default:
		b, err := json.Marshal(a)
		if err != nil {
			return ""
		}
		return string(b)
	}
	sort.Strings(kv)
	out := strings.Join(kv, ", ")
	if len(out) > 60 {
		out = out[:60] + "…"
	}
	return out
}

func (e *ADKEngine) Run(ctx context.Context, req EngineRequest, emit func(EngineEvent)) (EngineResult, error) {
	return e.RunControlled(ctx, req, emit, nil)
}

func (e *ADKEngine) RunControlled(ctx context.Context, req EngineRequest, emit func(EngineEvent), control *RunControl) (EngineResult, error) {
	if req.Model == "" {
		return EngineResult{}, errors.New("model is required")
	}
	m, err := e.modelFor(req)
	if err != nil {
		return EngineResult{}, fmt.Errorf("create model: %w", err)
	}

	toolCtx := adkToolContext{
		Store:     e.store,
		RoomID:    req.RoomID,
		TaskID:    req.TaskID,
		Workspace: req.Workspace,
		AgentName: req.AgentName,
		AllowCmds: req.AllowProjectCommands,
		Emit:      emit,
		diff:      &diffTracker{},
	}
	adkTools := makeADKTools(toolCtx)
	allowed := allowedADKToolNames(req.AllowedTools)
	var tools []tool.Tool
	for _, t := range adkTools {
		tt, ok := t.(tool.Tool)
		if !ok {
			continue
		}
		if len(allowed) > 0 && !allowed[tt.Name()] {
			continue
		}
		tools = append(tools, tt)
	}
	if len(tools) == 0 {
		for _, t := range adkTools {
			if tt, ok := t.(tool.Tool); ok {
				tools = append(tools, tt)
			}
		}
	}

	// Build MCP toolsets for the server IDs enabled on this room. Tools are
	// namespaced per server (`mcp__<server>_<tool>`) to avoid collisions and
	// keep routing deterministic.
	var closers []io.Closer
	for _, srv := range e.store.MCPServersByIDs(req.MCPServerIDs) {
		ts, err := buildMCPToolset(srv)
		if err != nil {
			continue
		}
		if c, ok := ts.(io.Closer); ok {
			closers = append(closers, c)
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		mock := agent.NewStrictContextMock(ctx)
		mcpTools, terr := ts.Tools(&mock)
		cancel()
		if terr != nil {
			continue // server unreachable — keep core tools working
		}
		prefix := "mcp__" + sanitizeMCPName(srv.ID) + "_"
		for _, mt := range mcpTools {
			tools = append(tools, &mcpRenamedTool{inner: mt, prefix: prefix + mt.Name()})
		}
	}
	defer func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}()

	a, err := llmagent.New(llmagent.Config{
		Name:        "foreman_agent",
		Model:       m,
		Instruction: req.Prompt,
		Tools:       tools,
	})
	if err != nil {
		return EngineResult{}, fmt.Errorf("create agent: %w", err)
	}

	sessSvc := session.InMemoryService()
	r, err := runner.New(runner.Config{
		AppName:           "foreman",
		Agent:             a,
		SessionService:    sessSvc,
		AutoCreateSession: true,
	})
	if err != nil {
		return EngineResult{}, fmt.Errorf("create runner: %w", err)
	}

	sessionID := req.ConversationID
	if sessionID == "" {
		sessionID = req.TaskID
	}
	if sessionID == "" {
		sessionID = "session"
	}

	paused := false
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if control != nil {
		control.setPause(func() {
			paused = true
			cancel()
		})
	}

	msg := &genai.Content{Role: "user", Parts: []*genai.Part{{Text: req.Prompt}}}
	var finalText strings.Builder

	// Loop guard: cap total tool-call rounds so an agent cannot churn forever.
	// The ADK runner has no built-in iteration limit; without this, the only
	// stop is the run timeout.
	maxToolRounds := req.MaxIterations * 2
	if maxToolRounds <= 0 {
		maxToolRounds = 60
	}
	toolRounds := 0
	loopGuard := false
	budgetHit := false
	doomHit := false
	doomMsg := ""
	det := &actionDetector{}
	var totalPrompt, totalCand int64

	for ev, err := range r.Run(runCtx, "user", sessionID, msg, agent.RunConfig{}) {
		if err != nil {
			if paused {
				return EngineResult{Status: "paused", Message: finalText.String()}, nil
			}
			if errors.Is(runCtx.Err(), context.Canceled) || errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				return EngineResult{Status: statusFromContext(runCtx)}, runCtx.Err()
			}
			return EngineResult{Status: "failed", Message: finalText.String()}, err
		}
		if ev == nil {
			continue
		}
		// collect text
		var eventText strings.Builder
		var toolNames []string
		if ev.Content != nil {
			for _, p := range ev.Content.Parts {
				if p.Text != "" {
					finalText.WriteString(p.Text)
					eventText.WriteString(p.Text)
				}
				if p.FunctionCall != nil {
					toolNames = append(toolNames, p.FunctionCall.Name)
				}
			}
		}
		if len(toolNames) > 0 {
			toolRounds++
			if toolRounds > maxToolRounds {
				loopGuard = true
				break
			}
		}
		// doom-loop detection: 3+ consecutive identical tool calls (same
		// name + same args) almost always mean the model is stuck retrying.
		if len(toolNames) == 1 {
			sig := toolNames[0] + ":" + compactArgs(ev.Content.Parts[0].FunctionCall.Args)
			if stop, reason := det.observe(sig); stop {
				doomHit = true
				doomMsg = reason
				break
			}
		} else {
			det.reset()
		}
		if um := ev.UsageMetadata; um != nil {
			if um.PromptTokenCount > 0 {
				totalPrompt += int64(um.PromptTokenCount)
			}
			if um.CandidatesTokenCount > 0 {
				totalCand += int64(um.CandidatesTokenCount)
			}
			if req.BudgetUSD > 0 && costEstimate(totalPrompt, totalCand) >= req.BudgetUSD {
				budgetHit = true
				break
			}
		}
		// emit status events (never dump the raw event struct)
		if emit != nil {
			msg := strings.TrimSpace(eventText.String())
			if msg == "" && len(toolNames) > 0 {
				labels := []string{}
				for _, p := range ev.Content.Parts {
					if p.FunctionCall != nil {
						l := p.FunctionCall.Name
						if args := compactArgs(p.FunctionCall.Args); args != "" {
							l += "(" + args + ")"
						}
						labels = append(labels, l)
					}
				}
				msg = ev.Author + " → " + strings.Join(labels, ", ")
			}
			if msg == "" && ev.Author != "" {
				msg = ev.Author + " is working..."
			}
			if msg != "" {
				emit(EngineEvent{Type: "status", Message: msg})
			}
		}
		if paused {
			// best-effort: stop after current event
			break
		}
	}
	if loopGuard {
		snippet := strings.TrimSpace(finalText.String())
		if len(snippet) > 120 {
			snippet = snippet[:120] + "…"
		}
		slog.Warn("loop guard hit", "room", req.RoomID[:8], "task", req.TaskID[:8], "rounds", toolRounds, "tokens", totalPrompt+totalCand)
		msg := fmt.Sprintf("Exploration exceeded %d action rounds without finishing. Last known progress: %q. Resume with a higher iteration budget or narrow the prompt.", toolRounds, snippet)
		return EngineResult{Status: "blocked", Message: msg,
			Tokens: int(totalPrompt + totalCand), CostUSD: costEstimate(totalPrompt, totalCand)}, nil
	}
	if doomHit {
		slog.Warn("doom loop detected", "room", req.RoomID[:8], "task", req.TaskID[:8], "detail", doomMsg)
		return EngineResult{Status: "blocked", Message: doomMsg,
			Tokens: int(totalPrompt + totalCand), CostUSD: costEstimate(totalPrompt, totalCand)}, nil
	}
	if budgetHit {
		slog.Warn("budget reached", "room", req.RoomID[:8], "task", req.TaskID[:8], "cost", costEstimate(totalPrompt, totalCand), "budget", req.BudgetUSD)
		return EngineResult{Status: "blocked", Message: "Budget reached: stopped to control token spend.",
			Tokens: int(totalPrompt + totalCand), CostUSD: costEstimate(totalPrompt, totalCand)}, nil
	}
	if paused {
		return EngineResult{Status: "paused", Message: finalText.String()}, nil
	}
	if runCtx.Err() != nil {
		return EngineResult{Status: statusFromContext(runCtx)}, runCtx.Err()
	}
	msgText := strings.TrimSpace(finalText.String())
	if msgText == "" {
		msgText = "Agent run finished."
	}
	return EngineResult{
		Status: "completed", Message: msgText,
		Tokens: int(totalPrompt + totalCand), CostUSD: costEstimate(totalPrompt, totalCand),
	}, nil
}

// SDKInstalled etc for compatibility
func (e *ADKEngine) sdkInstalledCached() (bool, string) { return true, "adk go native" }
func (e *ADKEngine) diagnostics() EngineDiag {
	return EngineDiag{PythonVersion: "go", PipAvailable: false, Installed: true, ProbeDetail: "adk go native"}
}
func (e *ADKEngine) installSDK(ctx context.Context) (string, error) { return "", nil }
func (e *ADKEngine) pythonVersion() string                          { return "go (adk)" }
func (e *ADKEngine) SDKInstalled() (bool, string)                   { return true, "adk go native" }
func (e *ADKEngine) PythonVersion() string                          { return "go (adk)" }
func (e *ADKEngine) PipAvailable() bool                             { return false }
func (e *ADKEngine) InstallSDK(ctx context.Context) (string, error) { return "", nil }
