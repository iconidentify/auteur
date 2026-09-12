package harness

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"
)

// Result summarizes the outcome of a RunLoop execution.
//
// StopReason values:
//   - "completed": Agent.IsDone returned true
//   - "max_iterations": Config.MaxIterations was reached
//   - "token_budget": Config.MaxTokenBudget was exceeded
//   - "max_text_replies": Config.MaxConsecutiveTextReplies was reached
//   - "error": context cancellation or provider error (err is non-nil)
type Result struct {
	Iterations  int
	TotalTokens int64
	StopReason  string
	FinalText   string
}

// RunLoop executes a multi-turn agent conversation loop. It is the
// chonkbase pkg/agent loop with streaming, retries, tool-call validation,
// and enriched events folded in from the production harness.
func RunLoop(ctx context.Context, ag Agent, provider Provider, cfg Config, sink EventSink) (*Result, error) {
	if cfg.MaxIterations < 0 {
		cfg.MaxIterations = 100
	}
	unlimited := cfg.MaxIterations == 0
	if cfg.MaxOutputTokens <= 0 {
		cfg.MaxOutputTokens = 16384
	}
	if cfg.MaxConsecutiveTextReplies <= 0 {
		cfg.MaxConsecutiveTextReplies = 3
	}
	if cfg.Retry.MaxRetries <= 0 {
		cfg.Retry = DefaultConfig().Retry
	}
	if sink == nil {
		sink = NoOpSink{}
	}
	emit := func(e Event) {
		e.AgentID = cfg.AgentID
		e.AgentName = cfg.AgentName
		e.ParentID = cfg.ParentID
		e.Time = time.Now()
		sink.Emit(ctx, e)
	}

	streaming, canStream := provider.(StreamingProvider)

	messages := []Message{{Role: "system", Content: ag.SystemPrompt()}}
	if cfg.InitialMessage != "" {
		messages = append(messages, Message{
			Role:    "user",
			Content: cfg.InitialMessage,
			Images:  cfg.InitialImages,
		})
	}

	emit(Event{Type: "agent_start", Message: cfg.InitialMessage})

	result := &Result{}
	consecutiveTextReplies := 0
	validationRetries := 0

	for i := 0; unlimited || i < cfg.MaxIterations; i++ {
		if ctx.Err() != nil {
			result.StopReason = "error"
			return result, ctx.Err()
		}
		if cfg.Inbox != nil {
			for _, note := range cfg.Inbox() {
				messages = append(messages, Message{Role: "user", Content: note})
				emit(Event{Type: "note", Message: note, Iteration: i})
			}
		}
		if cfg.MaxTokenBudget > 0 && result.TotalTokens >= cfg.MaxTokenBudget {
			result.StopReason = "token_budget"
			emit(Event{Type: "error", Message: "token budget exhausted", Iteration: i})
			return result, nil
		}

		req := CompletionRequest{
			Model:       cfg.Model,
			Messages:    messages,
			Tools:       ag.Tools(),
			MaxTokens:   cfg.MaxOutputTokens,
			Temperature: cfg.Temperature,
		}

		resp, err := completeWithRetry(ctx, provider, streaming, canStream, req, cfg.Retry, i, emit)
		if err != nil {
			result.StopReason = "error"
			emit(Event{Type: "error", Message: err.Error(), Iteration: i})
			return result, fmt.Errorf("agent AI call (iteration %d): %w", i, err)
		}

		result.Iterations = i + 1
		result.TotalTokens += int64(resp.InputTokens + resp.OutputTokens)

		// Validate tool calls against the currently registered tools; give
		// the model a corrective nudge for hallucinated names (up to 3x).
		if len(resp.ToolCalls) > 0 {
			valid := map[string]bool{}
			var names []string
			for _, t := range req.Tools {
				valid[t.Name] = true
				names = append(names, t.Name)
			}
			if _, verr := ValidateToolCalls(resp.ToolCalls, valid); verr != nil {
				validationRetries++
				if validationRetries > 3 {
					result.StopReason = "error"
					return result, fmt.Errorf("repeated invalid tool calls: %w", verr)
				}
				messages = append(messages,
					Message{Role: "assistant", Content: resp.Content, ToolCalls: nil},
					Message{Role: "user", Content: fmt.Sprintf(
						"Error: %v. Available tools: %s. Retry with a valid tool.",
						verr, strings.Join(names, ", "))})
				continue
			}
			validationRetries = 0
		}

		if len(resp.ToolCalls) == 0 {
			if resp.Content != "" {
				messages = append(messages, Message{Role: "assistant", Content: resp.Content})
				result.FinalText = resp.Content
				emit(Event{Type: "text", Message: resp.Content, Iteration: i, Tokens: result.TotalTokens})
			}
			if ag.IsDone() {
				emit(Event{Type: "complete", Message: "Agent completed", Iteration: i, Tokens: result.TotalTokens})
				result.StopReason = "completed"
				return result, nil
			}
			consecutiveTextReplies++
			if consecutiveTextReplies >= cfg.MaxConsecutiveTextReplies {
				result.StopReason = "max_text_replies"
				return result, nil
			}
			messages = append(messages, Message{Role: "user", Content: "You replied with text but called no tool, so nothing actually happened. Take a concrete action NOW by calling a tool — delegate, list or read a file, render, review, request approval — or call your completion tool if the work is genuinely finished. Do not reply with prose alone; act."})
			continue
		}

		consecutiveTextReplies = 0

		if resp.Content != "" {
			emit(Event{Type: "text", Message: resp.Content, Iteration: i, Tokens: result.TotalTokens})
		}

		messages = append(messages, Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		toolResults := make([]ToolResult, 0, len(resp.ToolCalls))
		for _, tc := range resp.ToolCalls {
			emit(Event{Type: "tool_call", ToolName: tc.Name, ToolInput: tc.Input, Message: tc.Name, Iteration: i})

			start := time.Now()
			execResult, execErr := ag.ExecuteTool(ctx, tc)
			tr := ToolResult{ToolCallID: tc.ID}
			if execErr != nil {
				tr.Content = fmt.Sprintf("Error: %v", execErr)
				tr.IsError = true
			} else {
				tr.Content = execResult
			}
			toolResults = append(toolResults, tr)

			emit(Event{
				Type: "tool_result", ToolName: tc.Name,
				Message:    Abbreviate(tr.Content, 600),
				Iteration:  i,
				DurationMs: time.Since(start).Milliseconds(),
				Data:       map[string]any{"is_error": tr.IsError},
			})
		}

		messages = append(messages, Message{Role: "user", ToolResults: toolResults})

		if ag.IsDone() {
			emit(Event{Type: "complete", Message: "Agent completed", Iteration: i, Tokens: result.TotalTokens})
			result.StopReason = "completed"
			return result, nil
		}
	}

	if ag.IsDone() {
		result.StopReason = "completed"
		return result, nil
	}
	result.StopReason = "max_iterations"
	return result, fmt.Errorf("agent reached max iterations (%d) without completing", cfg.MaxIterations)
}

// completeWithRetry calls the provider (streaming when available) with
// exponential backoff on retryable errors.
func completeWithRetry(ctx context.Context, p Provider, sp StreamingProvider, canStream bool, req CompletionRequest, retry RetryConfig, iter int, emit func(Event)) (*CompletionResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= retry.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := retry.BaseDelay << (attempt - 1)
			if delay > retry.MaxDelay {
				delay = retry.MaxDelay
			}
			slog.Info("retrying provider call", "attempt", attempt, "delay", delay)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		var resp *CompletionResponse
		var err error
		if canStream {
			resp, err = sp.CompleteStream(ctx, req, func(d StreamDelta) {
				if d.Text != "" {
					emit(Event{Type: "text_delta", Message: d.Text, Iteration: iter})
				}
				if d.Reasoning != "" {
					emit(Event{Type: "reasoning_delta", Message: d.Reasoning, Iteration: iter})
				}
			})
		} else {
			resp, err = p.Complete(ctx, req)
		}
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("provider failed after %d retries: %w", retry.MaxRetries, lastErr)
}

// isRetryable reports whether a provider error is transient.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"status 429", "status 500", "status 502", "status 503", "status 504",
		"rate limit", "overloaded", "timeout", "connection reset", "eof",
		"temporarily unavailable",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// ValidateToolCalls checks that every tool call references a known tool and
// normalizes calls in place (colon-to-underscore fallback, nil input maps).
// Extracted verbatim from chonkbase pkg/agent.
func ValidateToolCalls(calls []ToolCall, validNames map[string]bool) ([]ToolCall, error) {
	for i, tc := range calls {
		if !validNames[tc.Name] {
			safeName := strings.ReplaceAll(tc.Name, ":", "_")
			if validNames[safeName] {
				calls[i].Name = safeName
			} else {
				return nil, fmt.Errorf("unknown tool %q", tc.Name)
			}
		}
		if tc.Input == nil {
			calls[i].Input = map[string]any{}
		}
	}
	return calls, nil
}

// Abbreviate truncates a string to maxLen characters for event payloads.
func Abbreviate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
