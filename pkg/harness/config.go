package harness

import "time"

// Config controls the execution parameters of RunLoop. Zero-value fields
// use the defaults documented on each field; DefaultConfig pre-populates
// sensible values.
type Config struct {
	// Model is the model identifier passed to the provider.
	Model string

	// AgentID and AgentName identify this run in emitted events, so a UI
	// can attribute interleaved events from concurrent agents.
	AgentID   string
	AgentName string

	// ParentID is set on delegated child runs for UI nesting.
	ParentID string

	// MaxIterations caps LLM round-trips (default 100; 0 = unlimited).
	MaxIterations int

	// MaxTokenBudget caps cumulative input+output tokens (0 = unlimited).
	MaxTokenBudget int64

	// MaxOutputTokens is max_tokens per LLM call (default 16384).
	MaxOutputTokens int

	// Temperature is the sampling temperature (default 0.3).
	Temperature float64

	// InitialMessage is the first user message that kicks off the loop.
	InitialMessage string

	// InitialImages are local image paths attached to the initial message.
	InitialImages []string

	// MaxConsecutiveTextReplies stops a loop that keeps producing text
	// without tool calls or completion (default 3).
	MaxConsecutiveTextReplies int

	// Inbox, when set, is polled at the top of every iteration; each
	// returned string is appended as a user message before the next model
	// call. It is how a live operator steers a running agent.
	Inbox func() []string

	// Retry controls provider-call retries.
	Retry RetryConfig
}

// RetryConfig mirrors chonkbase's retry behavior: exponential backoff on
// retryable provider errors.
type RetryConfig struct {
	MaxRetries int           // default 6
	BaseDelay  time.Duration // default 1s
	MaxDelay   time.Duration // default 30s
}

// DefaultConfig returns a Config with defaults tuned for production runs.
func DefaultConfig() Config {
	return Config{
		MaxIterations:             100,
		MaxOutputTokens:           16384,
		Temperature:               0.3,
		MaxConsecutiveTextReplies: 3,
		Retry: RetryConfig{
			MaxRetries: 6,
			BaseDelay:  time.Second,
			MaxDelay:   30 * time.Second,
		},
	}
}
