package agents

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"auteur/internal/media"
	"auteur/pkg/elevenlabs"
	"auteur/pkg/harness"
)

// RegisterSpeechTools adds voice casting and text-to-speech: listing the
// account's voices, voicing a line into a WAV, and joining per-shot audio into
// one continuous track. Registered only when an ElevenLabs client is wired.
func (t *Toolbox) RegisterSpeechTools(reg *harness.Registry) {
	if t.Eleven == nil {
		return
	}

	reg.Register(harness.ToolDefinition{
		Name: "list_voices",
		Description: "List the voices available for casting: id, name, gender, age, accent, and a " +
			"short character description. Cast the NARRATOR and every speaking character with a " +
			"voice that fits their gender, age, and temperament, and record the choices in " +
			"analysis/cast.json so every later line is voiced consistently.",
		InputSchema: harness.Obj(map[string]any{}),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		voices, err := t.Eleven.ListVoices(ctx)
		if err != nil {
			return "", err
		}
		type voice struct {
			ID, Name, Gender, Age, Accent, Description string
		}
		var out []voice
		for _, v := range voices {
			l := v.Labels
			out = append(out, voice{
				ID: v.ID, Name: v.Name, Gender: l["gender"], Age: l["age"], Accent: l["accent"],
				Description: firstNonEmpty(l["description"], v.Description),
			})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return jsonOut(out)
	})

	reg.Register(harness.ToolDefinition{
		Name: "synthesize_speech",
		Description: "Voice one line of narration or dialogue with ElevenLabs into a WAV under audio/. " +
			"Returns the path, the exact duration in seconds, and clip_seconds: the whole-second " +
			"length (min 4, max 15) the shot carrying this line must be rendered to. Voice dialogue " +
			"with the speaking character's voice and narration with the narrator's. Pass " +
			"previous_text/next_text (the neighbouring lines) so delivery flows across the film.",
		InputSchema: harness.Obj(map[string]any{
			"id":            harness.Str("Line id, used for the filename (e.g. s01-l1)"),
			"text":          harness.Str("The exact words to speak"),
			"voice_id":      harness.Str("ElevenLabs voice id, from list_voices / analysis/cast.json"),
			"previous_text": harness.Str("The line spoken just before this one (optional, for flow)"),
			"next_text":     harness.Str("The line spoken just after this one (optional, for flow)"),
			"stability":     harness.Num("0-1; lower gives more emotional range (optional)"),
			"style":         harness.Num("0-1; style exaggeration (optional)"),
			"speed":         harness.Num("Playback speed 0.7-1.2 (optional, default 1.0)"),
		}, "id", "text", "voice_id"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		id := sanitizeSlug(str(input, "id"))
		text := strings.TrimSpace(str(input, "text"))
		if id == "" || text == "" {
			return "", fmt.Errorf("id and text are required")
		}
		dir := filepath.Join(t.Workspace, "audio")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		mp3 := filepath.Join(dir, id+".mp3")
		wav := filepath.Join(dir, id+".wav")
		t.emit(ctx, "status", "voicing "+id, map[string]any{"state": "voicing", "line_id": id})
		data, err := t.Eleven.Synthesize(ctx, elevenlabs.SpeechRequest{
			Text:         text,
			VoiceID:      str(input, "voice_id"),
			PreviousText: str(input, "previous_text"),
			NextText:     str(input, "next_text"),
			Stability:    num(input, "stability"),
			Style:        num(input, "style"),
			Speed:        num(input, "speed"),
		}, "mp3_44100_128")
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(mp3, data, 0o644); err != nil {
			return "", err
		}
		if err := media.ToWAV(ctx, mp3, wav); err != nil {
			return "", err
		}
		os.Remove(mp3)
		probe, err := media.Probe(ctx, wav)
		if err != nil {
			return "", fmt.Errorf("probe speech: %w", err)
		}
		rel := t.rel(wav)
		t.emit(ctx, "artifact", fmt.Sprintf("voiced %s (%.1fs)", id, probe.Duration), map[string]any{
			"kind": "audio", "line_id": id, "path": rel, "duration": probe.Duration,
			"voice_id": str(input, "voice_id"),
		})
		return jsonOut(map[string]any{
			"id":               id,
			"path":             rel,
			"duration_seconds": probe.Duration,
			"clip_seconds":     clipSecondsFor(probe.Duration),
		})
	})

	reg.Register(harness.ToolDefinition{
		Name: "concat_audio",
		Description: "Join audio files end to end into one 44.1kHz stereo WAV: the film's continuous " +
			"narration/dialogue track, built from the per-line audio in SHOT ORDER. Pass the result " +
			"to stitch_timeline as music_path; because each timeline clip runs exactly its line's " +
			"duration, picture and words line up automatically.",
		InputSchema: harness.Obj(map[string]any{
			"paths":    harness.Arr("Audio files in timeline order", harness.Str("Audio file path")),
			"out_name": harness.Str("Output filename under audio/ (default narration_track.wav)"),
		}, "paths"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		rawPaths, _ := input["paths"].([]any)
		var paths []string
		for _, p := range rawPaths {
			if s, ok := p.(string); ok && strings.TrimSpace(s) != "" {
				paths = append(paths, t.resolve(s))
			}
		}
		if len(paths) == 0 {
			return "", fmt.Errorf("paths is required")
		}
		outName := str(input, "out_name")
		if outName == "" {
			outName = "narration_track.wav"
		}
		out := filepath.Join(t.Workspace, "audio", filepath.Base(outName))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return "", err
		}
		if err := media.ConcatAudio(ctx, paths, out); err != nil {
			return "", err
		}
		probe, _ := media.Probe(ctx, out)
		rel := t.rel(out)
		data := map[string]any{"kind": "audio", "path": rel}
		if probe != nil {
			data["duration"] = probe.Duration
		}
		t.emit(ctx, "artifact", "narration track assembled: "+rel, data)
		return jsonOut(map[string]any{"path": rel, "probe": probe})
	})
}

// clipSecondsFor is the whole-second clip length that carries a spoken line on
// the hosted camera: the speech rounded up, never under 4s, never over 15s.
func clipSecondsFor(speechSeconds float64) int {
	s := int(math.Ceil(speechSeconds))
	if s < 4 {
		s = 4
	}
	if s > 15 {
		s = 15
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
