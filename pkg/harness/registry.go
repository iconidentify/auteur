package harness

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Handler executes a tool call. Extracted from chonkbase pkg/tools.
type Handler func(ctx context.Context, input map[string]any) (string, error)

// Registry is a mutex-guarded named-tool registry. An agent implementation
// can embed one to satisfy the Tools/ExecuteTool half of the Agent contract.
type Registry struct {
	mu       sync.RWMutex
	defs     map[string]ToolDefinition
	handlers map[string]Handler
	order    []string
}

func NewRegistry() *Registry {
	return &Registry{
		defs:     map[string]ToolDefinition{},
		handlers: map[string]Handler{},
	}
}

// Register adds or replaces a tool.
func (r *Registry) Register(def ToolDefinition, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.defs[def.Name]; !exists {
		r.order = append(r.order, def.Name)
	}
	r.defs[def.Name] = def
	r.handlers[def.Name] = h
}

// Unregister removes a tool by name.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.defs, name)
	delete(r.handlers, name)
	for i, n := range r.order {
		if n == name {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}

// List returns tool definitions in registration order.
func (r *Registry) List() []ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolDefinition, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.defs[name])
	}
	return out
}

// Names returns a sorted list of registered tool names.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.defs))
	for n := range r.defs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Execute dispatches a call to the registered handler.
func (r *Registry) Execute(ctx context.Context, call ToolCall) (string, error) {
	r.mu.RLock()
	h, ok := r.handlers[call.Name]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("unknown tool %q", call.Name)
	}
	if call.Input == nil {
		call.Input = map[string]any{}
	}
	return h(ctx, call.Input)
}

// Schema helpers keep hand-written JSON schemas terse, following the
// chonkbase convention of inline map literals rather than struct reflection.

// Obj builds a JSON Schema object with required properties.
func Obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func Str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func StrEnum(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

func Num(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc}
}

func Int(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func Bool(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func Arr(desc string, items map[string]any) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": items}
}
