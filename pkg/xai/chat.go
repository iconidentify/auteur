package xai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Message is a chat message. Content is either a plain string or a slice of
// ContentPart for multimodal input; use TextMessage / MultiMessage helpers.
type Message struct {
	Role       string          `json:"role"`
	Content    any             `json:"content,omitempty"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Reasoning  json.RawMessage `json:"reasoning_content,omitempty"`
}

type ContentPart struct {
	Type     string    `json:"type"` // "text" | "image_url"
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageRef `json:"image_url,omitempty"`
}

type ImageRef struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

func TextMessage(role, text string) Message {
	return Message{Role: role, Content: text}
}

// ImagePart builds an image content part from a local file, inlined as a
// base64 data URI.
func ImagePart(path string) (ContentPart, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ContentPart{}, err
	}
	mime := mimeForExt(filepath.Ext(path))
	uri := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
	return ContentPart{Type: "image_url", ImageURL: &ImageRef{URL: uri, Detail: "high"}}, nil
}

func mimeForExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

// Tool is a function tool definition in OpenAI-compatible format.
type Tool struct {
	Type     string       `json:"type"` // always "function"
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type ChatRequest struct {
	Model       string         `json:"model"`
	Messages    []Message      `json:"messages"`
	Tools       []Tool         `json:"tools,omitempty"`
	ToolChoice  any            `json:"tool_choice,omitempty"`
	Temperature *float64       `json:"temperature,omitempty"`
	MaxTokens   int            `json:"max_tokens,omitempty"`
	Stream      bool           `json:"stream,omitempty"`
	StreamOpts  *StreamOptions `json:"stream_options,omitempty"`
}

// StreamOptions requests usage reporting on streamed completions; without
// it the final usage chunk is omitted and token accounting reads zero.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ChatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
}

// ChatCompletion performs a non-streaming chat completion.
func (c *Client) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	req.Stream = false
	resp, err := c.do(ctx, http.MethodPost, "/chat/completions", req)
	if err != nil {
		return nil, err
	}
	return decodeOrError[ChatResponse](resp)
}

// StreamDelta is one incremental update from a streaming completion.
type StreamDelta struct {
	Text      string // assistant text delta
	Reasoning string // reasoning/thinking delta, if the model exposes it
}

// streamChunk mirrors the SSE chunk shape of chat.completions.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Role             string     `json:"role"`
			Content          string     `json:"content"`
			ReasoningContent string     `json:"reasoning_content"`
			ToolCalls        []struct { // deltas keyed by index
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

// ChatCompletionStream performs a streaming completion, invoking onDelta for
// each token delta, and returns the fully assembled final message.
func (c *Client) ChatCompletionStream(ctx context.Context, req ChatRequest, onDelta func(StreamDelta)) (*ChatResponse, error) {
	req.Stream = true
	req.StreamOpts = &StreamOptions{IncludeUsage: true}
	resp, err := c.do(ctx, http.MethodPost, "/chat/completions", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		data, _ := io.ReadAll(resp.Body)
		return nil, &APIError{Status: resp.StatusCode, Body: string(data)}
	}

	final := &ChatResponse{}
	var content, reasoning strings.Builder
	var finish string
	toolCalls := map[int]*ToolCall{}
	maxIdx := -1

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		payload := bytes.TrimPrefix(line, []byte("data: "))
		if bytes.Equal(payload, []byte("[DONE]")) {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal(payload, &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			final.Usage = *chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.FinishReason != "" {
			finish = ch.FinishReason
		}
		d := ch.Delta
		if d.Content != "" || d.ReasoningContent != "" {
			content.WriteString(d.Content)
			reasoning.WriteString(d.ReasoningContent)
			if onDelta != nil {
				onDelta(StreamDelta{Text: d.Content, Reasoning: d.ReasoningContent})
			}
		}
		for _, tc := range d.ToolCalls {
			cur, ok := toolCalls[tc.Index]
			if !ok {
				cur = &ToolCall{}
				toolCalls[tc.Index] = cur
				if tc.Index > maxIdx {
					maxIdx = tc.Index
				}
			}
			if tc.ID != "" {
				cur.ID = tc.ID
			}
			if tc.Type != "" {
				cur.Type = tc.Type
			}
			if tc.Function.Name != "" {
				cur.Function.Name += tc.Function.Name
			}
			cur.Function.Arguments += tc.Function.Arguments
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}

	msg := Message{Role: "assistant", Content: content.String()}
	for i := 0; i <= maxIdx; i++ {
		if tc, ok := toolCalls[i]; ok {
			msg.ToolCalls = append(msg.ToolCalls, *tc)
		}
	}
	final.Choices = []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	}{{Message: msg, FinishReason: finish}}
	return final, nil
}
