package foreman

import (
	"time"

	"github.com/google/uuid"
)

const SchemaVersion = 2

type WorkspaceState struct {
	SchemaVersion int              `json:"schemaVersion"`
	ProjectPath   string           `json:"projectPath"`
	Rooms         []Room           `json:"rooms"`
	Teams         []Team           `json:"teams"`
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
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Version      int    `json:"version"`
	Instructions string `json:"instructions"`
	Enabled      bool   `json:"enabled"`
}

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
			ID: "quick-fix", Version: 1, Name: "Quick Fix",
			Purpose:   "Small, well-defined fixes with a focused implementation and optional review.",
			MainAgent: "Quick Coordinator",
			Agents: []Agent{
				{Name: "Quick Coordinator", Role: "Main agent · coordinates and verifies the focused change"},
				{Name: "Implementer", Role: "Worker · implements a bounded change"},
				{Name: "Reviewer", Role: "Optional worker · reviews behavior-sensitive changes"},
			},
			Instructions: "Prefer one worker. Keep the change focused, verify behavior, and avoid delegation unless it is useful.",
		},
		{
			ID: "feature-team", Version: 1, Name: "Feature Team",
			Purpose:   "Ordinary feature work with planning, implementation, and verification.",
			MainAgent: "Feature Lead",
			Agents: []Agent{
				{Name: "Feature Lead", Role: "Main agent · plans, routes work, and summarizes results"},
				{Name: "Analyst", Role: "Worker · investigates requirements and dependencies"},
				{Name: "Implementer", Role: "Worker · implements assigned changes"},
				{Name: "Test Engineer", Role: "Worker · verifies behavior and regressions"},
			},
			Instructions: "Create bounded tasks, identify dependencies, parallelize only independent work, and verify before reporting completion.",
		},
		{
			ID: "deep-engineering", Version: 1, Name: "Deep Engineering",
			Purpose:   "Complex or ambiguous engineering work with investigation and independent review.",
			MainAgent: "Engineering Lead",
			Agents: []Agent{
				{Name: "Engineering Lead", Role: "Main agent · coordinates the task and manages limits"},
				{Name: "Architect", Role: "Worker · investigates architecture and risks"},
				{Name: "Implementer", Role: "Worker · implements isolated changes"},
				{Name: "Test Engineer", Role: "Worker · runs targeted verification"},
				{Name: "Independent Reviewer", Role: "Worker · independently reviews changes and evidence"},
			},
			Instructions: "Investigate first, define dependencies and bounded work, parallelize only independent tasks, verify changes, and report unresolved risks.",
		},
	}
}
