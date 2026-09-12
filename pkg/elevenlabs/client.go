// Package elevenlabs is a client for the ElevenLabs text-to-speech API. Auteur
// uses it to voice narration and character dialogue: each character in a film
// is cast with one of the account's voices, and every line is synthesized in
// that voice before the shot that carries it is rendered.
package elevenlabs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	DefaultBaseURL = "https://api.elevenlabs.io"
	// DefaultModel is the reliable high-quality narration model. eleven_v3 is
	// more expressive but more variable take to take; multilingual_v2 gives
	// consistent delivery across a whole film's worth of lines.
	DefaultModel = "eleven_multilingual_v2"
)

// Client talks to the ElevenLabs API.
type Client struct {
	APIKey  string
	BaseURL string
	Model   string
	HTTP    *http.Client
}

// NewClient returns a client for the given API key.
func NewClient(apiKey string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: DefaultBaseURL,
		Model:   DefaultModel,
		HTTP:    &http.Client{Timeout: 5 * time.Minute},
	}
}

// Voice is one voice available to the account.
type Voice struct {
	ID          string            `json:"voice_id"`
	Name        string            `json:"name"`
	Category    string            `json:"category,omitempty"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// ListVoices returns the voices available to the account.
func (c *Client) ListVoices(ctx context.Context) ([]Voice, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/voices", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("elevenlabs voices: status %d: %s", resp.StatusCode, truncate(string(data), 300))
	}
	var out struct {
		Voices []Voice `json:"voices"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("elevenlabs voices: decode: %w", err)
	}
	return out.Voices, nil
}

// SpeechRequest is one line to voice.
type SpeechRequest struct {
	Text    string
	VoiceID string
	// ModelID overrides the client default when set.
	ModelID string
	// Voice settings; zero values fall back to ElevenLabs defaults.
	Stability       float64
	SimilarityBoost float64
	Style           float64
	Speed           float64
	// PreviousText / NextText give the model surrounding lines so delivery
	// flows naturally across a sequence of separately-synthesized lines.
	PreviousText string
	NextText     string
}

type voiceSettings struct {
	Stability       float64 `json:"stability,omitempty"`
	SimilarityBoost float64 `json:"similarity_boost,omitempty"`
	Style           float64 `json:"style,omitempty"`
	Speed           float64 `json:"speed,omitempty"`
}

type speechBody struct {
	Text          string         `json:"text"`
	ModelID       string         `json:"model_id"`
	VoiceSettings *voiceSettings `json:"voice_settings,omitempty"`
	PreviousText  string         `json:"previous_text,omitempty"`
	NextText      string         `json:"next_text,omitempty"`
}

// Synthesize voices one line and returns the audio bytes in outputFormat
// (e.g. "mp3_44100_128"). The caller writes them to disk.
func (c *Client) Synthesize(ctx context.Context, sr SpeechRequest, outputFormat string) ([]byte, error) {
	if sr.VoiceID == "" {
		return nil, fmt.Errorf("elevenlabs: voice_id is required")
	}
	if sr.Text == "" {
		return nil, fmt.Errorf("elevenlabs: text is required")
	}
	model := sr.ModelID
	if model == "" {
		model = c.Model
	}
	if outputFormat == "" {
		outputFormat = "mp3_44100_128"
	}
	body := speechBody{Text: sr.Text, ModelID: model, PreviousText: sr.PreviousText, NextText: sr.NextText}
	if sr.Stability != 0 || sr.SimilarityBoost != 0 || sr.Style != 0 || sr.Speed != 0 {
		body.VoiceSettings = &voiceSettings{
			Stability: sr.Stability, SimilarityBoost: sr.SimilarityBoost, Style: sr.Style, Speed: sr.Speed,
		}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/v1/text-to-speech/%s?output_format=%s", c.BaseURL, url.PathEscape(sr.VoiceID), url.QueryEscape(outputFormat))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/mpeg")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("elevenlabs tts: status %d: %s", resp.StatusCode, truncate(string(data), 300))
	}
	return data, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
