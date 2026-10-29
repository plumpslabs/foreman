package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	bcontext "github.com/plumpslabs/bro-code/internal/context"
	"github.com/plumpslabs/bro-code/internal/learn"
	"github.com/plumpslabs/bro-code/internal/loop"
	"github.com/plumpslabs/bro-code/internal/lsp"
	"github.com/plumpslabs/bro-code/internal/memory"
	"github.com/plumpslabs/bro-code/internal/prompt"
	"github.com/plumpslabs/bro-code/internal/provider"
	"github.com/plumpslabs/bro-code/internal/repo"
	"github.com/plumpslabs/bro-code/internal/search"
	"github.com/plumpslabs/bro-code/internal/skill"
	"github.com/plumpslabs/bro-code/internal/store"
	"github.com/plumpslabs/bro-code/internal/subagent"
	"github.com/plumpslabs/bro-code/internal/tool"
)

// runHeadlessTask executes an autonomous agent turn without BubbleTea TUI.
// Output (streaming text, tool calls, file diffs) is printed directly to stdout.
func runHeadlessTask(
	promptText string,
	cfg provider.AppConfig,
	activeProvider provider.DetectedProvider,
	activeModel string,
	adapter provider.ProviderAdapter,
	tools *tool.Registry,
	ctxMgr *bcontext.Manager,
	st *store.Store,
	sessionID string,
	budgetUSD float64,
	cwd string,
	lspMgr *lsp.Manager,
	scoutMgr *subagent.ScoutManager,
) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	engine := loop.NewEngine(adapter, tools, ctxMgr, activeModel)
	engine.SetMode("BUILDER")
	engine.SetBudgetUSD(budgetUSD)

	projCtx := search.BuildProjectContext(cwd)
	engine.SetProjectContext(projCtx.String())

	usage := repo.NewUsage(cwd)
	if rmap := repo.BuildMap(cwd, usage); rmap != nil {
		engine.SetRepoMap(rmap.String())
		stackHints := make([]prompt.Stack, 0, len(rmap.Stacks))
		for _, s := range rmap.Stacks {
			stackHints = append(stackHints, prompt.Stack{Name: s.Name, Files: s.Files})
		}
		engine.SetDetectedStacks(stackHints)
	}

	memStore := memory.NewStore(cwd)
	engine.SetMemoryStore(memStore)
	tools.SetMemoryStore(memStore)

	skill.EnsureGlobalDefaultsInstalled()
	allSkills := skill.NewLoader(cwd).All()
	var skillEntries []prompt.SkillEntry
	for _, s := range allSkills {
		skillEntries = append(skillEntries, prompt.SkillEntry{
			Name:        s.Name,
			Description: s.Description,
			Path:        s.Path,
		})
	}
	engine.SetSkillCatalog(skillEntries)

	engine.SetTuning(prompt.LoadTuning(prompt.DefaultTuningPath()))
	if lp := learn.DefaultPath(); lp != "" {
		engine.SetLearner(learn.NewLearner(lp))
	}
	if lspMgr != nil {
		engine.SetLSPStatus(len(lspMgr.AvailableServers()))
	}
	engine.SetScoutManager(scoutMgr)

	for _, d := range provider.AutoDetect(cfg) {
		if activeProvider.Info.ID != "" && d.Info.ID == activeProvider.Info.ID {
			continue
		}
		var a provider.ProviderAdapter
		switch {
		case d.Info.ID == "opencode":
			a = provider.NewOpenCodeAdapter()
		case d.Info.Protocol == "anthropic":
			a = provider.NewAnthropicAdapter(d.Info.DefaultBaseURL, d.APIKey)
		default:
			a = provider.NewOpenAIAdapter(d.Info.DefaultBaseURL, d.APIKey)
		}
		m := ""
		if len(d.Info.DefaultModels) > 0 {
			m = d.Info.DefaultModels[0]
		}
		if m == "" {
			m = "deepseek-v4-flash-free"
		}
		engine.AddFallback(loop.Fallback{ID: d.Info.ID, Protocol: d.Info.Protocol, Adapter: a, Model: m})
	}
	engine.SetPrimaryIdentity(activeProvider.Info.ID, activeProvider.Info.Protocol)
	engine.SetFallbackPolicy(cfg.FallbackPolicy)

	// Stream assistant content directly to stdout
	engine.SetStreamHandler(func(delta string) {
		fmt.Print(delta)
	})

	// Subscribe to structured agent core events
	engine.Subscribe(func(ev loop.AgentEvent) {
		switch ev.Type {
		case loop.EventToolCall:
			if tc, ok := ev.Payload.(loop.ToolCallPayload); ok {
				fmt.Printf("\n⚙️ [%s] %s\n", tc.ToolName, tc.Arguments)
			}
		case loop.EventFilePatch:
			if fp, ok := ev.Payload.(loop.FilePatchPayload); ok {
				fmt.Printf("\n📝 [diff] %s\n%s\n", fp.Path, fp.Diff)
			}
		case loop.EventVerification:
			if vp, ok := ev.Payload.(loop.VerificationPayload); ok {
				if vp.Passed {
					fmt.Printf("\n✅ Verification passed (%s)\n", vp.Command)
				} else {
					fmt.Printf("\n⚠️ Verification failed (%s)\n", vp.Command)
				}
			}
		case loop.EventModeSwitched:
			if mode, ok := ev.Payload.(string); ok {
				fmt.Printf("\n🔄 Mode switched to %s\n", mode)
			}
		}
	})

	fmt.Printf("⚡ BroCode Headless Core • Model: %s • Session: %s\n\n", activeModel, sessionID)

	result, err := engine.RunTask(ctx, promptText)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Task error: %v\n", err)
		return 1
	}

	fmt.Println()
	if result != nil {
		fmt.Printf("\n──────────────────────────────────────────────────\n")
		fmt.Printf("✨ Completed in %d round(s) (%s)\n", result.Rounds, result.Duration.Round(time.Millisecond))
		if len(result.ModifiedFiles) > 0 {
			fmt.Printf("📁 Modified files (%d):\n", len(result.ModifiedFiles))
			for _, f := range result.ModifiedFiles {
				fmt.Printf("   • %s\n", f)
			}
		}
		if result.TotalTokens > 0 {
			fmt.Printf("📊 Tokens: %d | Spend: $%.4f\n", result.TotalTokens, result.CostUSD)
		}
	}

	// Capture memory at session end
	if st != nil {
		if events, err := st.GetSessionEvents(sessionID); err == nil && len(events) > 0 {
			mem := memory.NewStore(cwd)
			_ = mem.CaptureSession(sessionID, events)
		}
		_, _ = bcontext.Reflect(st)
	}

	return 0
}
