// Package agents defines the Auteur studio crew: the provider binding, the
// agent implementation, and the role definitions for the director and its
// specialist sub-agents.
package agents

import (
	"context"
	"encoding/json"
	"fmt"

	"auteur/pkg/harness"
	"auteur/pkg/xai"
)

// XAIProvider adapts pkg/xai to the harness Provider/StreamingProvider
// contracts, converting between harness messages and the OpenAI-compatible
// wire format (the same mapping chonkbase's ai client performs).
type XAIProvider struct {
	Client *xai.Client
}

var _ harness.StreamingProvider = (*XAIProvider)(nil)

func (p *XAIProvider) Complete(ctx context.Context, req harness.CompletionRequest) (*harness.CompletionResponse, error) {
	return p.complete(ctx, req, nil)
}

func (p *XAIProvider) CompleteStream(ctx context.Context, req harness.CompletionRequest, onDelta func(harness.StreamDelta)) (*harness.CompletionResponse, error) {
	return p.complete(ctx, req, onDelta)
}

func (p *XAIProvider) complete(ctx context.Context, req harness.CompletionRequest, onDelta func(harness.StreamDelta)) (*harness.CompletionResponse, error) {
	wireReq, err := toWire(req)
	if err != nil {
		return nil, err
	}
	var resp *xai.ChatResponse
	if onDelta != nil {
		resp, err = p.Client.ChatCompletionStream(ctx, wireReq, func(d xai.StreamDelta) {
			onDelta(harness.StreamDelta{Text: d.Text, Reasoning: d.Reasoning})
		})
	} else {
		resp, err = p.Client.ChatCompletion(ctx, wireReq)
	}
	if err != nil {
		return nil, err
	}
	return fromWire(resp)
}

func toWire(req harness.CompletionRequest) (xai.ChatRequest, error) {
	out := xai.ChatRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
	}
	if req.Temperature > 0 {
		t := req.Temperature
		out.Temperature = &t
	}
	for _, t := range req.Tools {
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		raw, err := json.Marshal(schema)
		if err != nil {
			return out, fmt.Errorf("marshal schema for tool %s: %w", t.Name, err)
		}
		out.Tools = append(out.Tools, xai.Tool{
			Type: "function",
			Function: xai.ToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  raw,
			},
		})
	}

	for _, m := range req.Messages {
		switch {
		case len(m.ToolResults) > 0:
			// Each tool result becomes its own role:"tool" message.
			for _, tr := range m.ToolResults {
				content := tr.Content
				if tr.IsError {
					content = "TOOL ERROR: " + content
				}
				out.Messages = append(out.Messages, xai.Message{
					Role:       "tool",
					Content:    content,
					ToolCallID: tr.ToolCallID,
				})
			}
		case len(m.ToolCalls) > 0:
			wm := xai.Message{Role: m.Role, Content: m.Content}
			for _, tc := range m.ToolCalls {
				args, err := json.Marshal(tc.Input)
				if err != nil {
					return out, err
				}
				wtc := xai.ToolCall{ID: tc.ID, Type: "function"}
				wtc.Function.Name = tc.Name
				wtc.Function.Arguments = string(args)
				wm.ToolCalls = append(wm.ToolCalls, wtc)
			}
			out.Messages = append(out.Messages, wm)
		case len(m.Images) > 0:
			parts := []xai.ContentPart{{Type: "text", Text: m.Content}}
			for _, img := range m.Images {
				part, err := xai.ImagePart(img)
				if err != nil {
					return out, fmt.Errorf("attach image %s: %w", img, err)
				}
				parts = append(parts, part)
			}
			out.Messages = append(out.Messages, xai.Message{Role: m.Role, Content: parts})
		default:
			out.Messages = append(out.Messages, xai.Message{Role: m.Role, Content: m.Content})
		}
	}
	return out, nil
}

func fromWire(resp *xai.ChatResponse) (*harness.CompletionResponse, error) {
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("empty response from provider")
	}
	ch := resp.Choices[0]
	out := &harness.CompletionResponse{
		StopReason:   ch.FinishReason,
		Model:        resp.Model,
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
	}
	if s, ok := ch.Message.Content.(string); ok {
		out.Content = s
	}
	for _, tc := range ch.Message.ToolCalls {
		input := map[string]any{}
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
				return nil, fmt.Errorf("tool %s returned unparseable arguments: %w", tc.Function.Name, err)
			}
		}
		out.ToolCalls = append(out.ToolCalls, harness.ToolCall{
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}
	return out, nil
}
