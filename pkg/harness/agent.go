package harness

import (
	"context"
	"time"
)

// Agent defines the contract for an agentic AI application, matching the
// chonkbase pkg/agent contract. RunLoop drives the multi-turn conversation
// by repeatedly calling Complete on a Provider, dispatching tool calls
// through ExecuteTool, and checking IsDone after each round.
type Agent interface {
	// SystemPrompt returns the system prompt for the AI conversation.
	// Called once at the start of RunLoop; must not return "".
	SystemPrompt() string

	// Tools returns the tool definitions available to the AI. Called every
	// iteration, so implementations may add or remove tools mid-run.
	Tools() []ToolDefinition

	// ExecuteTool dispatches a tool call and returns the string result.
	// A non-nil error marks the result as a tool error in the conversation;
	// the loop does not abort on tool errors.
	ExecuteTool(ctx context.Context, call ToolCall) (string, error)

	// IsDone reports whether the agent has completed its work. Checked
	// after every LLM response and after every batch of tool executions.
	IsDone() bool
}

// Provider calls an LLM to generate completions.
type Provider interface {
	Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)
}

// StreamingProvider is an optional Provider extension. When the provider
// passed to RunLoop implements it, the loop streams each turn and forwards
// deltas to the EventSink as "text_delta" / "reasoning_delta" events.
type StreamingProvider interface {
	Provider
	CompleteStream(ctx context.Context, req CompletionRequest, onDelta func(StreamDelta)) (*CompletionResponse, error)
}

// Event is a progress update emitted by RunLoop and by tools.
//
// Type is one of:
//   - "agent_start": a run began (Message is the initial task)
//   - "text_delta" / "reasoning_delta": streaming output chunks
//   - "text": a full assistant text block (Message contains the text)
//   - "tool_call": the LLM requested a tool (ToolName, ToolInput set)
//   - "tool_result": a tool finished (ToolName, Message, DurationMs set)
//   - "status": a mid-tool progress note (e.g. video render polling)
//   - "artifact": a produced asset (Data describes it)
//   - "complete": the agent signaled completion
//   - "error": an error occurred
type Event struct {
	Type       string         `json:"type"`
	AgentID    string         `json:"agent_id,omitempty"`
	AgentName  string         `json:"agent_name,omitempty"`
	ParentID   string         `json:"parent_id,omitempty"`
	Message    string         `json:"message,omitempty"`
	ToolName   string         `json:"tool_name,omitempty"`
	ToolInput  map[string]any `json:"tool_input,omitempty"`
	Iteration  int            `json:"iteration"`
	DurationMs int64          `json:"duration_ms,omitempty"`
	Tokens     int64          `json:"tokens,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
	Time       time.Time      `json:"time"`
}

// EventSink receives progress events. Emit is called synchronously from the
// RunLoop goroutine, so slow sinks should buffer internally. A nil sink
// passed to RunLoop is safe.
type EventSink interface {
	Emit(ctx context.Context, event Event)
}

// NoOpSink discards all events.
type NoOpSink struct{}

func (NoOpSink) Emit(context.Context, Event) {}
