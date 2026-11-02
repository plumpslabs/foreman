package foreman

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sqlitedialect "github.com/plumpslabs/foreman/internal/foreman/sqlitedialect"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/auth"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/compaction"
	"google.golang.org/adk/v2/session/database"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/mcptoolset"
	"google.golang.org/adk/v2/tool/toolutils"
)

type ADKEngine struct {
	store *Store

	// sessMu guards lazy construction of the persistent session service. A
	// failure to open or migrate the database is cached too, so we only try
	// once per process instead of per run.
	sessMu  sync.Mutex
	sessSvc session.Service
	sessErr error

	// lockMu guards the per-conversation lock map. Two runs sharing one
	// conversation (same room + agent) are serialized so the persistent
	// session service never sees concurrent appends to a single session — its
	// stale-session guard would reject the later writer.
	lockMu   sync.Mutex
	sessLock map[string]*sync.Mutex
}

// allowedADKToolNames is the single source of truth for which tools a run can
// actually call. It must agree with what makeADKTools registers, including the
// project-command gate: run_checks and diagnose are only registered when
// ctx.AllowCmds is true. The prompt advertises tools from this set, so listing a
// gated tool here told the agent about a capability it did not have — which is
// exactly how a coordinator ended up writing "I'm not sure run_checks is
// available" mid-run.
func allowedADKToolNames(allowed []string, allowCommands bool) map[string]bool {
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
	add("finish_task", "list_tasks", "send_message", "ask_human", "escalate", "memory", "repo_map", "request_spawn", "related", "init", "confirm", "symbol_map")
	for _, raw := range allowed {
		switch raw {
		case "file_editor", "read", "edit", "write_tests":
			add("view_file", "create_file", "replace_string", "insert_lines", "edit_file", "symbol_map")
		case "grep", "glob", "search":
			add("grep_files", "glob_files", "search_index", "view_file", "related", "symbol_map")
		case "terminal", "run_tests", "run_commands":
			// Project commands are opt-in: without the setting these tools are
			// not registered at all, so advertising them would be a lie.
			if allowCommands {
				add("run_checks", "diagnose")
			}
		case "task_tracker", "delegate", "subagents", "synthesize":
			add("create_task", "update_task", "claim_task", "spawn_agent", "delete_task", "delete_artifact", "create_plan", "delete_plan")
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

// answerText returns the user-facing text of one content part.
//
// Reasoning-capable providers stream their private scratchpad as text parts with
// Thought=true ("Let me start by understanding the situation…", "I've used 7
// rounds"). Those are not the answer. The engine used to concatenate every part
// blindly, which is why room transcripts filled with the model's inner monologue
// instead of a reply. Keeping the filter in one named place makes the rule
// explicit and testable.
func answerText(p *genai.Part) string {
	if p == nil || p.Text == "" || p.Thought {
		return ""
	}
	return p.Text
}

func NewADKEngine(store *Store) *ADKEngine {
	return &ADKEngine{store: store}
}

// sessionService returns the engine-wide persistent ADK session service,
// created lazily on first use. Sessions live in <project>/.foreman/sessions.db
// (SQLite via the pure-Go glebarez driver, so the binary stays CGO-free).
// A failure to open or migrate the database is cached and yields nil; callers
// then fall back to the in-memory service, so a read-only disk must not break
// runs.
func (e *ADKEngine) sessionService() session.Service {
	e.sessMu.Lock()
	defer e.sessMu.Unlock()
	if e.sessSvc != nil || e.sessErr != nil {
		return e.sessSvc
	}
	svc, err := e.openSessionService()
	if err != nil {
		e.sessErr = err
		slog.Warn("persistent session service unavailable; using in-memory", "err", err)
		return nil
	}
	e.sessSvc = svc
	return svc
}

func (e *ADKEngine) openSessionService() (session.Service, error) {
	path := e.store.SessionDBPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	svc, err := database.NewSessionService(sqlitedialect.Open(path))
	if err != nil {
		return nil, fmt.Errorf("open session db: %w", err)
	}
	if err := database.AutoMigrate(svc); err != nil {
		return nil, fmt.Errorf("migrate session db: %w", err)
	}
	return svc, nil
}

// conversationLock returns a release func that serializes runs sharing a
// conversation (same room + agent). Holding it for the whole run is safe:
// every run terminates through its iteration cap or timeout, and ask_human
// never blocks the run loop.
func (e *ADKEngine) conversationLock(conversationID string) func() {
	e.lockMu.Lock()
	if e.sessLock == nil {
		e.sessLock = map[string]*sync.Mutex{}
	}
	mu := e.sessLock[conversationID]
	if mu == nil {
		mu = &sync.Mutex{}
		e.sessLock[conversationID] = mu
	}
	e.lockMu.Unlock()
	mu.Lock()
	return func() { mu.Unlock() }
}

// pickSessionService chooses the session backend for one run: persistent by
// default (settings opt-out), falling back to in-memory when the persistent
// store is unavailable, and always serializing same-conversation runs.
func (e *ADKEngine) pickSessionService(conversationID string) (session.Service, func()) {
	sessSvc := session.InMemoryService()
	if !e.store.Settings().DisableSessionPersistence {
		sessSvc = e.sessionService()
	}
	if sessSvc == nil {
		sessSvc = session.InMemoryService()
	}
	return sessSvc, e.conversationLock(conversationID)
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

// newPromptAgent builds the runner agent for one engine call. The dynamically
// composed prompt contains literal braces (JSON examples, `{word}` output
// templates, webhook paths like /{roomID}). ADK treats `{identifier}` in a
// plain Instruction as a session-state placeholder and fails the call with
// "state key does not exist" when no such key exists — an InstructionProvider
// bypasses state templating entirely, so the prompt reaches the model verbatim.
func newPromptAgent(prompt string, m model.LLM, tools []tool.Tool) (agent.Agent, error) {
	return llmagent.New(llmagent.Config{
		Name:  "foreman_agent",
		Model: m,
		InstructionProvider: func(_ agent.ReadonlyContext) (string, error) {
			return prompt, nil
		},
		Tools: tools,
	})
}

// costEstimate approximates USD from prompt/candidate token counts using// generic mid-range prices (input $3 / 1M, output $15 / 1M). Used only as a
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

// argsSignature returns a stable, full fingerprint of tool-call args for loop
// detection. Unlike compactArgs (which truncates for display), this hashes the
// entire argument object so two calls that differ only past the display prefix
// are never mistaken for a repeated action.
func argsSignature(a any) string {
	if a == nil {
		return ""
	}
	b, err := json.Marshal(a)
	if err != nil {
		return fmt.Sprintf("%v", a)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
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
		// The signal tracker feeds the per-project co-usage graph; finalize
		// flushes the learned edges whatever way this run ends (even on
		// error/pause), so the graph keeps growing.
		signals: newSignalTracker(req.RoomID),
	}
	defer toolCtx.signals.finalize(e.store)
	adkTools := makeADKTools(toolCtx)
	allowed := allowedADKToolNames(req.AllowedTools, req.AllowProjectCommands)
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

	a, err := newPromptAgent(req.Prompt, m, tools)
	if err != nil {
		return EngineResult{}, fmt.Errorf("create agent: %w", err)
	}

	// Loop guard: cap total tool-call rounds so an agent cannot churn forever.
	// The ADK runner has no built-in iteration limit; without this, the only
	// stop is the run timeout.
	maxToolRounds := req.MaxIterations * 2
	if maxToolRounds <= 0 {
		maxToolRounds = 60
	}
	// The governor is a soft guardrail layered on top of the hard cap: it blocks
	// further read-only calls once the read budget is spent, so the model is
	// pushed to act or summarize instead of silently churning until the cap.
	// The role directive (e.g. the coordinator mandate) rides along so native
	// cut-off messages repeat the same instruction the prompt gave.
	guard := newLoopGuard(req.ReadOnlyBudget, maxToolRounds, req.RoleDirective)
	guardCfg, err := guard.pluginConfig()
	if err != nil {
		return EngineResult{}, fmt.Errorf("create loop guard: %w", err)
	}

	// Context compaction: summarize older events before a model call once the
	// prompt grows past the threshold, so a long exploratory run does not keep
	// re-sending an ever-larger context. The runner installs an LLM summarizer
	// over this agent's model when none is supplied.
	var compactionCfg *compaction.Config
	if req.ContextTokenLimit > 0 {
		compactionCfg = &compaction.Config{
			TokenThreshold:     req.ContextTokenLimit,
			EventRetentionSize: req.ContextRetention,
		}
	}

	sessionID := req.ConversationID
	if sessionID == "" {
		sessionID = req.TaskID
	}
	if sessionID == "" {
		sessionID = "session"
	}

	// Persistent ADK sessions: a conversation (room + agent) survives restarts
	// in <project>/.foreman/sessions.db, so a follow-up prompt continues the
	// same thread instead of starting cold. The per-conversation lock
	// serializes concurrent runs on the same session; it is released on return.
	sessSvc, unlock := e.pickSessionService(sessionID)
	defer unlock()

	r, err := runner.New(runner.Config{
		AppName:           "foreman",
		Agent:             a,
		SessionService:    sessSvc,
		AutoCreateSession: true,
		PluginConfig:      guardCfg,
		Compaction:        compactionCfg,
	})
	if err != nil {
		return EngineResult{}, fmt.Errorf("create runner: %w", err)
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

	toolRounds := 0
	loopGuard := false
	loopGuardReason := ""
	forcedAt := -1
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
		//
		// Thought parts are the model's private reasoning, not its answer (see
		// answerText). Reasoning-capable providers stream them with
		// Thought=true, and the engine used to concatenate every part blindly —
		// which is why room transcripts filled up with "Let me start by
		// understanding the situation… I've used 7 rounds" instead of a reply.
		var eventText strings.Builder
		var toolNames []string
		if ev.Content != nil {
			for _, p := range ev.Content.Parts {
				if text := answerText(p); text != "" {
					finalText.WriteString(text)
					eventText.WriteString(text)
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
		// Doom/cycle detection runs per tool call, each with its own args, and
		// the whole round is fed at once so the window stays continuous across
		// multi-tool batches (the old full reset let churn hidden between
		// batches evade the detector). Keeping the args is essential: two
		// batches with the same tool names but different files are different
		// work, not a loop.
		sigs := make([]string, 0, len(toolNames))
		if ev.Content != nil {
			for _, p := range ev.Content.Parts {
				if p.FunctionCall == nil {
					continue
				}
				sigs = append(sigs, p.FunctionCall.Name+":"+argsSignature(p.FunctionCall.Args))
			}
		}
		if stop, reason := det.observeRound(sigs); stop {
			doomHit = true
			doomMsg = reason
			break
		}
		// After the governor spends the read budget it blocks further reads and
		// asks the model to act or summarize. If the model ignores that and
		// keeps emitting tool calls, stop a few rounds later instead of draining
		// the rest of the hard cap.
		if guard.hasForced() {
			if forcedAt < 0 {
				forcedAt = toolRounds
			} else if toolRounds-forcedAt > 3 {
				loopGuard = true
				loopGuardReason = guard.forcedReason() + " The model kept calling tools instead of summarizing."
				break
			}
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
		if len(snippet) > 240 {
			snippet = snippet[:240] + "…"
		}
		stats := guard.stats()
		slog.Warn("loop guard hit", "room", req.RoomID[:8], "task", req.TaskID[:8], "rounds", toolRounds, "tokens", totalPrompt+totalCand, "tools", stats)
		msg := fmt.Sprintf("Exploration reached the %d-round limit without finishing.", toolRounds)
		if loopGuardReason != "" {
			msg += " " + loopGuardReason
		}
		if stats != "" {
			msg += fmt.Sprintf(" Tool usage: %s.", stats)
		}
		if snippet != "" {
			msg += fmt.Sprintf(" Partial findings: %q.", snippet)
		} else {
			msg += " No written findings were produced."
		}
		msg += " Resume with a clearer prompt, a higher iteration budget, or delegate to a specialist."
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
