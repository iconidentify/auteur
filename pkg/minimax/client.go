// Package minimax is a client for the MiniMax hosted video generation API
// (https://api.minimax.io) — the V2 asynchronous workflow: submit a task,
// poll it to completion, then download the resulting clip. It renders with the
// MiniMax-H3 model, the same family auteur runs locally on ComfyUI, but off
// this box's GPUs entirely.
package minimax

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultBaseURL is the international MiniMax API host.
const DefaultBaseURL = "https://api.minimax.io"

// Client talks to the MiniMax video generation API.
type Client struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
}

// NewClient returns a client for the given API key against the default host.
func NewClient(apiKey string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: 10 * time.Minute},
	}
}

// URLRef is an image_url / audio_url object: either an https URL or a data: URI.
type URLRef struct {
	URL string `json:"url"`
}

// Content is one multimodal item in a video request.
type Content struct {
	Type     string  `json:"type"` // "text" | "image_url" | "audio_url"
	Text     string  `json:"text,omitempty"`
	ImageURL *URLRef `json:"image_url,omitempty"`
	AudioURL *URLRef `json:"audio_url,omitempty"`
	Role     string  `json:"role,omitempty"` // first_frame | last_frame | reference_image
}

// VideoRequest is the body of POST /v2/video_generation.
type VideoRequest struct {
	Model      string    `json:"model"`
	Content    []Content `json:"content"`
	Duration   int       `json:"duration,omitempty"`
	Resolution string    `json:"resolution,omitempty"` // "768P" | "2K"
	Ratio      string    `json:"ratio,omitempty"`      // "16:9" ... or "adaptive"
}

type baseResp struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
}

type submitResp struct {
	TaskID   string   `json:"task_id"`
	BaseResp baseResp `json:"base_resp"`
}

// SubmitVideo creates a generation task and returns its task_id.
func (c *Client) SubmitVideo(ctx context.Context, req VideoRequest) (string, error) {
	var out submitResp
	if err := c.do(ctx, http.MethodPost, "/v2/video_generation", req, &out); err != nil {
		return "", err
	}
	if out.BaseResp.StatusCode != 0 {
		return "", fmt.Errorf("minimax submit: %d %s", out.BaseResp.StatusCode, out.BaseResp.StatusMsg)
	}
	if out.TaskID == "" {
		return "", fmt.Errorf("minimax submit: no task_id in response")
	}
	return out.TaskID, nil
}

// TaskInfo is the "task" object inside a query response.
type TaskInfo struct {
	ID       string  `json:"id"`
	Status   string  `json:"status"` // preparing | queueing | running | succeeded | failed | cancelled
	Duration float64 `json:"duration"`
	Content  *struct {
		URL string `json:"url"` // populated once status is succeeded
	} `json:"content"`
}

// VideoStatus is the GET /v2/query/video_generation/{task_id} response, which
// wraps the task state in a "task" object.
type VideoStatus struct {
	Task     TaskInfo `json:"task"`
	BaseResp baseResp `json:"base_resp"`
	// Raw is the response body as received, kept so a failed task can be
	// reported with whatever reason MiniMax attached, wherever it put it.
	Raw json.RawMessage `json:"-"`
}

// terminal reports whether the task reached a final state and whether it succeeded.
func (s *VideoStatus) terminal() (done, ok bool) {
	switch strings.ToLower(s.Task.Status) {
	case "succeeded", "success":
		return true, true
	case "failed", "fail", "cancelled", "canceled":
		return true, false
	}
	return false, false
}

// QueryVideo polls one task's status.
func (c *Client) QueryVideo(ctx context.Context, taskID string) (*VideoStatus, error) {
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/v2/query/video_generation/"+taskID, nil, &raw); err != nil {
		return nil, err
	}
	var out VideoStatus
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("minimax query: decode: %w", err)
	}
	out.Raw = raw
	return &out, nil
}

// WaitVideo polls a task until it finishes, calling onProgress each tick.
// Transient query errors are tolerated (the network or the API blips); only a
// terminal failure status or a cancelled context ends the wait.
func (c *Client) WaitVideo(ctx context.Context, taskID string, interval time.Duration, onProgress func(elapsed time.Duration, st *VideoStatus)) (*VideoStatus, error) {
	start := time.Now()
	for {
		st, err := c.QueryVideo(ctx, taskID)
		if err == nil {
			if onProgress != nil {
				onProgress(time.Since(start), st)
			}
			if done, ok := st.terminal(); done {
				if !ok {
					msg := st.Task.Status
					if strings.Contains(string(st.Raw), "voice sensitive") || strings.Contains(string(st.Raw), `"1040"`) {
						// The reference AUDIO was refused: MiniMax's speech moderation
						// objected to the spoken line, not the picture.
						msg = "rejected by MiniMax AUDIO moderation (code 1040 voice sensitive): the spoken line " +
							"itself tripped speech moderation, so it cannot be lip-synced on this camera. Re-render " +
							"this shot WITHOUT sync_audio_path — describe the character speaking with the mouth " +
							"moving — and let the editor lay the recorded voice over it"
					} else if strings.Contains(string(st.Raw), "new_sensitive") || strings.Contains(string(st.Raw), `"1027"`) {
						// MiniMax rendered the clip and then its output moderation
						// withheld it: the picture, not the prompt, was judged
						// sensitive. Adjectives will not fix that; composition will.
						msg = "rejected by MiniMax OUTPUT moderation (code 1027 new_sensitive): the rendered " +
							"picture was judged sensitive even though the request was accepted. Do not resubmit " +
							"the same composition — reframe so bodies are implied (figures from behind, waist-up, " +
							"in silhouette, or behind foliage) and describe the cloth that covers them"
					} else if st.BaseResp.StatusMsg != "" {
						msg += ": " + st.BaseResp.StatusMsg
					} else if len(st.Raw) > 0 {
						// No status_msg: surface the body so the crew can see
						// a moderation refusal or whatever else MiniMax said.
						body := string(st.Raw)
						if len(body) > 400 {
							body = body[:400] + "…"
						}
						msg += ": " + body
					}
					return st, fmt.Errorf("minimax render %s", msg)
				}
				return st, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// DownloadFile streams a URL (the finished clip) to dest.
func (c *Client) DownloadFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// do performs a JSON request and decodes the response.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("minimax %s %s: status %d: %s", method, path, resp.StatusCode, truncate(string(data), 300))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("minimax %s %s: decode: %w (body: %s)", method, path, err, truncate(string(data), 300))
		}
	}
	return nil
}

// DataURIFromFile base64-encodes a local file into a data: URI. MiniMax accepts
// a hosted URL or an inline data URI in image_url/audio_url; auteur's stills and
// audio live on disk, so it inlines them rather than standing up an uploader.
func DataURIFromFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	ctype := mime.TypeByExtension(filepath.Ext(path))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	return "data:" + ctype + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
