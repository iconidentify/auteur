package harness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// AgentFactory builds a child agent for a delegated role. It returns the
// agent plus a Config carrying the child's model and limits; RunLoop
// identity fields (AgentID, ParentID, InitialMessage) are filled in by the
// delegate tool. Modeled on chonkbase internal/agent/chonk/delegate.go.
type AgentFactory func(ctx context.Context, role string) (Agent, Config, error)

// NewID returns a short random identifier for agent runs.
func NewID(prefix string) string {
	b := make([]byte, 4)
	rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

// RegisterDelegateTools adds "delegate" and "delegate_parallel" tools to a
// registry. Children run in-process via RunLoop, share the provider and
// sink, and are tagged with ParentID for UI nesting. maxParallel bounds
// concurrent children in delegate_parallel (chonkbase used 5).
func RegisterDelegateTools(reg *Registry, factory AgentFactory, provider Provider, sink EventSink, parentID string, roles []string, maxParallel int) {
	if maxParallel <= 0 {
		maxParallel = 4
	}
	roleList := strings.Join(roles, ", ")

	runChild := func(ctx context.Context, role, task string) (string, error) {
		child, cfg, err := factory(ctx, role)
		if err != nil {
			return "", err
		}
		// Respect identity assigned by the factory (tools may already be
		// bound to it for event attribution); fill in only when absent.
		if cfg.AgentID == "" {
			cfg.AgentID = NewID(role)
		}
		if cfg.AgentName == "" {
			cfg.AgentName = role
		}
		cfg.ParentID = parentID
		cfg.InitialMessage = task
		res, err := RunLoop(ctx, child, provider, cfg, sink)
		if err != nil {
			return "", fmt.Errorf("sub-agent %s: %w", role, err)
		}
		// Agents that finish via a completion tool expose their report
		// through the optional Reporter extension.
		if r, ok := child.(interface{ Report() string }); ok && r.Report() != "" {
			return r.Report(), nil
		}
		if res.FinalText == "" {
			return fmt.Sprintf("(sub-agent %s finished with stop reason %s and no final report)", role, res.StopReason), nil
		}
		return res.FinalText, nil
	}

	reg.Register(ToolDefinition{
		Name: "delegate",
		Description: "Delegate a task to a specialist sub-agent and wait for its report. " +
			"Available roles: " + roleList + ". The task should be a complete, self-contained brief: " +
			"the sub-agent cannot see your conversation.",
		InputSchema: Obj(map[string]any{
			"role": StrEnum("The specialist to engage", roles...),
			"task": Str("Complete brief for the sub-agent, including all context it needs"),
		}, "role", "task"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		role, _ := input["role"].(string)
		task, _ := input["task"].(string)
		return runChild(ctx, role, task)
	})

	reg.Register(ToolDefinition{
		Name: "delegate_parallel",
		Description: "Delegate several independent tasks to sub-agents that run concurrently. " +
			"Use when tasks do not depend on each other's output. Available roles: " + roleList + ".",
		InputSchema: Obj(map[string]any{
			"tasks": Arr("Tasks to run concurrently", Obj(map[string]any{
				"role": StrEnum("The specialist to engage", roles...),
				"task": Str("Complete brief for the sub-agent"),
			}, "role", "task")),
		}, "tasks"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		rawTasks, _ := input["tasks"].([]any)
		if len(rawTasks) == 0 {
			return "", fmt.Errorf("tasks must be a non-empty array")
		}
		type item struct {
			Role   string `json:"role"`
			Result string `json:"result"`
			Error  string `json:"error,omitempty"`
		}
		results := make([]item, len(rawTasks))
		sem := make(chan struct{}, maxParallel)
		var wg sync.WaitGroup
		for idx, raw := range rawTasks {
			m, _ := raw.(map[string]any)
			role, _ := m["role"].(string)
			task, _ := m["task"].(string)
			results[idx].Role = role
			wg.Add(1)
			go func(idx int, role, task string) {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						results[idx].Error = fmt.Sprintf("panic: %v", r)
					}
				}()
				sem <- struct{}{}
				defer func() { <-sem }()
				out, err := runChild(ctx, role, task)
				if err != nil {
					results[idx].Error = err.Error()
					return
				}
				results[idx].Result = out
			}(idx, role, task)
		}
		wg.Wait()
		b, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return "", err
		}
		return string(b), nil
	})
}
