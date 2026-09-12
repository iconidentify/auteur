package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"auteur/internal/media"
	"auteur/internal/studio"
	"auteur/pkg/comfy"
	"auteur/pkg/elevenlabs"
	"auteur/pkg/harness"
	"auteur/pkg/xai"
)

// Toolbox builds the studio toolset for one agent, bound to a production
// workspace and that agent's event identity so mid-tool progress (video
// render polling, produced artifacts) attributes correctly in the UI.
type Toolbox struct {
	XAI       *xai.Client
	Video     VideoBackend
	Workspace string
	Sink      harness.EventSink
	AgentID   string
	AgentName string
	// Eleven voices narration and dialogue (film mode); nil disables the
	// speech tools.
	Eleven *elevenlabs.Client
	// Mode is the production mode ("" / music-video, or film).
	Mode string

	// Ledger enforces per-shot take budgets and journals spend, durably per
	// production: budgets survive restarts and are shared by every agent in
	// the crew, because a stopping rule that lives only in a prompt (or only
	// in memory) is a hope.
	Ledger *Ledger

	// Run counters back the per-run caps (anti-runaway backstops). They are
	// deliberately in-memory: a resumed production gets a fresh run budget,
	// while the ledger's per-shot budgets still carry over.
	mu   sync.Mutex
	runs map[string]int
}

// countRun bumps this run's counter for kind and returns it.
func (t *Toolbox) countRun(kind string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.runs == nil {
		t.runs = map[string]int{}
	}
	t.runs[kind]++
	return t.runs[kind]
}

func (t *Toolbox) refundRun(kind string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.runs[kind] > 0 {
		t.runs[kind]--
	}
}

// Take budgets. Per target: takes beyond the limit are refused with an
// explanation of what to do instead. Per run: a backstop against loops that
// dodge the per-target family by renaming.
const (
	imageTakeLimit = 3
	clipTakeLimit  = 3
	// Per-run caps by mode. A music video needs a handful of keyframes and a
	// couple dozen clips; a film's style bible needs a sheet per character
	// AND a keyframe per location, and its shot list runs one clip per
	// spoken line, so both caps are wider there.
	stillsPerRunMusic = 14
	clipsPerRunMusic  = 24
	stillsPerRunFilm  = 36
	clipsPerRunFilm   = 96 // a 50-shot chapter plus a round of retakes
)

// runBudgetImages is this run's cap on generated stills.
func (t *Toolbox) runBudgetImages() int {
	if t.Mode == studio.ModeFilm {
		return stillsPerRunFilm
	}
	return stillsPerRunMusic
}

// runBudgetClips is this run's cap on rendered clips.
func (t *Toolbox) runBudgetClips() int {
	if t.Mode == studio.ModeFilm {
		return clipsPerRunFilm
	}
	return clipsPerRunMusic
}

// takeMarker strips explicit take/variant suffixes ("-t3", "_take2", "-v4")
// so retakes under cosmetic renames count against the same target. Plain
// numbered ids like "shot-01" are distinct targets and are not collapsed.
var takeMarker = regexp.MustCompile(`(?:[-_](?:t|v|take|try|retake)\d+)+$`)

// ledger returns the production ledger, lazily creating an in-memory one so
// tests and partial wiring never nil-panic.
func (t *Toolbox) ledger() *Ledger {
	if t.Ledger == nil {
		t.Ledger = OpenLedger(t.Workspace)
	}
	return t.Ledger
}

func (t *Toolbox) countTake(kind, id string) (target, run int) {
	target, _ = t.ledger().CountTake(kind, id)
	return target, t.countRun(kind)
}

func (t *Toolbox) refundTake(kind, id string) {
	t.ledger().RefundTake(kind, id)
	t.refundRun(kind)
}

func (t *Toolbox) addRenderTime(d time.Duration) float64 {
	return t.ledger().AddRenderTime(d)
}

// emit sends a status or artifact event stamped with the agent identity.
func (t *Toolbox) emit(ctx context.Context, typ, msg string, data map[string]any) {
	if t.Sink == nil {
		return
	}
	t.Sink.Emit(ctx, harness.Event{
		Type: typ, Message: msg, Data: data,
		AgentID: t.AgentID, AgentName: t.AgentName,
		Time: time.Now(),
	})
}

// resolve turns a workspace-relative path into an absolute one.
func (t *Toolbox) resolve(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(t.Workspace, p)
}

// rel converts an absolute path back to workspace-relative for events/UI.
func (t *Toolbox) rel(p string) string {
	if r, err := filepath.Rel(t.Workspace, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

func jsonOut(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// RegisterFileTools adds workspace file access plus a local shell.
func (t *Toolbox) RegisterFileTools(reg *harness.Registry) {
	reg.Register(harness.ToolDefinition{
		Name:        "bash",
		Description: "Run a shell command in the production workspace. ffmpeg and ffprobe are available. Output is combined stdout+stderr, truncated to 8000 chars.",
		InputSchema: harness.Obj(map[string]any{
			"command":         harness.Str("The shell command to run"),
			"timeout_seconds": harness.Int("Timeout in seconds (default 120, max 900)"),
		}, "command"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		command, _ := input["command"].(string)
		timeout := 120.0
		if v, ok := input["timeout_seconds"].(float64); ok && v > 0 {
			timeout = v
		}
		if timeout > 900 {
			timeout = 900
		}
		cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cctx, "/bin/sh", "-c", command)
		cmd.Dir = t.Workspace
		out, err := cmd.CombinedOutput()
		result := harness.Abbreviate(string(out), 8000)
		if err != nil {
			return "", fmt.Errorf("%v\n%s", err, result)
		}
		if result == "" {
			result = "(no output)"
		}
		return result, nil
	})

	reg.Register(harness.ToolDefinition{
		Name:        "list_files",
		Description: "List files in the production workspace (recursive), with sizes.",
		InputSchema: harness.Obj(map[string]any{
			"path": harness.Str("Subdirectory to list (default: workspace root)"),
		}),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		sub, _ := input["path"].(string)
		root := t.resolve(sub)
		var lines []string
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == "events.jsonl" {
					return nil
				}
				return nil
			}
			info, ierr := d.Info()
			size := int64(0)
			if ierr == nil {
				size = info.Size()
			}
			lines = append(lines, fmt.Sprintf("%s (%d bytes)", t.rel(p), size))
			return nil
		})
		if err != nil {
			return "", err
		}
		sort.Strings(lines)
		if len(lines) > 300 {
			lines = append(lines[:300], fmt.Sprintf("... and %d more", len(lines)-300))
		}
		if len(lines) == 0 {
			return "(empty)", nil
		}
		return strings.Join(lines, "\n"), nil
	})

	reg.Register(harness.ToolDefinition{
		Name:        "read_file",
		Description: "Read a text file from the workspace (up to 48KB).",
		InputSchema: harness.Obj(map[string]any{
			"path": harness.Str("File path, workspace-relative or absolute"),
		}, "path"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		p, _ := input["path"].(string)
		data, err := os.ReadFile(t.resolve(p))
		if err != nil {
			return "", err
		}
		if len(data) > 48*1024 {
			data = data[:48*1024]
		}
		return string(data), nil
	})

	reg.Register(harness.ToolDefinition{
		Name:        "write_file",
		Description: "Write a text file into the workspace (creates parent directories).",
		InputSchema: harness.Obj(map[string]any{
			"path":    harness.Str("File path, workspace-relative"),
			"content": harness.Str("File content"),
		}, "path", "content"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		p, _ := input["path"].(string)
		content, _ := input["content"].(string)
		abs := t.resolve(p)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf("Wrote %d bytes to %s", len(content), t.rel(abs)), nil
	})
}

// RegisterMediaTools adds probing, audio analysis, and frame extraction.
func (t *Toolbox) RegisterMediaTools(reg *harness.Registry) {
	reg.Register(harness.ToolDefinition{
		Name: "probe_media",
		Description: "Inspect media files: format, duration, streams, dimensions. Pass one path, or " +
			"paths for many at once (an editor checking every clip and line should do it in one call).",
		InputSchema: harness.Obj(map[string]any{
			"path":  harness.Str("Media file path"),
			"paths": harness.Arr("Several media file paths, probed together", harness.Str("Media file path")),
		}),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		var list []string
		if p, _ := input["path"].(string); strings.TrimSpace(p) != "" {
			list = append(list, p)
		}
		if raw, ok := input["paths"].([]any); ok {
			for _, v := range raw {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					list = append(list, s)
				}
			}
		}
		if len(list) == 0 {
			return "", fmt.Errorf("pass path or paths")
		}
		if len(list) == 1 {
			res, err := media.Probe(ctx, t.resolve(list[0]))
			if err != nil {
				return "", err
			}
			return jsonOut(res)
		}
		out := make([]map[string]any, 0, len(list))
		for _, p := range list {
			res, err := media.Probe(ctx, t.resolve(p))
			if err != nil {
				out = append(out, map[string]any{"path": p, "error": err.Error()})
				continue
			}
			out = append(out, map[string]any{"path": p, "duration_seconds": res.Duration, "probe": res})
		}
		return jsonOut(out)
	})

	reg.Register(harness.ToolDefinition{
		Name: "analyze_audio",
		Description: "Deep-analyze a music track: duration, tempo (BPM), beat grid, per-second energy " +
			"curve, and structural sections (intro/build/peak/breakdown/outro). This is the foundation " +
			"of the whole production; the result is also saved to analysis/audio_analysis.json.",
		InputSchema: harness.Obj(map[string]any{
			"path": harness.Str("Audio file path"),
		}, "path"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		p, _ := input["path"].(string)
		res, err := media.AnalyzeAudio(ctx, t.resolve(p))
		if err != nil {
			return "", err
		}
		out, err := jsonOut(res)
		if err != nil {
			return "", err
		}
		savePath := filepath.Join(t.Workspace, "analysis", "audio_analysis.json")
		os.MkdirAll(filepath.Dir(savePath), 0o755)
		os.WriteFile(savePath, []byte(out), 0o644)
		t.emit(ctx, "artifact", "audio analysis complete", map[string]any{
			"kind": "analysis", "path": "analysis/audio_analysis.json",
			"tempo": res.Tempo, "duration": res.Duration, "sections": len(res.Sections),
		})
		return out, nil
	})

	reg.Register(harness.ToolDefinition{
		Name: "transcribe_lyrics",
		Description: "Transcribe the track's vocals with line-level timing (local whisper, ~1 min per " +
			"3 min of audio). Returns timed lyric lines and saves analysis/lyrics.json. Run this on " +
			"any track that has vocals — the words and their timing drive the film. Zero segments " +
			"means the track is instrumental.",
		InputSchema: harness.Obj(map[string]any{
			"path": harness.Str("Audio file path"),
		}, "path"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		p, _ := input["path"].(string)
		t.emit(ctx, "status", "transcribing vocals", map[string]any{"state": "transcribing"})
		res, err := media.Transcribe(ctx, t.resolve(p))
		if err != nil {
			return "", err
		}
		out, err := jsonOut(res)
		if err != nil {
			return "", err
		}
		savePath := filepath.Join(t.Workspace, "analysis", "lyrics.json")
		os.MkdirAll(filepath.Dir(savePath), 0o755)
		os.WriteFile(savePath, []byte(out), 0o644)
		t.emit(ctx, "artifact", fmt.Sprintf("lyrics transcribed: %d lines", len(res.Segments)), map[string]any{
			"kind": "analysis", "path": "analysis/lyrics.json", "lines": len(res.Segments),
		})
		return out, nil
	})

	reg.Register(harness.ToolDefinition{
		Name:        "extract_frame",
		Description: "Extract a single frame from a video as a JPEG, e.g. to inspect a clip or to build a keyframe for continuity.",
		InputSchema: harness.Obj(map[string]any{
			"video_path": harness.Str("Source video path"),
			"at_seconds": harness.Num("Timestamp to extract"),
			"out_path":   harness.Str("Output JPEG path (workspace-relative)"),
		}, "video_path", "at_seconds", "out_path"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		vp, _ := input["video_path"].(string)
		at, _ := input["at_seconds"].(float64)
		op, _ := input["out_path"].(string)
		abs := t.resolve(op)
		os.MkdirAll(filepath.Dir(abs), 0o755)
		if err := media.ExtractFrame(ctx, t.resolve(vp), at, abs); err != nil {
			return "", err
		}
		return "Extracted frame to " + t.rel(abs), nil
	})
}

// RegisterVisionTools adds Grok-vision image inspection.
func (t *Toolbox) RegisterVisionTools(reg *harness.Registry) {
	reg.Register(harness.ToolDefinition{
		Name: "analyze_image",
		Description: "Look at one or more images with vision and answer a question about them. Use for " +
			"studying reference material (subjects, palette, style, mood) or checking generated frames " +
			"and clips. Pass up to 6 paths per call; images are presented in order.",
		InputSchema: harness.Obj(map[string]any{
			"paths":    harness.Arr("Image file paths (1-6)", harness.Str("image path")),
			"path":     harness.Str("Single image file path (alternative to paths)"),
			"question": harness.Str("What you want to know about the image(s)"),
		}, "question"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		q, _ := input["question"].(string)
		var paths []string
		if p, _ := input["path"].(string); p != "" {
			paths = append(paths, p)
		}
		if raw, ok := input["paths"].([]any); ok {
			for _, v := range raw {
				if s, ok := v.(string); ok && s != "" {
					paths = append(paths, s)
				}
			}
		}
		if len(paths) == 0 {
			return "", fmt.Errorf("provide path or paths")
		}
		if len(paths) > 6 {
			return "", fmt.Errorf("at most 6 images per call (got %d); batch your calls", len(paths))
		}
		parts := []xai.ContentPart{{
			Type: "text",
			Text: "Answer precisely and concretely, as a film professional describing visual material. " +
				"Images are numbered in the order attached; refer to them by number and filename.\n\nFiles: " +
				strings.Join(paths, ", ") + "\n\n" + q,
		}}
		for _, p := range paths {
			part, err := xai.ImagePart(t.resolve(p))
			if err != nil {
				return "", err
			}
			parts = append(parts, part)
		}
		resp, err := t.XAI.ChatCompletion(ctx, xai.ChatRequest{
			Model:    xai.DefaultChatModel,
			Messages: []xai.Message{{Role: "user", Content: parts}},
		})
		if err != nil {
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("no response")
		}
		if s, ok := resp.Choices[0].Message.Content.(string); ok {
			return s, nil
		}
		return "", fmt.Errorf("unexpected response shape")
	})
}

// RegisterClipReviewTool adds one-call dailies review: sample frames across a
// clip and put them in front of vision together. Replaces the hand-rolled
// extract-extract-extract-analyze pattern every crew invented per take.
func (t *Toolbox) RegisterClipReviewTool(reg *harness.Registry) {
	reg.Register(harness.ToolDefinition{
		Name: "review_clip",
		Description: "Review a rendered clip in one call: samples frames across its duration (default " +
			"first / 25% / 50% / 75% / last, or pass at_seconds), saves them under frames/qc/, and " +
			"answers your question about them with vision. Use this for every dailies check instead " +
			"of extracting frames one by one. For a lip-synced dialogue shot pass sync_audio_path: " +
			"the frames are then sampled inside the spoken window (the clip is padded past the line " +
			"and the mouth is still after it), which is the only fair test of the performance.",
		InputSchema: harness.Obj(map[string]any{
			"video_path": harness.Str("Clip to review"),
			"question": harness.Str("What to verify, concretely: continuity locks, action timing, " +
				"camera behaviour, text legibility"),
			"at_seconds":      harness.Arr("Optional explicit sample times (max 6)", harness.Num("timestamp")),
			"sync_audio_path": harness.Str("Optional: the line audio the shot was lip-synced to; samples fall within its duration"),
		}, "video_path", "question"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		vp, _ := input["video_path"].(string)
		q, _ := input["question"].(string)
		abs := t.resolve(vp)
		probe, err := media.Probe(ctx, abs)
		if err != nil {
			return "", err
		}
		var times []float64
		if raw, ok := input["at_seconds"].([]any); ok {
			for _, v := range raw {
				if f, ok := v.(float64); ok {
					times = append(times, f)
				}
			}
		}
		spoken := 0.0
		if sp, _ := input["sync_audio_path"].(string); sp != "" {
			if ap, err := media.Probe(ctx, t.resolve(sp)); err == nil && ap.Duration > 0 {
				spoken = math.Min(ap.Duration, probe.Duration)
			}
		}
		if len(times) == 0 {
			d := probe.Duration
			if spoken > 0 {
				// Sample the performance, not the padded tail.
				d = spoken
			}
			times = []float64{0.05, d * 0.25, d * 0.5, d * 0.75, d - 0.1}
		}
		if len(times) > 6 {
			return "", fmt.Errorf("at most 6 sample times (got %d)", len(times))
		}

		base := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
		var paths []string
		parts := []xai.ContentPart{}
		for i, at := range times {
			if at < 0 {
				at = 0
			}
			if at > probe.Duration-0.05 {
				at = probe.Duration - 0.05
			}
			out := filepath.Join(t.Workspace, "frames", "qc", fmt.Sprintf("%s-%02d-%04.1fs.jpg", sanitizeSlug(base), i, at))
			os.MkdirAll(filepath.Dir(out), 0o755)
			if err := media.ExtractFrame(ctx, abs, at, out); err != nil {
				return "", fmt.Errorf("extract at %.1fs: %w", at, err)
			}
			part, err := xai.ImagePart(out)
			if err != nil {
				return "", err
			}
			parts = append(parts, part)
			paths = append(paths, t.rel(out))
		}
		spokenNote := ""
		if spoken > 0 {
			spokenNote = fmt.Sprintf(" The pictured character speaks from 0.0s to %.2fs; the frames are "+
				"sampled inside that window, and the mouth is expected to be still after it.", spoken)
		}
		prompt := fmt.Sprintf("These are %d frames sampled from one video clip (%s, %.2fs), in "+
			"chronological order at the timestamps given by their filenames.%s Answer as a film "+
			"professional reviewing dailies: precise, concrete, and honest about faults.\n\nFrames: %s\n\n%s",
			len(paths), t.rel(abs), probe.Duration, spokenNote, strings.Join(paths, ", "), q)
		parts = append([]xai.ContentPart{{Type: "text", Text: prompt}}, parts...)

		resp, err := t.XAI.ChatCompletion(ctx, xai.ChatRequest{
			Model:    xai.DefaultChatModel,
			Messages: []xai.Message{{Role: "user", Content: parts}},
		})
		if err != nil {
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("no response")
		}
		answer, _ := resp.Choices[0].Message.Content.(string)
		return jsonOut(map[string]any{"frames": paths, "review": answer})
	})
}

// RegisterImageGenTools adds keyframe/style-frame generation.
func (t *Toolbox) RegisterImageGenTools(reg *harness.Registry) {
	reg.Register(harness.ToolDefinition{
		Name: "generate_image",
		Description: "Generate a still image (keyframe, style frame, establishing look) with Grok Imagine. " +
			"Saved under frames/. Returns the saved path; use analyze_image to inspect the result.",
		InputSchema: harness.Obj(map[string]any{
			"id":           harness.Str("Short slug used as the filename, e.g. 'shot03-key'"),
			"prompt":       harness.Str("The image prompt: subject, style, lighting, composition"),
			"aspect_ratio": harness.StrEnum("Aspect ratio (default 16:9)", "16:9", "9:16", "1:1", "4:3", "3:4", "3:2", "2:3"),
		}, "id", "prompt"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		id, _ := input["id"].(string)
		prompt, _ := input["prompt"].(string)
		ar, _ := input["aspect_ratio"].(string)
		take, run := t.countTake("image", id)
		if take > imageTakeLimit {
			return "", fmt.Errorf("take %d of %q refused: the %d-take budget for this still is spent. "+
				"Text-to-image cannot match an existing image by description — every take re-rolls the "+
				"composition, so more takes will not converge. Pick the best take already in frames/ and "+
				"move on. If the frame must match an existing image exactly, get it FROM that image: "+
				"animate it with generate_video_clips mode 'image' and extract_frame the moment you need",
				take, id, imageTakeLimit)
		}
		if run > t.runBudgetImages() {
			return "", fmt.Errorf("still generation refused: this run's budget of %d stills is spent. "+
				"Work with the frames you have, or extract frames from rendered footage", t.runBudgetImages())
		}
		resp, err := t.XAI.GenerateImage(ctx, xai.ImageRequest{
			Prompt:         prompt,
			AspectRatio:    ar,
			ResponseFormat: "b64_json",
		})
		if err != nil {
			return "", err
		}
		if len(resp.Data) == 0 {
			return "", fmt.Errorf("no image returned")
		}
		out := filepath.Join(t.Workspace, "frames", sanitizeSlugPath(id)+".png")
		os.MkdirAll(filepath.Dir(out), 0o755)
		if err := t.XAI.SaveImage(ctx, resp.Data[0], out); err != nil {
			return "", err
		}
		relPath := t.rel(out)
		t.emit(ctx, "artifact", "generated frame "+id, map[string]any{
			"kind": "frame", "id": id, "path": relPath, "prompt": harness.Abbreviate(prompt, 300),
		})
		out2 := map[string]any{
			"path":           relPath,
			"revised_prompt": resp.Data[0].RevisedPrompt,
			"take":           take,
		}
		if take == imageTakeLimit {
			out2["warning"] = fmt.Sprintf("this was the last budgeted take for %q: further takes will be "+
				"refused. If it still misses, use the best take so far, or derive the frame from footage "+
				"(animate + extract_frame) instead of regenerating", id)
		}
		return jsonOut(out2)
	})
}

// shotInput is one requested clip in generate_video_clips.
type shotInput struct {
	ID             string   `json:"id"`
	Prompt         string   `json:"prompt"`
	Duration       int      `json:"duration_seconds"`
	Mode           string   `json:"mode"` // text | image | reference
	ImagePath      string   `json:"image_path,omitempty"`
	EndImagePath   string   `json:"end_image_path,omitempty"`
	ReferencePaths []string `json:"reference_paths,omitempty"`
	SyncAudioPath  string   `json:"sync_audio_path,omitempty"`
	SyncFrom       float64  `json:"sync_audio_from_seconds,omitempty"`
	AspectRatio    string   `json:"aspect_ratio,omitempty"`
	Resolution     string   `json:"resolution,omitempty"`
	Seed           int64    `json:"seed,omitempty"`
}

type shotResult struct {
	ID       string  `json:"id"`
	Path     string  `json:"path,omitempty"`
	Thumb    string  `json:"thumbnail,omitempty"`
	Duration float64 `json:"actual_duration_seconds,omitempty"`
	Render   string  `json:"render,omitempty"`
	Take     int     `json:"take,omitempty"`
	// GPUMinutes is the run's cumulative render wall-clock, so the crew
	// always knows what the shoot has cost so far.
	GPUMinutes float64 `json:"gpu_minutes_this_run,omitempty"`
	Error      string  `json:"error,omitempty"`
}

// RegisterVideoGenTools adds the core video generation tool, shaped by
// whichever engine is wired in: hosted Grok Imagine or MiniMax H3 on the
// local GPUs.
func (t *Toolbox) RegisterVideoGenTools(reg *harness.Registry) {
	caps := t.Video.Caps()

	modes := []string{"text", "image"}
	if caps.SupportsReference {
		modes = append(modes, "reference")
	}
	props := map[string]any{
		"id":               harness.Str("Short slug for the clip filename, e.g. 'shot01'"),
		"prompt":           harness.Str("The motion prompt: subject, action, camera movement, style, lighting"),
		"duration_seconds": harness.Int(fmt.Sprintf("Clip duration %d-%d seconds", caps.MinSeconds, caps.MaxSeconds)),
		"mode":             harness.StrEnum("Generation mode", modes...),
		"image_path":       harness.Str("For mode 'image': the still to animate (its first frame)"),
		"aspect_ratio":     harness.StrEnum("Aspect ratio (default 16:9)", caps.AspectRatios...),
		"resolution":       harness.StrEnum("Resolution (default 720p)", caps.Resolutions...),
	}
	if caps.SupportsReference {
		props["reference_paths"] = harness.Arr("For mode 'reference': guiding images", harness.Str("image path"))
	}
	if caps.SupportsEndFrame {
		props["end_image_path"] = harness.Str("Optional: a still to land on as the clip's final frame, " +
			"interpolating from image_path to it. Use it to chain shots seamlessly.")
	}
	if caps.SupportsSyncAudio {
		props["sync_audio_path"] = harness.Str("Lip-sync (mode 'reference' only): audio file whose " +
			"vocals the performance must sync to — usually the music track itself")
		props["sync_audio_from_seconds"] = harness.Num("Where in that audio the clip's segment starts; " +
			"the slice length is the clip's duration. The editor MUST place this clip at exactly this " +
			"timeline position so the lips match the master track.")
	}
	if caps.SupportsSeed {
		props["seed"] = harness.Int("Optional noise seed; reuse it to re-render a shot with a tweaked prompt, " +
			"omit it for a fresh take")
	}

	reg.Register(harness.ToolDefinition{
		Name: "generate_video_clips",
		Description: fmt.Sprintf("Generate one or more video clips with %s. %s "+
			"Saved under clips/<id>.mp4 with a thumbnail. Returns per-clip results; failed clips "+
			"report errors and can be retried individually.", caps.Name, caps.Note),
		InputSchema: harness.Obj(map[string]any{
			"shots": harness.Arr("Clips to generate", harness.Obj(props,
				"id", "prompt", "duration_seconds", "mode")),
		}, "shots"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		raw, err := json.Marshal(input["shots"])
		if err != nil {
			return "", err
		}
		var shots []shotInput
		if err := json.Unmarshal(raw, &shots); err != nil {
			return "", fmt.Errorf("invalid shots array: %w", err)
		}
		if len(shots) == 0 {
			return "", fmt.Errorf("shots must be non-empty")
		}
		limit := caps.MaxConcurrent
		if limit < 1 {
			limit = 1
		}
		// A call may carry as many shots as the engine renders at once, so a
		// hosted backend with a deep queue is used at full width.
		maxPerCall := 12
		if limit > maxPerCall {
			maxPerCall = limit
		}
		if len(shots) > maxPerCall {
			return "", fmt.Errorf("at most %d shots per call; split into batches", maxPerCall)
		}
		results := make([]shotResult, len(shots))
		var wg sync.WaitGroup
		sem := make(chan struct{}, limit)
		for i := range shots {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				results[i] = t.generateOneClip(ctx, shots[i])
			}(i)
		}
		wg.Wait()
		return jsonOut(results)
	})
}

func (t *Toolbox) generateOneClip(ctx context.Context, shot shotInput) shotResult {
	res := shotResult{ID: shot.ID}
	fail := func(err error) shotResult {
		res.Error = err.Error()
		t.emit(ctx, "status", fmt.Sprintf("clip %s failed: %v", shot.ID, err), map[string]any{
			"shot_id": shot.ID, "state": "failed",
		})
		return res
	}
	take, run := t.countTake("clip", shot.ID)
	fail = func(err error) shotResult {
		if isInfraError(err) {
			t.refundTake("clip", shot.ID)
			err = fmt.Errorf("%w\n(render infrastructure failure: this did NOT count against the "+
				"shot's take budget. Do not start, stop, or kill server processes yourself — "+
				"report the outage in your report if it persists)", err)
		}
		res.Error = err.Error()
		t.emit(ctx, "status", fmt.Sprintf("clip %s failed: %v", shot.ID, err), map[string]any{
			"shot_id": shot.ID, "state": "failed",
		})
		return res
	}
	if take > clipTakeLimit {
		return fail(fmt.Errorf("take %d of shot %q refused: the %d-take budget is spent. Use the best "+
			"take rendered so far; if the shot is truly unusable, change the approach (different first "+
			"frame, simpler motion) under a NEW shot id and say so in your report", take, shot.ID, clipTakeLimit))
	}
	if run > t.runBudgetClips() {
		return fail(fmt.Errorf("render refused: this run's budget of %d clips is spent. Cut the film "+
			"from the footage on disk", t.runBudgetClips()))
	}

	outPath := filepath.Join(t.Workspace, "clips", sanitizeSlug(shot.ID)+".mp4")
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fail(err)
	}

	req := ClipRequest{
		ID:          shot.ID,
		Prompt:      shot.Prompt,
		Seconds:     shot.Duration,
		Mode:        shot.Mode,
		AspectRatio: shot.AspectRatio,
		Resolution:  shot.Resolution,
		Seed:        shot.Seed,
		DestPath:    outPath,
	}
	if shot.ImagePath != "" {
		req.ImagePath = t.resolve(shot.ImagePath)
	}
	if shot.EndImagePath != "" {
		req.EndImagePath = t.resolve(shot.EndImagePath)
	}
	for _, rp := range shot.ReferencePaths {
		req.ReferencePaths = append(req.ReferencePaths, t.resolve(rp))
	}
	if shot.SyncAudioPath != "" {
		// Slice exactly the clip's length so audio and video line up. The
		// local H3 snaps durations to its frame grid; the hosted camera takes
		// plain whole seconds.
		clipSecs := float64(shot.Duration)
		if _, local := t.Video.(*H3Backend); local {
			clipSecs = comfy.SecondsForFrames(comfy.FramesForSeconds(float64(shot.Duration)))
		}
		slicePath := filepath.Join(t.Workspace, "state", "sync_"+sanitizeSlug(shot.ID)+".wav")
		os.MkdirAll(filepath.Dir(slicePath), 0o755)
		if err := media.SliceAudio(ctx, t.resolve(shot.SyncAudioPath), shot.SyncFrom, clipSecs, slicePath); err != nil {
			return fail(err)
		}
		req.SyncAudioPath = slicePath
	}

	t.emit(ctx, "status", "submitting clip "+shot.ID, map[string]any{
		"shot_id": shot.ID, "state": "submitted", "engine": t.Video.Caps().Name,
		"prompt": harness.Abbreviate(shot.Prompt, 300),
	})

	renderStart := time.Now()
	lastEmit := time.Time{}
	lastState := ""
	out, err := t.Video.Generate(ctx, req, func(elapsed time.Duration, state string) {
		// Repeated ticks of the same state are rate-limited; a change of
		// state (submitted with its task id, a retry, a queue message) is
		// always recorded.
		if state == lastState && time.Since(lastEmit) < 14*time.Second {
			return
		}
		lastState = state
		lastEmit = time.Now()
		t.emit(ctx, "status", fmt.Sprintf("clip %s %s (%ds)", shot.ID, state, int(elapsed.Seconds())),
			map[string]any{
				"shot_id": shot.ID, "state": "rendering",
				"detail": state, "elapsed_seconds": int(elapsed.Seconds()),
			})
	})
	if err != nil {
		return fail(err)
	}

	total := t.addRenderTime(time.Since(renderStart))
	res.Path = t.rel(outPath)
	res.Duration = out.Duration
	res.Render = out.Note
	if shot.SyncAudioPath != "" {
		res.Render += fmt.Sprintf("; LIP-SYNCED to %s at %.2fs — the editor must place this clip at "+
			"timeline %.2fs exactly (in_seconds 0) or the sync is destroyed", shot.SyncAudioPath,
			shot.SyncFrom, shot.SyncFrom)
	}
	res.Take = take
	res.GPUMinutes = math.Round(total/6) / 10
	t.ledger().RecordRender(RenderRecord{
		Time: time.Now(), ShotID: shot.ID, Path: res.Path,
		Take: take, Duration: out.Duration, Render: out.Note,
	})

	thumbPath := strings.TrimSuffix(outPath, ".mp4") + ".jpg"
	if err := media.ExtractFrame(ctx, outPath, 0.5, thumbPath); err == nil {
		res.Thumb = t.rel(thumbPath)
	}
	t.emit(ctx, "artifact", "clip ready: "+shot.ID, map[string]any{
		"kind": "clip", "shot_id": shot.ID, "path": res.Path, "thumbnail": res.Thumb,
		"duration": res.Duration, "render": res.Render,
		"prompt": harness.Abbreviate(shot.Prompt, 300),
	})
	return res
}

// RegisterEditTools adds timeline stitching and audio mastering.
func (t *Toolbox) RegisterEditTools(reg *harness.Registry) {
	reg.Register(harness.ToolDefinition{
		Name: "stitch_timeline",
		Description: "Assemble the final cut: trim and concatenate clips in order, scaled to a uniform " +
			"frame, with the music track as the only audio. Cut points should land on beats; use the " +
			"audio analysis. Writes final/<out_name>. Hard cuts by default; optional crossfades.",
		InputSchema: harness.Obj(map[string]any{
			"clips": harness.Arr("Ordered edit decision list", harness.Obj(map[string]any{
				"path":             harness.Str("Clip file path"),
				"in_seconds":       harness.Num("Trim start within the clip (default 0)"),
				"duration_seconds": harness.Num("How long this clip runs on the timeline"),
				"transition_seconds": harness.Num("Dissolve INTO this clip from the previous one over " +
					"this many seconds (default 0 = hard cut). Use ~0.5-1.0s for time-jumps and " +
					"mood shifts; never on an invisible chained join."),
			}, "path", "duration_seconds")),
			"music_path":             harness.Str("The music track"),
			"music_offset_seconds":   harness.Num("Start the music this far in (default 0)"),
			"crossfade_seconds":      harness.Num("Crossfade between clips (default 0 = hard cuts)"),
			"audio_fade_out_seconds": harness.Num("Fade the music out at the end (default 2)"),
			"width":                  harness.Int("Frame width (default 1280)"),
			"height":                 harness.Int("Frame height (default 720)"),
			"out_name":               harness.Str("Output filename under final/ (default cut.mp4)"),
		}, "clips", "music_path"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		raw, err := json.Marshal(input["clips"])
		if err != nil {
			return "", err
		}
		var clips []struct {
			Path       string  `json:"path"`
			In         float64 `json:"in_seconds"`
			Duration   float64 `json:"duration_seconds"`
			Transition float64 `json:"transition_seconds"`
		}
		if err := json.Unmarshal(raw, &clips); err != nil {
			return "", fmt.Errorf("invalid clips array: %w", err)
		}
		spec := media.StitchSpec{
			MusicPath:    t.resolve(str(input, "music_path")),
			MusicOffset:  num(input, "music_offset_seconds"),
			CrossfadeSec: num(input, "crossfade_seconds"),
			AudioFadeOut: num(input, "audio_fade_out_seconds"),
			Width:        int(num(input, "width")),
			Height:       int(num(input, "height")),
		}
		if spec.AudioFadeOut == 0 {
			spec.AudioFadeOut = 2
		}
		for _, c := range clips {
			spec.Clips = append(spec.Clips, media.TimelineClip{
				Path: t.resolve(c.Path), In: c.In, Duration: c.Duration,
				TransitionSec: c.Transition,
			})
		}
		outName := str(input, "out_name")
		if outName == "" {
			outName = "cut.mp4"
		}
		spec.OutPath = filepath.Join(t.Workspace, "final", filepath.Base(outName))
		os.MkdirAll(filepath.Dir(spec.OutPath), 0o755)

		t.emit(ctx, "status", "stitching timeline", map[string]any{"state": "stitching", "clips": len(spec.Clips)})
		if err := media.Stitch(ctx, spec); err != nil {
			return "", err
		}
		probe, _ := media.Probe(ctx, spec.OutPath)
		relPath := t.rel(spec.OutPath)
		data := map[string]any{"kind": "cut", "path": relPath}
		if probe != nil {
			data["duration"] = probe.Duration
		}
		t.emit(ctx, "artifact", "timeline stitched: "+relPath, data)
		return jsonOut(map[string]any{"path": relPath, "probe": probe})
	})

	reg.Register(harness.ToolDefinition{
		Name: "master_audio",
		Description: "Master the audio of a finished video: two-pass EBU R128 loudness normalization " +
			"(default -14 LUFS, streaming standard) with the video stream copied untouched. " +
			"Writes final/<out_name>.",
		InputSchema: harness.Obj(map[string]any{
			"in_path":     harness.Str("The stitched video to master"),
			"out_name":    harness.Str("Output filename under final/ (default master.mp4)"),
			"target_lufs": harness.Num("Target integrated loudness (default -14)"),
		}, "in_path"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		in := t.resolve(str(input, "in_path"))
		outName := str(input, "out_name")
		if outName == "" {
			outName = "master.mp4"
		}
		out := filepath.Join(t.Workspace, "final", filepath.Base(outName))
		t.emit(ctx, "status", "mastering audio", map[string]any{"state": "mastering"})
		if err := media.MasterAudio(ctx, in, out, num(input, "target_lufs")); err != nil {
			return "", err
		}
		relPath := t.rel(out)
		t.emit(ctx, "artifact", "audio mastered: "+relPath, map[string]any{"kind": "master", "path": relPath})
		return "Mastered to " + relPath, nil
	})
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func num(m map[string]any, k string) float64 {
	f, _ := m[k].(float64)
	return f
}

// sanitizeSlugPath is sanitizeSlug for ids that may carry a subdirectory,
// e.g. the film bible's "bible/the_heavens". Each segment is sanitized on
// its own so the slash survives; empty and dot segments are dropped so the
// result can never escape frames/.
func sanitizeSlugPath(s string) string {
	var parts []string
	for _, seg := range strings.Split(s, "/") {
		seg = strings.Trim(seg, ".")
		if seg == "" {
			continue
		}
		parts = append(parts, sanitizeSlug(seg))
	}
	if len(parts) == 0 {
		return "clip"
	}
	return filepath.Join(parts...)
}

func sanitizeSlug(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, s)
	if s == "" {
		s = "clip"
	}
	return s
}
