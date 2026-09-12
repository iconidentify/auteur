package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

// LyricSegment is one timed line of vocals.
type LyricSegment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// Transcript is a track's vocal content with timing.
type Transcript struct {
	Language string         `json:"language"`
	Segments []LyricSegment `json:"segments"`
}

// Transcribe runs the local faster-whisper transcriber (CPU; the GPUs belong
// to rendering). A track with no vocals returns zero segments — that is a
// result, not an error. Interpreter and script default to the repo layout
// and can be overridden with AUTEUR_TRANSCRIBER_PY / AUTEUR_TRANSCRIBER.
func Transcribe(ctx context.Context, audioPath string) (*Transcript, error) {
	py := envOrDefault("AUTEUR_TRANSCRIBER_PY", "tools-env/bin/python")
	script := envOrDefault("AUTEUR_TRANSCRIBER", "scripts/transcribe.py")

	cmd := exec.CommandContext(ctx, py, script, audioPath)
	out, err := cmd.Output()
	if err != nil {
		detail := ""
		if ee, ok := err.(*exec.ExitError); ok {
			detail = ": " + truncate(string(ee.Stderr), 400)
		}
		return nil, fmt.Errorf("transcribe: %w%s", err, detail)
	}
	var t Transcript
	if err := json.Unmarshal(out, &t); err != nil {
		return nil, fmt.Errorf("transcribe output: %w", err)
	}
	return &t, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// SliceAudio cuts [from, from+duration) out of an audio file into a stereo
// 44.1k wav, padding with silence if the source runs out, so the slice is
// exactly duration seconds — the contract a lip-sync reference needs.
func SliceAudio(ctx context.Context, srcPath string, from, duration float64, destPath string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-v", "error",
		"-ss", fmt.Sprintf("%.3f", from), "-i", srcPath,
		"-t", fmt.Sprintf("%.3f", duration),
		"-af", fmt.Sprintf("apad=whole_dur=%.3f", duration),
		"-ac", "2", "-ar", "44100", destPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("slice audio: %w: %s", err, truncate(string(out), 400))
	}
	return nil
}
