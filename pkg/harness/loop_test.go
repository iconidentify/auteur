package harness

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// scriptedProvider returns canned responses in order.
type scriptedProvider struct {
	responses []*CompletionResponse
	calls     int
	requests  []CompletionRequest
}

func (p *scriptedProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	p.requests = append(p.requests, req)
	if p.calls >= len(p.responses) {
		return nil, fmt.Errorf("scripted provider exhausted after %d calls", p.calls)
	}
	r := p.responses[p.calls]
	p.calls++
	return r, nil
}

// testAgent is a minimal Agent driven by a Registry.
type testAgent struct {
	reg  *Registry
	done bool
}

func (a *testAgent) SystemPrompt() string    { return "You are a test agent." }
func (a *testAgent) Tools() []ToolDefinition { return a.reg.List() }
func (a *testAgent) IsDone() bool            { return a.done }
func (a *testAgent) ExecuteTool(ctx context.Context, call ToolCall) (string, error) {
	return a.reg.Execute(ctx, call)
}

func newTestAgent() *testAgent {
	a := &testAgent{reg: NewRegistry()}
	a.reg.Register(ToolDefinition{
		Name:        "echo",
		Description: "echo input",
		InputSchema: Obj(map[string]any{"text": Str("text")}, "text"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		return "echo: " + input["text"].(string), nil
	})
	a.reg.Register(ToolDefinition{
		Name:        "done",
		Description: "finish",
		InputSchema: Obj(map[string]any{}),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		a.done = true
		return "done", nil
	})
	return a
}

type recordingSink struct{ events []Event }

func (s *recordingSink) Emit(ctx context.Context, e Event) { s.events = append(s.events, e) }

func TestRunLoopToolCallsAndCompletion(t *testing.T) {
	ag := newTestAgent()
	provider := &scriptedProvider{responses: []*CompletionResponse{
		{Content: "Working.", ToolCalls: []ToolCall{{ID: "t1", Name: "echo", Input: map[string]any{"text": "hi"}}},
			InputTokens: 10, OutputTokens: 5},
		{ToolCalls: []ToolCall{{ID: "t2", Name: "done"}}, InputTokens: 20, OutputTokens: 5},
	}}
	sink := &recordingSink{}

	cfg := DefaultConfig()
	cfg.Model = "test-model"
	cfg.InitialMessage = "go"
	cfg.AgentID = "agent-1"
	cfg.AgentName = "tester"

	res, err := RunLoop(context.Background(), ag, provider, cfg, sink)
	if err != nil {
		t.Fatalf("RunLoop error: %v", err)
	}
	if res.StopReason != "completed" {
		t.Errorf("stop reason = %q, want completed", res.StopReason)
	}
	if res.TotalTokens != 40 {
		t.Errorf("tokens = %d, want 40", res.TotalTokens)
	}

	// Tool result must be threaded back to the provider.
	last := provider.requests[len(provider.requests)-1]
	var foundResult bool
	for _, m := range last.Messages {
		for _, tr := range m.ToolResults {
			if tr.ToolCallID == "t1" && tr.Content == "echo: hi" {
				foundResult = true
			}
		}
	}
	if !foundResult {
		t.Error("echo tool result was not threaded back into the conversation")
	}

	// Events: agent_start, text, tool_call x2, tool_result x2, complete.
	var types []string
	for _, e := range sink.events {
		types = append(types, e.Type)
		if e.AgentID != "agent-1" {
			t.Errorf("event %s missing agent identity", e.Type)
		}
	}
	joined := strings.Join(types, ",")
	for _, want := range []string{"agent_start", "tool_call", "tool_result", "complete"} {
		if !strings.Contains(joined, want) {
			t.Errorf("event stream missing %q (got %s)", want, joined)
		}
	}
}

func TestRunLoopHallucinatedToolGetsCorrected(t *testing.T) {
	ag := newTestAgent()
	provider := &scriptedProvider{responses: []*CompletionResponse{
		{ToolCalls: []ToolCall{{ID: "t1", Name: "no_such_tool"}}},
		{ToolCalls: []ToolCall{{ID: "t2", Name: "done"}}},
	}}
	res, err := RunLoop(context.Background(), ag, provider, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("RunLoop error: %v", err)
	}
	if res.StopReason != "completed" {
		t.Errorf("stop reason = %q, want completed", res.StopReason)
	}
	// The corrective nudge must name the available tools.
	last := provider.requests[len(provider.requests)-1]
	corrective := last.Messages[len(last.Messages)-1]
	if !strings.Contains(corrective.Content, "Available tools") {
		t.Errorf("corrective message not sent, got %q", corrective.Content)
	}
}

func TestRunLoopMaxTextReplies(t *testing.T) {
	ag := newTestAgent()
	provider := &scriptedProvider{responses: []*CompletionResponse{
		{Content: "chatting"}, {Content: "still chatting"}, {Content: "more chatting"},
	}}
	cfg := DefaultConfig()
	cfg.MaxConsecutiveTextReplies = 3
	res, err := RunLoop(context.Background(), ag, provider, cfg, nil)
	if err != nil {
		t.Fatalf("RunLoop error: %v", err)
	}
	if res.StopReason != "max_text_replies" {
		t.Errorf("stop reason = %q, want max_text_replies", res.StopReason)
	}
}

func TestValidateToolCalls(t *testing.T) {
	valid := map[string]bool{"skill_tool": true, "echo": true}
	calls := []ToolCall{{Name: "skill:tool"}, {Name: "echo", Input: nil}}
	out, err := ValidateToolCalls(calls, valid)
	if err != nil {
		t.Fatalf("ValidateToolCalls: %v", err)
	}
	if out[0].Name != "skill_tool" {
		t.Errorf("colon normalization failed: %q", out[0].Name)
	}
	if out[1].Input == nil {
		t.Error("nil input not normalized to empty map")
	}
	if _, err := ValidateToolCalls([]ToolCall{{Name: "bogus"}}, valid); err == nil {
		t.Error("unknown tool not rejected")
	}
}

func TestDelegateRunsChildAndReturnsReport(t *testing.T) {
	childProvider := &scriptedProvider{responses: []*CompletionResponse{
		{ToolCalls: []ToolCall{{ID: "c1", Name: "done"}}},
	}}
	reg := NewRegistry()
	sink := &recordingSink{}
	factory := func(ctx context.Context, role string) (Agent, Config, error) {
		if role != "worker" {
			return nil, Config{}, fmt.Errorf("unknown role %q", role)
		}
		cfg := DefaultConfig()
		return newTestAgent(), cfg, nil
	}
	RegisterDelegateTools(reg, factory, childProvider, sink, "parent-1", []string{"worker"}, 2)

	out, err := reg.Execute(context.Background(), ToolCall{
		Name:  "delegate",
		Input: map[string]any{"role": "worker", "task": "do the thing"},
	})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if !strings.Contains(out, "worker") && out == "" {
		t.Errorf("unexpected delegate output %q", out)
	}
	// Child events must carry the parent linkage.
	var sawChild bool
	for _, e := range sink.events {
		if e.ParentID == "parent-1" && e.AgentName == "worker" {
			sawChild = true
		}
	}
	if !sawChild {
		t.Error("child events not tagged with parent id")
	}
}

// Inbox notes must reach the model as user messages on the next iteration.
func TestRunLoopInboxInjection(t *testing.T) {
	ag := newTestAgent()
	provider := &scriptedProvider{responses: []*CompletionResponse{
		{ToolCalls: []ToolCall{{ID: "1", Name: "echo", Input: map[string]any{"text": "hi"}}}},
		{ToolCalls: []ToolCall{{ID: "2", Name: "done", Input: map[string]any{}}}},
	}}
	delivered := false
	cfg := DefaultConfig()
	cfg.InitialMessage = "go"
	cfg.Inbox = func() []string {
		if delivered {
			return nil
		}
		delivered = true
		return []string{"PRODUCER'S NOTE: make it blue"}
	}
	sink := &recordingSink{}
	if _, err := RunLoop(context.Background(), ag, provider, cfg, sink); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, req := range provider.requests {
		for _, m := range req.Messages {
			if m.Role == "user" && m.Content == "PRODUCER'S NOTE: make it blue" {
				found = true
			}
		}
	}
	if !found {
		t.Error("inbox note never reached the model as a user message")
	}
	noteEmitted := false
	for _, e := range sink.events {
		if e.Type == "note" {
			noteEmitted = true
		}
	}
	if !noteEmitted {
		t.Error("note event not emitted for the UI")
	}
}
