package loop

import (
	"time"
)

// EventType identifies the nature of an event emitted by the agent core.
type EventType string

const (
	EventTurnStart    EventType = "turn_start"
	EventThinking     EventType = "thinking"
	EventContentDelta EventType = "content_delta"
	EventProgress     EventType = "progress"
	EventToolCall     EventType = "tool_call"
	EventToolResult   EventType = "tool_result"
	EventFilePatch    EventType = "file_patch"
	EventVerification EventType = "verification"
	EventSkillLoaded  EventType = "skill_loaded"
	EventModeSwitched EventType = "mode_switched"
	EventTurnComplete EventType = "turn_complete"
	EventTurnError    EventType = "turn_error"
)

// AgentEvent represents a typed event emitted during agent task execution.
// Interfaces (CLI, TUI, Web, Editor) subscribe to AgentEvent to render live
// updates without coupling to internal engine state.
type AgentEvent struct {
	Type      EventType `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Payload   any       `json:"payload"`
}

// EventListener is a callback function that receives AgentEvent notifications.
type EventListener func(event AgentEvent)

// ToolCallPayload describes a tool invocation proposed by the model.
type ToolCallPayload struct {
	CallID    string `json:"call_id"`
	ToolName  string `json:"tool_name"`
	Arguments string `json:"arguments"`
}

// ToolResultPayload describes the result of executing a tool call.
type ToolResultPayload struct {
	CallID   string        `json:"call_id"`
	ToolName string        `json:"tool_name"`
	Output   string        `json:"output"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration"`
	Cached   bool          `json:"cached"`
}

// FilePatchPayload describes a file system modification made by the agent.
type FilePatchPayload struct {
	Path   string `json:"path"`
	Action string `json:"action"` // "created", "modified", "deleted"
	Diff   string `json:"diff"`
	Seq    int    `json:"seq"`
}

// VerificationPayload describes the output and status of compiler or test checks.
type VerificationPayload struct {
	Command  string        `json:"command"`
	Passed   bool          `json:"passed"`
	Output   string        `json:"output"`
	Duration time.Duration `json:"duration"`
}

// ProgressPayload describes high-level loop phase or status changes.
type ProgressPayload struct {
	State LoopState `json:"state"`
	Info  string    `json:"info"`
}
