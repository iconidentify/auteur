package xai

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// VideoImage is an image input to video generation. The API requires this
// object form; bare URL strings are rejected with a 422 despite what the
// docs examples show.
type VideoImage struct {
	URL  string `json:"url"`
	Type string `json:"type"` // always "image_url"
}

// VideoRequest covers text-to-video, image-to-video and reference-to-video.
// Image inputs accept public URLs or data URIs (see DataURI).
type VideoRequest struct {
	Model           string       `json:"model"`
	Prompt          string       `json:"prompt"`
	Image           *VideoImage  `json:"image,omitempty"`
	ReferenceImages []VideoImage `json:"reference_images,omitempty"`
	Duration        int          `json:"duration,omitempty"`     // 1-15 seconds
	AspectRatio     string       `json:"aspect_ratio,omitempty"` // 16:9, 9:16, 1:1, 4:3, 3:4, 3:2, 2:3
	Resolution      string       `json:"resolution,omitempty"`   // 480p, 720p, 1080p
}

// VideoImageFromFile inlines a local image file as a video image input.
func VideoImageFromFile(path string) (VideoImage, error) {
	uri, err := DataURI(path)
	if err != nil {
		return VideoImage{}, err
	}
	return VideoImage{URL: uri, Type: "image_url"}, nil
}

type videoSubmitResponse struct {
	RequestID string `json:"request_id"`
}

type VideoStatus struct {
	Status string `json:"status"` // pending | done | expired | failed
	Model  string `json:"model"`
	Video  *struct {
		URL      string  `json:"url"`
		Duration float64 `json:"duration"`
	} `json:"video"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// DataURI inlines a local image file as a base64 data URI for video/image inputs.
func DataURI(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	mime := mimeForExt(filepath.Ext(path))
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// SubmitVideo starts a video generation and returns the request id.
func (c *Client) SubmitVideo(ctx context.Context, req VideoRequest) (string, error) {
	if req.Model == "" {
		req.Model = DefaultVideoModel
	}
	resp, err := c.do(ctx, http.MethodPost, "/videos/generations", req)
	if err != nil {
		return "", err
	}
	out, err := decodeOrError[videoSubmitResponse](resp)
	if err != nil {
		return "", err
	}
	if out.RequestID == "" {
		return "", fmt.Errorf("video submit: empty request_id")
	}
	return out.RequestID, nil
}

// PollVideo fetches the current status of a video generation request.
func (c *Client) PollVideo(ctx context.Context, requestID string) (*VideoStatus, error) {
	resp, err := c.do(ctx, http.MethodGet, "/videos/"+requestID, nil)
	if err != nil {
		return nil, err
	}
	return decodeOrError[VideoStatus](resp)
}

// WaitVideo polls until the generation finishes. onPoll (optional) is called
// after each poll with the elapsed time and latest status.
func (c *Client) WaitVideo(ctx context.Context, requestID string, interval time.Duration, onPoll func(elapsed time.Duration, st *VideoStatus)) (*VideoStatus, error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	start := time.Now()
	for {
		st, err := c.PollVideo(ctx, requestID)
		if err != nil {
			return nil, err
		}
		if onPoll != nil {
			onPoll(time.Since(start), st)
		}
		switch st.Status {
		case "done":
			return st, nil
		case "failed":
			msg := "unknown error"
			if st.Error != nil {
				msg = st.Error.Code + ": " + st.Error.Message
			}
			return st, fmt.Errorf("video generation failed: %s", msg)
		case "expired":
			return st, fmt.Errorf("video generation request expired")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}
