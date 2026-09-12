package agents

import (
	"context"
	"fmt"

	"auteur/pkg/harness"
)

// StudioAgent is the harness.Agent implementation shared by every role in
// the crew. Behavior differences between roles live entirely in the system
// prompt and the registered toolset.
type StudioAgent struct {
	role         string
	systemPrompt string
	reg          *harness.Registry

	done   bool
	report string
}

var _ harness.Agent = (*StudioAgent)(nil)

func NewStudioAgent(role, systemPrompt string, reg *harness.Registry) *StudioAgent {
	a := &StudioAgent{role: role, systemPrompt: systemPrompt, reg: reg}
	reg.Register(harness.ToolDefinition{
		Name: "finish",
		Description: "Signal that your assignment is complete. The report is delivered to whoever " +
			"engaged you; make it a complete account of what you produced, including file paths.",
		InputSchema: harness.Obj(map[string]any{
			"report": harness.Str("Complete report of the work done, decisions made, and output file paths"),
		}, "report"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		report, _ := input["report"].(string)
		if report == "" {
			return "", fmt.Errorf("report must not be empty")
		}
		a.done = true
		a.report = report
		return "Assignment marked complete.", nil
	})
	return a
}

func (a *StudioAgent) SystemPrompt() string            { return a.systemPrompt }
func (a *StudioAgent) Tools() []harness.ToolDefinition { return a.reg.List() }
func (a *StudioAgent) IsDone() bool                    { return a.done }
func (a *StudioAgent) Report() string                  { return a.report }

func (a *StudioAgent) ExecuteTool(ctx context.Context, call harness.ToolCall) (string, error) {
	return a.reg.Execute(ctx, call)
}
