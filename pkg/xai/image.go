package xai

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
)

type ImageRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n,omitempty"`
	AspectRatio    string `json:"aspect_ratio,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"` // "url" | "b64_json"
}

type ImageData struct {
	URL           string `json:"url"`
	B64JSON       string `json:"b64_json"`
	RevisedPrompt string `json:"revised_prompt"`
}

type ImageResponse struct {
	Data []ImageData `json:"data"`
}

// GenerateImage generates images from a prompt. Returned entries carry either
// a URL or base64 payload depending on ResponseFormat.
func (c *Client) GenerateImage(ctx context.Context, req ImageRequest) (*ImageResponse, error) {
	if req.Model == "" {
		req.Model = DefaultImageModel
	}
	resp, err := c.do(ctx, http.MethodPost, "/images/generations", req)
	if err != nil {
		return nil, err
	}
	return decodeOrError[ImageResponse](resp)
}

// SaveImage writes one ImageResponse entry to destPath, downloading or
// decoding as needed.
func (c *Client) SaveImage(ctx context.Context, entry ImageData, destPath string) error {
	if entry.B64JSON != "" {
		data, err := base64.StdEncoding.DecodeString(entry.B64JSON)
		if err != nil {
			return fmt.Errorf("decode image: %w", err)
		}
		return os.WriteFile(destPath, data, 0o644)
	}
	if entry.URL != "" {
		return c.DownloadFile(ctx, entry.URL, destPath)
	}
	return fmt.Errorf("image entry has neither url nor b64 payload")
}
