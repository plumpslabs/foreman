package foreman

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

const SchemaVersion = 3

type WorkspaceState struct {
	SchemaVersion int              `json:"schemaVersion"`
	ProjectPath   string           `json:"projectPath"`
	Rooms         []Room           `json:"rooms"`
	Teams         []Team           `json:"teams"`
	Agents        []Agent          `json:"agents"`
	Templates     []TeamTemplate   `json:"templates"`
	Providers     []ProviderConfig `json:"providers"`
	Tasks         []Task           `json:"tasks"`
	Runs          []AgentRun       `json:"runs"`
	Skills        []Skill          `json:"skills"`
	Settings      Settings         `json:"settings"`
}

type Settings struct {
	Theme                string  `json:"theme"`
	EffortMode           string  `json:"effortMode"`
	DefaultEffort        string  `json:"defaultEffort"`
	MaxConcurrentRuns    int     `json:"maxConcurrentRuns"`
	MaxWorkersPerTask    int     `json:"maxWorkersPerTask"`
	MaxDelegationDepth   int     `json:"maxDelegationDepth"`
	MaxRetries           int     `json:"maxRetries"`
	MaxExecutionSeconds  int     `json:"maxExecutionSeconds"`
	BudgetUSD            float64 `json:"budgetUsd"`
	RequireRunApproval   bool    `json:"requireRunApproval"`
	AllowProjectCommands bool    `json:"allowProjectCommands"`
}

type ProviderConfig struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Protocol     string   `json:"protocol"`
	BaseURL      string   `json:"baseUrl"`
	Models       []string `json:"models"`
	DefaultModel string   `json:"defaultModel"`
	HasAPIKey    bool     `json:"hasApiKey"`
	Enabled      bool     `json:"enabled"`
}

type Skill struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Description  string      `json:"description"`
	Version      int         `json:"version"`
	Instructions string      `json:"instructions"`
	Enabled      bool        `json:"enabled"`
	Mode         string      `json:"mode"` // "active" (injected into agent context) or "passive" (reference only)
	Files        []SkillFile `json:"files"`
}

// SkillFile is one markdown (or text) file inside a skill bundle. Paths are
// relative to the skill root and may contain sub-directories.
type SkillFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

const (
	SkillModeActive  = "active"
	SkillModePassive = "passive"
)

type Task struct {
	ID             string    `json:"id"`
	RoomID         string    `json:"roomId"`
	TeamID         string    `json:"teamId,omitempty"`
	AgentName      string    `json:"agentName,omitempty"`
	Title          string    `json:"title"`
	Description    string    `json:"description"`
	Status         string    `json:"status"`
	Priority       string    `json:"priority"`
	Effort         string    `json:"effort"`
	Dependencies   []string  `json:"dependencies"`
	Approved       bool      `json:"approved"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	LastRunID      string    `json:"lastRunId,omitempty"`
	ManualOverride bool      `json:"manualOverride,omitempty"`
}

type AgentRun struct {
	ID        string     `json:"id"`
	TaskID    string     `json:"taskId"`
	RoomID    string     `json:"roomId"`
	Provider  string     `json:"provider"`
	Model     string     `json:"model"`
	Status    string     `json:"status"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   time.Time  `json:"endedAt,omitempty"`
	Result    string     `json:"result,omitempty"`
	Error     string     `json:"error,omitempty"`
	Events    []RunEvent `json:"events"`
	CostUSD   *float64   `json:"costUsd,omitempty"`
	Tokens    *int       `json:"tokens,omitempty"`
}

type RunEvent struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
}

type Room struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Goal      string        `json:"goal"`
	TeamIDs   []string      `json:"teamIds"`
	Status    string        `json:"status"`
	Messages  []ChatMessage `json:"messages"`
	CreatedAt time.Time     `json:"createdAt"`
}

type Team struct {
	ID           string    `json:"id"`
	TemplateID   string    `json:"templateId"`
	Name         string    `json:"name"`
	Purpose      string    `json:"purpose"`
	MainAgent    string    `json:"mainAgent"`
	Agents       []Agent   `json:"agents"`
	Instructions string    `json:"instructions"`
	CreatedAt    time.Time `json:"createdAt"`
}

type Agent struct {
	ID            string   `json:"id,omitempty"`
	Name          string   `json:"name"`
	Role          string   `json:"role"`
	Description   string   `json:"description,omitempty"`
	Instructions  string   `json:"instructions,omitempty"`
	ProviderID    string   `json:"providerId,omitempty"`
	Model         string   `json:"model,omitempty"`
	Effort        string   `json:"effort,omitempty"`
	Skills        []string `json:"skills,omitempty"`
	Tools         []string `json:"tools,omitempty"`
	RequireReview bool     `json:"requireReview,omitempty"`
}

type ChatMessage struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
}

func NewChatMessage(role, content string) ChatMessage {
	return ChatMessage{ID: uuid.NewString(), Role: role, Content: content, CreatedAt: time.Now().UTC()}
}

type TeamTemplate struct {
	ID           string  `json:"id"`
	Version      int     `json:"version"`
	Name         string  `json:"name"`
	Purpose      string  `json:"purpose"`
	MainAgent    string  `json:"mainAgent"`
	Agents       []Agent `json:"agents"`
	Instructions string  `json:"instructions"`
}

func DefaultTemplates() []TeamTemplate {
	return []TeamTemplate{
		{
			ID: "product-team", Version: 1, Name: "Product Team",
			Purpose:   "End-to-end feature planning, architectural breakdown, resilient implementation, and regression prevention.",
			MainAgent: "Atlas",
			Agents: []Agent{
				{
					Name:         "Atlas",
					Role:         "Main agent · Planner & Coordinator",
					Description:  "Breaks down complex requests, tracks dependency graph, validates constraints, and orchestrates team members.",
					Instructions: "You are the team coordinator. Always inspect existing architecture before delegating. Break tasks into bounded independent units, enforce safety checks, and never make sweeping destructive edits.",
					Skills:       []string{"Planning & decomposition", "Safe code review", "Git worktree workflow"},
					Tools:        []string{"read", "search", "delegate", "synthesize"},
				},
				{
					Name:          "Mira",
					Role:          "Backend engineer",
					Description:   "Implements core business logic, API schemas, and robust data persistence with error handling.",
					Instructions:  "Focus strictly on bounded backend tasks. Write clean, idiomatic code, ensure backward compatibility, and avoid touching unrelated modules.",
					Skills:        []string{"Backend architecture", "Database migrations", "Error handling"},
					Tools:         []string{"read", "search", "edit", "run_tests"},
					RequireReview: true,
				},
				{
					Name:         "Quinn",
					Role:         "QA & Test Analyst",
					Description:  "Analyzes edge cases, generates thorough test suites, and verifies regressions without modifying production code.",
					Instructions: "Perform read-only code analysis and write targeted regression tests. Ensure all edge cases and failure paths are covered.",
					Skills:       []string{"Test strategy", "Regression analysis", "Playwright & Unit testing"},
					Tools:        []string{"read", "search", "write_tests", "run_tests"},
				},
			},
			Instructions: "Decompose requirements into bounded milestones. Parallelize read-only investigations, serialize critical code edits, and require test validation before task completion.",
		},
		{
			ID: "frontend-team", Version: 1, Name: "Frontend & UI Team",
			Purpose:   "Modern UI/UX implementation, accessibility standards, responsive layouts, and reactive client state.",
			MainAgent: "Nova",
			Agents: []Agent{
				{
					Name:         "Nova",
					Role:         "Lead Frontend Architect",
					Description:  "Designs responsive component hierarchy, manages design tokens, and ensures UI state consistency.",
					Instructions: "Enforce clean component modularity, maintain strict accessibility guidelines, and ensure fluid responsive behavior across viewports.",
					Skills:       []string{"Frontend implementation", "Design systems", "A11y compliance"},
					Tools:        []string{"read", "search", "edit", "preview"},
				},
				{
					Name:          "Pixel",
					Role:          "UI Specialist & Stylist",
					Description:   "Crafts polished CSS layouts, subtle transitions, dark/light themes, and mobile-friendly adaptations.",
					Instructions:  "Focus on responsive layout rules, CSS variables, visual hierarchy, and component polish. Avoid breaking existing utility classes.",
					Skills:        []string{"CSS architecture", "Responsive polish", "Micro-interactions"},
					Tools:         []string{"read", "search", "edit"},
					RequireReview: true,
				},
			},
			Instructions: "Keep design systems consistent. Test responsive breakpoints across mobile and desktop. Ensure keyboard navigation and contrast ratios.",
		},
		{
			ID: "quick-fix", Version: 1, Name: "Quick Fix",
			Purpose:   "Small, well-defined fixes with a focused implementation and optional review.",
			MainAgent: "Quick Coordinator",
			Agents: []Agent{
				{
					Name:         "Quick Coordinator",
					Role:         "Main agent · coordinates and verifies the focused change",
					Description:  "Scopes bug reports and verifies minimal diffs.",
					Instructions: "Verify the bug before editing. Keep the diff minimal and targeted.",
					Skills:       []string{"Root cause analysis", "Targeted testing"},
					Tools:        []string{"read", "search", "edit", "run_tests"},
				},
				{
					Name:          "Implementer",
					Role:          "Worker · implements a bounded change",
					Description:   "Applies the minimal code fix.",
					Instructions:  "Make exact edits only on the problematic lines.",
					Skills:        []string{"Bug fix", "Regression check"},
					Tools:         []string{"read", "search", "edit"},
					RequireReview: false,
				},
			},
			Instructions: "Prefer one worker. Keep the change focused, verify behavior, and avoid delegation unless it is useful.",
		},
		{
			ID: "deep-engineering", Version: 1, Name: "Deep Engineering",
			Purpose:   "Complex or ambiguous engineering work with investigation, risk mapping, and independent review.",
			MainAgent: "Engineering Lead",
			Agents: []Agent{
				{
					Name:         "Engineering Lead",
					Role:         "Main agent · coordinates the task and manages limits",
					Description:  "Architects solutions and sets boundary conditions.",
					Instructions: "Require thorough analysis before code generation. Maintain isolated worktrees for parallel tracks.",
					Skills:       []string{"Planning & decomposition", "Risk mitigation", "Git worktree workflow"},
					Tools:        []string{"read", "search", "delegate", "synthesize"},
				},
				{
					Name:         "Architect",
					Role:         "Worker · investigates architecture and risks",
					Description:  "Reads codebases, maps dependencies, and identifies architectural bottlenecks.",
					Instructions: "Operate strictly read-only. Report findings with clear tradeoffs and evidence.",
					Skills:       []string{"Architecture analysis", "Performance profiling"},
					Tools:        []string{"read", "search"},
				},
				{
					Name:          "Implementer",
					Role:          "Worker · implements isolated changes",
					Description:   "Implements features according to architect specifications.",
					Instructions:  "Follow architecture documents strictly. Add defensive checks and logging.",
					Skills:        []string{"System implementation", "Refactoring"},
					Tools:         []string{"read", "search", "edit", "run_tests"},
					RequireReview: true,
				},
				{
					Name:         "Independent Reviewer",
					Role:         "Worker · independently reviews changes and evidence",
					Description:  "Audits diffs for security, logic flaws, regressions, and style violations.",
					Instructions: "Inspect all modified files with an adversarial mindset. Ensure no secrets or bad practices are committed.",
					Skills:       []string{"Safe code review", "Security audit"},
					Tools:        []string{"read", "search", "diff_audit"},
				},
			},
			Instructions: "Investigate first, define dependencies and bounded work, parallelize only independent tasks, verify changes, and report unresolved risks.",
		},
	}
}

// DefaultAgents derives the initial agent library from the built-in team
// templates. Names are the identity used by teams and tasks, so duplicates are
// collapsed (the first template that declares a name wins).
func DefaultAgents() []Agent {
	seen := map[string]bool{}
	var agents []Agent
	for _, template := range DefaultTemplates() {
		for _, agent := range template.Agents {
			if seen[agent.Name] {
				continue
			}
			seen[agent.Name] = true
			agents = append(agents, agent)
		}
	}
	return agents
}

// DefaultSkills derives a passive skill library from the capability names the
// built-in templates reference, so imported teams and agents can attach them.
func DefaultSkills() []Skill {
	seen := map[string]bool{}
	var skills []Skill
	for _, template := range DefaultTemplates() {
		for _, agent := range template.Agents {
			for _, name := range agent.Skills {
				name = strings.TrimSpace(name)
				if name == "" || seen[name] {
					continue
				}
				seen[name] = true
				skills = append(skills, Skill{
					Name:         name,
					Description:  "Built-in capability shipped with the team templates.",
					Version:      1,
					Mode:         SkillModePassive,
					Enabled:      false,
					Instructions: "Describe how agents should apply this capability. Passive skills are reference only until switched to active.",
					Files:        []SkillFile{{Path: "SKILL.md", Content: "# " + name + "\n\nDescribe how agents should apply this capability."}},
				})
			}
		}
	}
	return skills
}
