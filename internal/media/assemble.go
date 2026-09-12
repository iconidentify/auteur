package media

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"strings"
)

// TimelineClip is one edit decision: take source from In for Duration seconds.
type TimelineClip struct {
	Path     string  `json:"path"`
	In       float64 `json:"in_seconds"`       // trim start within the source clip
	Duration float64 `json:"duration_seconds"` // how long this clip runs on the timeline
	// TransitionSec dissolves INTO this clip from the previous one over this
	// many seconds; 0 falls back to the spec-wide CrossfadeSec (usually a
	// hard cut). Ignored on the first clip.
	TransitionSec float64 `json:"transition_seconds,omitempty"`
}

// StitchSpec describes the final assembly.
type StitchSpec struct {
	Clips        []TimelineClip `json:"clips"`
	MusicPath    string         `json:"music_path"`
	MusicOffset  float64        `json:"music_offset_seconds,omitempty"`
	Width        int            `json:"width"`
	Height       int            `json:"height"`
	FPS          int            `json:"fps,omitempty"`
	CrossfadeSec float64        `json:"crossfade_seconds,omitempty"` // 0 = hard cuts
	AudioFadeOut float64        `json:"audio_fade_out_seconds,omitempty"`
	OutPath      string         `json:"out_path"`
}

// Stitch renders the timeline to OutPath using a single ffmpeg filter graph.
// Video comes from the clips; audio comes exclusively from the music track.
func Stitch(ctx context.Context, spec StitchSpec) error {
	if len(spec.Clips) == 0 {
		return fmt.Errorf("no clips in timeline")
	}
	if spec.FPS == 0 {
		spec.FPS = 24
	}
	if spec.Width == 0 || spec.Height == 0 {
		spec.Width, spec.Height = 1280, 720
	}
	for _, c := range spec.Clips {
		if c.Duration <= 0 {
			return fmt.Errorf("clip %s has non-positive duration", c.Path)
		}
	}

	args := []string{"-y", "-v", "error"}
	for _, c := range spec.Clips {
		args = append(args, "-i", c.Path)
	}
	args = append(args, "-i", spec.MusicPath)
	musicIdx := len(spec.Clips)

	graph, last, total := buildVideoGraph(spec)
	var fg strings.Builder
	fg.WriteString(graph)

	// Music bed: offset, clamp to timeline length, optional fade-out.
	fmt.Fprintf(&fg, "[%d:a:0]atrim=start=%.4f:duration=%.4f,asetpts=PTS-STARTPTS", musicIdx, spec.MusicOffset, total)
	if spec.AudioFadeOut > 0 {
		fmt.Fprintf(&fg, ",afade=t=out:st=%.4f:d=%.4f", total-spec.AudioFadeOut, spec.AudioFadeOut)
	}
	fmt.Fprintf(&fg, "[aout]")

	args = append(args,
		"-filter_complex", fg.String(),
		"-map", "["+last+"]", "-map", "[aout]",
		"-c:v", "libx264", "-preset", "medium", "-crf", "18", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "256k",
		"-movflags", "+faststart",
		spec.OutPath,
	)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg stitch: %w: %s", err, truncate(string(out), 2000))
	}
	return nil
}

// buildVideoGraph builds the filter graph for the timeline video: per-clip
// normalization, then joins — a hard concat by default, an xfade dissolve
// where a clip asks for one (TransitionSec, else the spec-wide CrossfadeSec).
// Returns the graph, the final video label, and the timeline's total seconds.
func buildVideoGraph(spec StitchSpec) (graph, last string, total float64) {
	var fg strings.Builder
	// Each clip is cut to a whole number of frames. Rounding every clip on
	// its own always rounds up, and over a narrated film of forty cuts that
	// drifts the picture two-thirds of a second behind a continuous voice
	// track. Instead the frame count is chosen cumulatively, so the running
	// total never strays more than half a frame from the requested timeline.
	fps := float64(spec.FPS)
	frameDur := make([]float64, len(spec.Clips))
	exact, placed := 0.0, 0
	for i, c := range spec.Clips {
		exact += c.Duration
		n := int(math.Round(exact*fps)) - placed
		if n < 1 {
			n = 1
		}
		placed += n
		frameDur[i] = float64(n) / fps
		// [i:v:0] pins the first real video stream: generated clips also
		// carry an attached mjpeg cover stream that must not be selected.
		// The source trim keeps a frame of headroom; the exact cut happens
		// on the output frame grid with end_frame.
		fmt.Fprintf(&fg,
			"[%d:v:0]trim=start=%.4f:duration=%.4f,setpts=PTS-STARTPTS,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=%d,trim=end_frame=%d,setpts=PTS-STARTPTS[v%d];",
			i, c.In, c.Duration+1/fps, spec.Width, spec.Height, spec.Width, spec.Height, spec.FPS, n, i)
	}

	prev := "v0"
	running := frameDur[0]
	for i := 1; i < len(spec.Clips); i++ {
		trans := spec.Clips[i].TransitionSec
		if trans == 0 {
			trans = spec.CrossfadeSec
		}
		// A dissolve cannot be longer than what is already on the timeline
		// or than the incoming clip.
		if trans > frameDur[i] {
			trans = frameDur[i]
		}
		if trans > running {
			trans = running
		}
		out := fmt.Sprintf("j%d", i)
		if trans > 0 {
			fmt.Fprintf(&fg, "[%s][v%d]xfade=transition=fade:duration=%.4f:offset=%.4f[%s];",
				prev, i, trans, running-trans, out)
			running += frameDur[i] - trans
		} else {
			fmt.Fprintf(&fg, "[%s][v%d]concat=n=2:v=1:a=0[%s];", prev, i, out)
			running += frameDur[i]
		}
		prev = out
	}
	return fg.String(), prev, running
}

// MasterAudio runs two-pass loudnorm on the audio of a finished video,
// re-muxing the untouched video stream. Target is EBU R128 loudness.
func MasterAudio(ctx context.Context, inPath, outPath string, targetLUFS float64) error {
	if targetLUFS == 0 {
		targetLUFS = -14 // streaming-platform standard
	}
	// Pass 1: measure.
	measureArgs := []string{"-v", "info", "-i", inPath,
		"-af", fmt.Sprintf("loudnorm=I=%.1f:TP=-1.0:LRA=11:print_format=json", targetLUFS),
		"-f", "null", "-"}
	cmd := exec.CommandContext(ctx, "ffmpeg", measureArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("loudnorm measure: %w: %s", err, truncate(string(out), 1000))
	}
	stats, err := parseLoudnormJSON(string(out))
	if err != nil {
		return fmt.Errorf("parse loudnorm stats: %w", err)
	}

	// Pass 2: apply with measured values for a transparent linear normalization.
	filter := fmt.Sprintf(
		"loudnorm=I=%.1f:TP=-1.0:LRA=11:measured_I=%s:measured_TP=%s:measured_LRA=%s:measured_thresh=%s:offset=%s:linear=true",
		targetLUFS, stats["input_i"], stats["input_tp"], stats["input_lra"], stats["input_thresh"], stats["target_offset"])
	applyArgs := []string{"-y", "-v", "error", "-i", inPath,
		"-c:v", "copy", "-af", filter, "-c:a", "aac", "-b:a", "256k",
		"-movflags", "+faststart", outPath}
	cmd = exec.CommandContext(ctx, "ffmpeg", applyArgs...)
	out, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("loudnorm apply: %w: %s", err, truncate(string(out), 1000))
	}
	return nil
}

var loudnormBlock = regexp.MustCompile(`(?s)\{[^{}]*"input_i"[^{}]*\}`)

func parseLoudnormJSON(ffmpegOutput string) (map[string]string, error) {
	m := loudnormBlock.FindString(ffmpegOutput)
	if m == "" {
		return nil, fmt.Errorf("no loudnorm JSON block in ffmpeg output")
	}
	var stats map[string]string
	if err := json.Unmarshal([]byte(m), &stats); err != nil {
		return nil, err
	}
	return stats, nil
}

// ExtractFrame grabs a single frame as JPEG for thumbnails.
func ExtractFrame(ctx context.Context, videoPath string, atSeconds float64, outPath string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-v", "error",
		"-ss", fmt.Sprintf("%.3f", atSeconds), "-i", videoPath,
		"-frames:v", "1", "-q:v", "3", outPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("extract frame: %w: %s", err, truncate(string(out), 500))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
