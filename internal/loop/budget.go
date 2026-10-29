package loop

import (
	"regexp"
	"strings"
	"time"
)

// SetToolDescBudget caps how many characters of tool descriptions are passed
// in system prompts. 0 means unlimited.
func (e *Engine) SetToolDescBudget(n int) {
	if n < 0 {
		n = 0
	}
	e.toolDescBudget = n
}

// Tool-only budget: a model that keeps calling tools without answering is
// nudged EARLY (so a rabbit-hole exploration like "search the schema for more
// models" is cut before it burns a dollar of tokens) and aborted shortly
// after. A few rounds of pure tool calls is plenty for most tasks (a big
// monorepo overview, an LSP-warning sweep); the first reminder lands early so
// the agent answers instead of reading itself in circles.
const (
	// toolWarnRounds — first "stop and answer" reminder.
	toolWarnRounds = 8
	// toolFinalWarnRounds — second, firmer warning.
	toolFinalWarnRounds = 12
	// maxToolOnlyRounds — abort once a spinning model stalls. Still well below
	// the 25-iteration cap.
	maxToolOnlyRounds = 16
	// maxToolOnlyAbsolute — unconditional abort even for a model that keeps
	// discovering new files (freedom is bounded, never infinite). 20 leaves
	// room for the final-warning pattern to complete instead of being cut right after read.
	maxToolOnlyAbsolute = 20
	// finalWarnHardStop — after the FINAL WARNING the model may make at most this
	// many more tool rounds before it is forced to synthesize.
	finalWarnHardStop = 2
	// exploredWarnCap — once the agent has read this many DISTINCT files/paths in
	// a row without answering, accelerate the final warning.
	exploredWarnCap = 18
)

// Read-only budget: separates exploration (reads) from execution (edits/writes)
// so a model that reads multiple files still has budget to edit. Without this,
// aggressive exploration starves execution and the agent reports findings without shipping code.
const (
	// readOnlyWarnRounds — first "stop reading, start editing" reminder.
	readOnlyWarnRounds = 8
	// readOnlyHardStop — hard cap on consecutive read-only rounds before advisory.
	readOnlyHardStop = 14
	// minWriteBudget — minimum number of tool rounds reserved for mutations
	minWriteBudget = 6
)

// isReadOnlyTool reports whether a tool is pure exploration (no side effects).
// Used to track the separate read-only budget.
func isReadOnlyTool(name string) bool {
	switch name {
	case "read_file", "list_dir", "grep", "glob", "search_code", "fetch_url", "web_search", "doc_lookup":
		return true
	}
	return false
}

// CalculateAdaptiveToolBudget dynamically scales the tool budget based on
// prompt complexity, active mode, and task keywords.
// complexityTier classifies a task's effort by prompt signals so the engine
// budgets iterations proportionally — a one-line typo fix must not be granted
// the same 25-round runway as a cross-module refactor. Deterministic and free
// (no LLM): keyword + length signals, matching the spirit of rule b7
// (PROPORTIONALITY). The autonomous extension path still rescues a
// misclassified task, so a wrong tier never traps a genuinely big task.
type complexityTier int

const (
	tierSimple complexityTier = iota
	tierMedium
	tierComplex
)

var (
	// Structural signals of complex multi-part specification
	multiStepListRe = regexp.MustCompile(`(?m)^(\s*[-*+]|\s*\d+\.|\s*\[[ xX]\])\s+`)
	filePathRe      = regexp.MustCompile(`[\w.-]+/(?:[\w.-]+/)*[\w.-]+\.\w+`)
	codeFenceRe     = regexp.MustCompile("```")

	// Construction & modification roots (multilingual: English, Indonesian, developer stems).
	// Word boundaries ensure matching meaningful words/stems rather than arbitrary letter collisions.
	complexStemRe = regexp.MustCompile(`(?i)\b(implement\w*|implements?ikan\w*|imolements?\w*|refactor\w*|migrat\w*|integrat\w*|architect\w*|arsitektur\w*|schema\w*|skema\w*|feature\w*|fitur\w*|service\w*|controller\w*|endpoint\w*|webhook\w*|pipeline\w*|blueprint\w*|step-by-step|bertahap\w*|rombak\w*|rancang\w*|buatkan\w*|bikin\w*|tambah\w*|perbaiki\w*|monorepo\w*|cross-module\w*|end-to-end\w*|audit\w*|rewrite\w*|backward-compat\w*)\b`)

	// Trivial / Informational roots
	simpleStemRe = regexp.MustCompile(`(?i)\b(typo|rename|spelling|ejaan|format|comment|komentar|docstring|fix the doc)\b`)
)

// classifyTaskComplexity scores a user query into simple / medium / complex
// using language-agnostic structural signals, stem taxonomy, and word volume.
func classifyTaskComplexity(query string) complexityTier {
	p := strings.TrimSpace(query)
	if p == "" {
		return tierMedium
	}
	words := len(strings.Fields(p))

	hasCodeFences := codeFenceRe.MatchString(p)
	hasFilePaths := filePathRe.MatchString(p)
	hasMultiStepList := multiStepListRe.MatchString(p)

	complexMatches := complexStemRe.FindAllString(p, -1)
	complexHits := len(complexMatches)

	simpleMatches := simpleStemRe.FindAllString(p, -1)
	simpleHits := len(simpleMatches)

	// Structural complexity: code snippets, multi-step checklists, or files with modification actions
	if hasCodeFences || (hasFilePaths && complexHits >= 1) || hasMultiStepList {
		return tierComplex
	}

	// Semantic & lexical volume complexity
	if complexHits >= 2 || (complexHits >= 1 && words >= 20) || words >= 30 {
		return tierComplex
	}

	// Strict & safe simple gate: only classified as simple when prompt is short (<= 8 words),
	// has zero file paths, zero code fences, zero multi-step lists, zero complex stems,
	// and matches an explicit minor fix stem.
	if simpleHits >= 1 && complexHits == 0 && words <= 8 && !hasFilePaths && !hasCodeFences {
		return tierSimple
	}

	return tierMedium
}

// iterationsForComplexity maps a task tier to its per-turn iteration budget.
// Simple tasks get a tight runway (finish fast, no ceremony); complex tasks
// get 25; autonomous momentum extension adds +15 repeatedly when
// active tasks or plans need more room to finish.
func iterationsForComplexity(t complexityTier) int {
	switch t {
	case tierSimple:
		return 12
	case tierComplex:
		return 25
	default:
		return 24
	}
}

// ComplexityTier is the exported alias for classifyTaskComplexity's result,
// so the UI layer can derive an adaptive timeout from the same signal.
type ComplexityTier = complexityTier

// Exported tier constants for use by callers outside this package.
const (
	TierSimple  = tierSimple
	TierMedium  = tierMedium
	TierComplex = tierComplex
)

// ClassifyTaskComplexity is the exported wrapper around classifyTaskComplexity.
func ClassifyTaskComplexity(query string) ComplexityTier {
	return classifyTaskComplexity(query)
}

// timeoutForComplexity maps a task tier to a wall-clock timeout for the turn
// watchdog. Simple tasks finish quickly; complex cross-module refactors
// get ample time.
func timeoutForComplexity(t complexityTier) time.Duration {
	switch t {
	case tierSimple:
		return 5 * time.Minute
	case tierComplex:
		return 20 * time.Minute
	default:
		return 12 * time.Minute
	}
}

// TimeoutForComplexity is the exported wrapper so the UI can derive an
// adaptive wall-clock timeout from the engine's complexity tier.
func TimeoutForComplexity(t ComplexityTier) time.Duration {
	return timeoutForComplexity(t)
}

// explorationBudget returns the adaptive warn, final warn, and absolute caps
// based on the active mode (PLANNER/MINER vs BUILDER) and prompt complexity.
func (e *Engine) explorationBudget() (warnRounds, finalWarnRounds, maxAbsolute, exploredCap int) {
	mode := e.Mode()
	prompt := e.context.LastUserPrompt()
	tier := classifyTaskComplexity(prompt)

	if mode == "PLANNER" || mode == "MINER" {
		// Deep research & architectural discovery modes need wider runway.
		if tier == tierComplex {
			return 16, 22, 28, 30
		}
		return 12, 18, 24, 22
	}

	// BUILDER mode:
	if tier == tierComplex {
		return 14, 18, 24, 24
	}
	return toolWarnRounds, toolFinalWarnRounds, maxToolOnlyAbsolute, exploredWarnCap
}

func CalculateAdaptiveToolBudget(prompt string, mode string) int {
	base := 18
	if mode == "MINER" || mode == "PLANNER" {
		base = 24
	}
	tier := classifyTaskComplexity(prompt)
	switch tier {
	case tierComplex:
		base += 6
	case tierMedium:
		base += 2
	}
	if base > 30 {
		return 30
	}
	if base < 12 {
		return 12
	}
	return base
}

// maxParallelReadOnlyTools caps how many read-only tools execute concurrently
// in one round. Read-only calls are stateless, so parallel execution cuts
// multi-read rounds from ~N×latency to ~N/cap×latency — the biggest per-turn
// speedup available without changing model behavior. The cap keeps a 20-tool
// batch from hammering the disk, the network, or the LSP server at once.
const maxParallelReadOnlyTools = 4

// isParallelReadOnly reports whether a tool is safe to execute concurrently:
// read-only, stateless, no interactive prompt, no session-affecting side
// effects. Everything else (write_file, edit_file, delete_file, bash,
// ask_user, git, undo, review_changes, memory) stays sequential so side
// effects and user prompts keep their order.
func isParallelReadOnly(name string) bool {
	switch name {
	case "read_file", "list_dir", "grep", "glob", "search_code", "fetch_url", "web_search", "doc_lookup":
		return true
	}
	return false
}

// isMutatingTool reports whether a tool has filesystem/process side effects
// that warrant early-exit-on-error (a failure here usually invalidates
// downstream calls in the same round).
func isMutatingTool(name string) bool {
	switch name {
	case "write_file", "edit_file", "delete_file", "bash", "git", "undo",
		"create_directory", "rename_file", "lsp_autofix":
		return true
	}
	return false
}

// taskType classifies the user's prompt into a task category for adaptive
// tool filtering. Different task types benefit from different tool subsets:
// bug fixes don't need memory or web search; code exploration doesn't need
// edit tools. This shrinks the schema payload and prevents the model from
// being tempted by irrelevant tools — the #1 efficiency gain for short tasks.
type taskType int

const (
	taskGeneric   taskType = iota
	taskBugFix             // error, crash, not working, regression
	taskExplore            // explain, what does, how does, architecture
	taskImplement          // add, create, implement, build
	taskRefactor           // refactor, clean up, simplify, optimize
	taskTest               // test, coverage, spec, assertion
)

func classifyTaskType(query string) taskType {
	p := strings.ToLower(strings.TrimSpace(query))

	// Bug fix: error signals, crash, not working
	for _, kw := range []string{"error", "crash", "panic", "failing", "broken", "not working", "regression", "bug", "fix the"} {
		if strings.Contains(p, kw) {
			return taskBugFix
		}
	}
	// Exploration: explain, what does, how does, architecture
	for _, kw := range []string{"explain", "what does", "how does", "architecture", "overview", "why does", "what is", "how is"} {
		if strings.Contains(p, kw) {
			return taskExplore
		}
	}
	// Implementation: add, create, implement, build
	for _, kw := range []string{"add", "create", "implement", "build", "new feature", "add support", "introduce"} {
		if strings.Contains(p, kw) {
			return taskImplement
		}
	}
	// Refactor: refactor, clean up, simplify, optimize
	for _, kw := range []string{"refactor", "clean up", "simplify", "optimize", "deduplicate", "restructure"} {
		if strings.Contains(p, kw) {
			return taskRefactor
		}
	}
	// Test: test, coverage, spec
	for _, kw := range []string{"test", "coverage", "spec", "assertion", "unit test", "integration test"} {
		if strings.Contains(p, kw) {
			return taskTest
		}
	}
	return taskGeneric
}

// taskExcludeTools maps task types to tools that should be EXCLUDED for that
// task type. This reduces schema noise and prevents the model from wasting
// tool calls on irrelevant actions. E.g., a bug-fix task doesn't need
// memory, web_search, or doc_lookup — only read, edit, bash, grep.
var taskExcludeTools = map[taskType]map[string]bool{
	taskBugFix: {
		"memory": true, "web_search": true, "doc_lookup": true,
		"code_outline": true, "blast_radius": true, "impact": true,
	},
	taskExplore: {
		"write_file": true, "edit_file": true, "delete_file": true,
		"memory": true, "lsp_fix": true, "lsp_rename": true,
	},
	taskTest: {
		"memory": true, "web_search": true, "doc_lookup": true,
		"code_outline": true, "blast_radius": true,
	},
}
