// Package harness is a lean agent-loop library extracted from chonkbase's
// pkg/agent harness. It keeps the same core contracts (Agent, Provider,
// EventSink, RunLoop) and adds streaming deltas, retries with backoff,
// richer events for UI introspection, file-based skills, and sub-agent
// delegation. It depends only on the standard library.
package harness

// Message represents a single message in a multi-turn AI conversation.
//
// The Role field determines the message type:
//   - "system": system prompt (typically the first message)
//   - "user": user input or tool results being returned to the LLM
//   - "assistant": LLM response, possibly containing tool calls
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`

	// ToolCalls is non-nil for assistant messages that invoked tools.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`

	// ToolResults is non-nil for user messages carrying tool results.
	ToolResults []ToolResult `json:"tool_results,omitempty"`

	// Images carries local image paths to attach to a user message
	// (providers inline them as data URIs).
	Images []string `json:"images,omitempty"`
}

// ToolCall represents an AI provider's request to invoke a named tool.
type ToolCall struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

// ToolResult represents the output of a single tool execution.
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error,omitempty"`
}

// ToolDefinition describes a tool the AI can call. InputSchema is a JSON
// Schema object; it may be nil for tools without arguments.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// CompletionRequest is the input to a Provider.Complete call.
type CompletionRequest struct {
	Model       string           `json:"model"`
	Messages    []Message        `json:"messages"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
	MaxTokens   int              `json:"max_tokens,omitempty"`
	Temperature float64          `json:"temperature,omitempty"`
}

// CompletionResponse is the output from a Provider.Complete call.
type CompletionResponse struct {
	Content      string     `json:"content"`
	Reasoning    string     `json:"reasoning,omitempty"`
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`
	StopReason   string     `json:"stop_reason"`
	Model        string     `json:"model"`
	InputTokens  int        `json:"input_tokens"`
	OutputTokens int        `json:"output_tokens"`
}

// StreamDelta is one incremental chunk from a streaming completion.
type StreamDelta struct {
	Text      string
	Reasoning string
}
