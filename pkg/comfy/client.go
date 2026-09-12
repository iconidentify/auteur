// Package comfy is a client for a local ComfyUI server: queue an API-format
// workflow, follow it to completion, upload input images, and fetch the
// rendered outputs. It is transport only; workflow graphs are built by the
// model-specific builders in this package (see h3.go).
package comfy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// Client talks to one ComfyUI instance.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// ClientID identifies this process to ComfyUI's queue.
	ClientID string
}

func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL: trimSlash(baseURL),
		// Renders are followed by polling, so the per-request timeout only
		// needs to cover a single call, not the render itself.
		HTTP:     &http.Client{Timeout: 2 * time.Minute},
		ClientID: fmt.Sprintf("auteur-%d", time.Now().UnixNano()),
	}
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("comfyui api error (status %d): %.500s", e.Status, e.Body)
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &APIError{Status: resp.StatusCode, Body: string(data)}
	}
	return data, nil
}

// FileRef locates a file in ComfyUI's input/output/temp trees.
type FileRef struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

// Name is the value a LoadImage node expects for an uploaded image.
func (f FileRef) Name() string {
	if f.Subfolder == "" {
		return f.Filename
	}
	return f.Subfolder + "/" + f.Filename
}

type systemStats struct {
	System struct {
		ComfyUIVersion string `json:"comfyui_version"`
	} `json:"system"`
	Devices []struct {
		Name      string `json:"name"`
		VRAMTotal int64  `json:"vram_total"`
		VRAMFree  int64  `json:"vram_free"`
	} `json:"devices"`
}

// Ping verifies the server is reachable and returns a short description of it.
func (c *Client) Ping(ctx context.Context) (string, error) {
	data, err := c.do(ctx, http.MethodGet, "/system_stats", nil)
	if err != nil {
		return "", err
	}
	var st systemStats
	if err := json.Unmarshal(data, &st); err != nil {
		return "", fmt.Errorf("unmarshal system_stats: %w", err)
	}
	return fmt.Sprintf("ComfyUI %s, %d device(s)", st.System.ComfyUIVersion, len(st.Devices)), nil
}

// UploadImage puts a local image into ComfyUI's input tree so a LoadImage node
// can read it, and returns the reference to use as that node's image name.
func (c *Client) UploadImage(ctx context.Context, localPath, subfolder string) (FileRef, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return FileRef{}, err
	}
	defer f.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("image", filepath.Base(localPath))
	if err != nil {
		return FileRef{}, err
	}
	if _, err := io.Copy(part, f); err != nil {
		return FileRef{}, err
	}
	mw.WriteField("type", "input")
	if subfolder != "" {
		mw.WriteField("subfolder", subfolder)
	}
	// Same basename from a different production must not silently win.
	mw.WriteField("overwrite", "true")
	if err := mw.Close(); err != nil {
		return FileRef{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/upload/image", &buf)
	if err != nil {
		return FileRef{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return FileRef{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return FileRef{}, &APIError{Status: resp.StatusCode, Body: string(data)}
	}
	// The upload endpoint names the file "name"; every other endpoint calls
	// the same field "filename".
	var up struct {
		Name      string `json:"name"`
		Subfolder string `json:"subfolder"`
		Type      string `json:"type"`
	}
	if err := json.Unmarshal(data, &up); err != nil {
		return FileRef{}, fmt.Errorf("unmarshal upload response: %w (body: %.200s)", err, data)
	}
	if up.Name == "" {
		return FileRef{}, fmt.Errorf("upload returned no filename (body: %.200s)", data)
	}
	return FileRef{Filename: up.Name, Subfolder: up.Subfolder, Type: up.Type}, nil
}

type queueResponse struct {
	PromptID string         `json:"prompt_id"`
	Number   int            `json:"number"`
	NodeErrs map[string]any `json:"node_errors"`
}

// Queue submits an API-format workflow graph and returns its prompt id.
func (c *Client) Queue(ctx context.Context, graph map[string]any) (string, error) {
	data, err := c.do(ctx, http.MethodPost, "/prompt", map[string]any{
		"prompt":    graph,
		"client_id": c.ClientID,
	})
	if err != nil {
		return "", err
	}
	var out queueResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("unmarshal queue response: %w (body: %.300s)", err, data)
	}
	if out.PromptID == "" {
		return "", fmt.Errorf("queue returned no prompt_id (body: %.300s)", data)
	}
	if len(out.NodeErrs) > 0 {
		return "", fmt.Errorf("workflow rejected: %v", out.NodeErrs)
	}
	return out.PromptID, nil
}

// Progress is a snapshot of a queued prompt, reported while waiting.
type Progress struct {
	Elapsed  time.Duration
	Running  bool // this prompt is the one executing
	Position int  // 0 when running, else places ahead in the queue
}

type historyEntry struct {
	Status struct {
		StatusStr string `json:"status_str"`
		Completed bool   `json:"completed"`
		Messages  []any  `json:"messages"`
	} `json:"status"`
	Outputs map[string]struct {
		Images []FileRef `json:"images"`
		Videos []FileRef `json:"videos"`
		GIFs   []FileRef `json:"gifs"`
	} `json:"outputs"`
}

// Result is a finished prompt's media outputs.
type Result struct {
	PromptID string
	Files    []FileRef
}

type queueState struct {
	Running []json.RawMessage `json:"queue_running"`
	Pending []json.RawMessage `json:"queue_pending"`
}

// queuePosition reports whether promptID is running and how many prompts sit
// ahead of it. A prompt that has left the queue reports (false, -1).
func (c *Client) queuePosition(ctx context.Context, promptID string) (running bool, ahead int) {
	data, err := c.do(ctx, http.MethodGet, "/queue", nil)
	if err != nil {
		return false, -1
	}
	var q queueState
	if err := json.Unmarshal(data, &q); err != nil {
		return false, -1
	}
	for _, raw := range q.Running {
		if entryPromptID(raw) == promptID {
			return true, 0
		}
	}
	for i, raw := range q.Pending {
		if entryPromptID(raw) == promptID {
			return false, i + 1 + len(q.Running)
		}
	}
	return false, -1
}

// entryPromptID pulls the prompt id out of a queue tuple, whose second element
// is the id: [number, prompt_id, prompt, extra_data, outputs].
func entryPromptID(raw json.RawMessage) string {
	var tuple []json.RawMessage
	if err := json.Unmarshal(raw, &tuple); err != nil || len(tuple) < 2 {
		return ""
	}
	var id string
	if err := json.Unmarshal(tuple[1], &id); err != nil {
		return ""
	}
	return id
}

// Wait follows a queued prompt to completion, calling onProgress (optional)
// after each poll. If ctx is cancelled the prompt is removed from the queue or
// interrupted, so a cancelled production does not leave the GPUs busy.
func (c *Client) Wait(ctx context.Context, promptID string, interval time.Duration, onProgress func(Progress)) (*Result, error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	start := time.Now()
	for {
		entry, err := c.history(ctx, promptID)
		if err != nil {
			return nil, err
		}
		if entry != nil {
			switch entry.Status.StatusStr {
			case "success":
				return &Result{PromptID: promptID, Files: mediaFiles(entry)}, nil
			case "error":
				return nil, fmt.Errorf("render failed: %s", firstError(entry))
			}
		}

		running, ahead := c.queuePosition(ctx, promptID)
		if entry == nil && ahead < 0 && time.Since(start) > 30*time.Second {
			// Neither in the queue nor in history: the server dropped it.
			return nil, fmt.Errorf("prompt %s vanished from the queue without a result", promptID)
		}
		if onProgress != nil {
			onProgress(Progress{Elapsed: time.Since(start), Running: running, Position: ahead})
		}

		select {
		case <-ctx.Done():
			c.cancel(promptID, running)
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// cancel clears a prompt from the queue on its own context, since the caller's
// is already cancelled.
func (c *Client) cancel(promptID string, running bool) {
	ctx, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	c.do(ctx, http.MethodPost, "/queue", map[string]any{"delete": []string{promptID}})
	if running {
		c.do(ctx, http.MethodPost, "/interrupt", map[string]any{})
	}
}

func (c *Client) history(ctx context.Context, promptID string) (*historyEntry, error) {
	data, err := c.do(ctx, http.MethodGet, "/history/"+url.PathEscape(promptID), nil)
	if err != nil {
		// A prompt that has not finished yet may 404; that is not fatal.
		if ae, ok := err.(*APIError); ok && ae.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	var hist map[string]historyEntry
	if err := json.Unmarshal(data, &hist); err != nil {
		return nil, fmt.Errorf("unmarshal history: %w", err)
	}
	entry, ok := hist[promptID]
	if !ok {
		return nil, nil
	}
	return &entry, nil
}

// mediaFiles collects every output file across nodes. SaveVideo reports its
// mp4 under "images" with an "animated" flag, so all three lists are read.
func mediaFiles(e *historyEntry) []FileRef {
	var out []FileRef
	for _, node := range e.Outputs {
		out = append(out, node.Videos...)
		out = append(out, node.GIFs...)
		out = append(out, node.Images...)
	}
	return out
}

func firstError(e *historyEntry) string {
	if len(e.Status.Messages) == 0 {
		return e.Status.StatusStr
	}
	b, err := json.Marshal(e.Status.Messages)
	if err != nil {
		return e.Status.StatusStr
	}
	return fmt.Sprintf("%.600s", b)
}

// Download fetches an output file to a local path.
func (c *Client) Download(ctx context.Context, ref FileRef, destPath string) error {
	typ := ref.Type
	if typ == "" {
		typ = "output"
	}
	q := url.Values{}
	q.Set("filename", ref.Filename)
	q.Set("subfolder", ref.Subfolder)
	q.Set("type", typ)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/view?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return &APIError{Status: resp.StatusCode, Body: string(body)}
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return nil
}
