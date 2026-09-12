package agents

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"time"

	"auteur/internal/media"
	"auteur/pkg/comfy"
	"auteur/pkg/minimax"
	"auteur/pkg/xai"
)

// minimaxMinAudioSeconds is the shortest reference audio MiniMax accepts.
const minimaxMinAudioSeconds = 2.0

// ClipRequest is one clip to render, independent of which engine renders it.
// Image paths are absolute by the time a backend sees them.
type ClipRequest struct {
	ID             string
	Prompt         string
	Seconds        int
	Mode           string // text | image | reference
	ImagePath      string
	EndImagePath   string
	ReferencePaths []string
	// SyncAudioPath is a pre-sliced audio file exactly the clip's length;
	// the render lip-syncs to it (reference mode only).
	SyncAudioPath string
	AspectRatio   string
	Resolution    string
	Seed          int64 // 0 picks one at random
	DestPath      string
}

// ClipOutput is what a backend produced.
type ClipOutput struct {
	Duration float64
	// Note describes how the clip was actually rendered (resolution, frame
	// count, any substitution the backend had to make). It is surfaced to the
	// agent so it can adjust the next batch.
	Note string
}

// BackendCaps tells the toolbox what the active engine can do, so the
// generate_video_clips schema and description describe reality.
type BackendCaps struct {
	Name              string
	MinSeconds        int
	MaxSeconds        int
	Resolutions       []string
	AspectRatios      []string
	SupportsReference bool
	SupportsEndFrame  bool
	SupportsSeed      bool
	SupportsSyncAudio bool
	// MaxConcurrent bounds in-flight renders.
	MaxConcurrent int
	// Note is appended to the tool description: the engine's character and
	// its rules, in the cinematographer's terms.
	Note string
}

// VideoBackend renders clips. Implementations write the finished mp4 to
// req.DestPath.
type VideoBackend interface {
	Caps() BackendCaps
	// Generate renders one clip. onProgress (optional) is called periodically
	// with elapsed time and a short human-readable state.
	Generate(ctx context.Context, req ClipRequest, onProgress func(elapsed time.Duration, state string)) (ClipOutput, error)
}

// --- Grok Imagine (xAI, hosted) ---

type GrokBackend struct{ Client *xai.Client }

func (b *GrokBackend) Caps() BackendCaps {
	return BackendCaps{
		Name:              "Grok Imagine",
		MinSeconds:        1,
		MaxSeconds:        15,
		Resolutions:       []string{"480p", "720p", "1080p"},
		AspectRatios:      []string{"16:9", "9:16", "1:1", "4:3", "3:4", "3:2", "2:3"},
		SupportsReference: true,
		MaxConcurrent:     6,
		Note: "Modes: 'text' (prompt only), 'image' (animate a still via image_path; strongest " +
			"continuity control), 'reference' (reference_paths images guide subject/style; max 720p). " +
			"Duration 1-15 seconds. Clips render concurrently, each taking a few minutes.",
	}
}

func (b *GrokBackend) Generate(ctx context.Context, req ClipRequest, onProgress func(time.Duration, string)) (ClipOutput, error) {
	vreq := xai.VideoRequest{
		Prompt:      req.Prompt,
		Duration:    req.Seconds,
		AspectRatio: req.AspectRatio,
		Resolution:  req.Resolution,
	}
	if vreq.Resolution == "" {
		vreq.Resolution = "720p"
	}
	switch req.Mode {
	case "image":
		if req.ImagePath == "" {
			return ClipOutput{}, fmt.Errorf("mode 'image' requires image_path")
		}
		img, err := xai.VideoImageFromFile(req.ImagePath)
		if err != nil {
			return ClipOutput{}, err
		}
		vreq.Image = &img
	case "reference":
		if len(req.ReferencePaths) == 0 {
			return ClipOutput{}, fmt.Errorf("mode 'reference' requires reference_paths")
		}
		for _, rp := range req.ReferencePaths {
			img, err := xai.VideoImageFromFile(rp)
			if err != nil {
				return ClipOutput{}, err
			}
			vreq.ReferenceImages = append(vreq.ReferenceImages, img)
		}
		if vreq.Resolution == "1080p" {
			vreq.Resolution = "720p"
		}
	}

	requestID, err := b.Client.SubmitVideo(ctx, vreq)
	if err != nil {
		return ClipOutput{}, err
	}
	st, err := b.Client.WaitVideo(ctx, requestID, 5*time.Second, func(elapsed time.Duration, st *xai.VideoStatus) {
		if onProgress != nil && st.Status == "pending" {
			onProgress(elapsed, "rendering")
		}
	})
	if err != nil {
		return ClipOutput{}, err
	}
	if st.Video == nil || st.Video.URL == "" {
		return ClipOutput{}, fmt.Errorf("done but no video URL")
	}
	if err := b.Client.DownloadFile(ctx, st.Video.URL, req.DestPath); err != nil {
		return ClipOutput{}, fmt.Errorf("download: %w", err)
	}
	return ClipOutput{Duration: st.Video.Duration, Note: vreq.Resolution + " " + vreq.AspectRatio}, nil
}

// --- MiniMax H3 on the local ComfyUI (this box, dual RTX 3090) ---

type H3Backend struct {
	// Pool schedules clips across one or more ComfyUI render nodes; a clip
	// leases a node for its whole upload/queue/download lifecycle.
	Pool *comfy.Pool
	// Production namespaces uploaded stills inside ComfyUI's input tree.
	Production string
	// Steps and Resolution override the defaults when set.
	Steps int
}

func (b *H3Backend) Caps() BackendCaps {
	return BackendCaps{
		Name:              "MiniMax H3 (local)",
		MinSeconds:        comfy.H3MinSeconds,
		MaxSeconds:        comfy.H3MaxSeconds,
		Resolutions:       []string{"480p", "720p"},
		AspectRatios:      []string{"16:9", "9:16", "1:1", "4:3", "3:4", "3:2", "2:3"},
		SupportsReference: true,
		SupportsEndFrame:  true,
		SupportsSeed:      true,
		SupportsSyncAudio: true,
		// One clip in flight per render node; the pool blocks extras.
		MaxConcurrent: b.poolSize(),
		Note: "Modes: 'text' (prompt only), 'image' (animate a still via image_path), 'reference' " +
			"(inject specific content). H3 is a first/last-frame model: image_path alone animates " +
			"forward from that frame; adding end_image_path interpolates between the two stills. " +
			"Continuity comes from frames, not descriptions: to continue from existing imagery, " +
			"animate that image and extract_frame the moment you need for the next shot's " +
			"image_path. Mode 'reference' (up to 9 reference_paths, REF2VA) puts SPECIFIC CONTENT " +
			"into a scene — a screenshot as a screen's contents, a logo, a product, a face: address " +
			"each reference in the prompt as <Picture 1>, <Picture 2> in list order ('the monitor " +
			"displays <Picture 1>'). Reference renders run ~2.5x slower (20 steps, no turbo LoRA) " +
			"and cannot take image_path; use them for content injection, never for shot-to-shot " +
			"continuity, which chaining owns. Duration 2-15 seconds, snapped to the model's frame " +
			"grid (3s=73, 5s=124, 10s=243, 15s=362 frames at 24fps). Resolution 480p (864x480) is " +
			"the studio default; 720p (1344x768, ~3x slower) only when the brief explicitly asks. " +
			"Renders run sequentially on local GPUs (~40s of render per second of 480p video; " +
			"reference and 720p cost more) and take budgets are enforced (3 takes per shot), so " +
			"plan the shot list before rolling. H3 also generates its own audio, which the edit " +
			"discards in favour of the music track." + b.poolNote(),
	}
}

// poolNote extends the tool description when several render nodes are pooled.
func (b *H3Backend) poolNote() string {
	n := b.poolSize()
	if n <= 1 {
		return ""
	}
	return fmt.Sprintf(" %d render nodes are POOLED: up to %d clips render in parallel, so "+
		"submitting a batch genuinely overlaps. Each node keeps its own warm model — batching "+
		"same-mode shots still avoids checkpoint swaps per node.", n, n)
}

func (b *H3Backend) poolSize() int {
	if b.Pool == nil {
		return 1
	}
	return b.Pool.Size()
}

func (b *H3Backend) Generate(ctx context.Context, req ClipRequest, onProgress func(time.Duration, string)) (ClipOutput, error) {
	var notes []string

	wantModel := comfy.H3DiT
	if req.Mode == "reference" {
		wantModel = comfy.H3RefDiT
	}
	if onProgress != nil {
		onProgress(0, "waiting for a render node")
	}
	client, nodeName, release, err := b.Pool.Acquire(ctx, wantModel)
	if err != nil {
		return ClipOutput{}, fmt.Errorf("acquire render node: %w", err)
	}
	loaded := ""
	defer func() { release(loaded) }()
	if b.Pool.Size() > 1 {
		notes = append(notes, "node "+nodeName)
	}

	seconds := float64(req.Seconds)
	if seconds < comfy.H3MinSeconds {
		seconds = comfy.H3MinSeconds
		notes = append(notes, fmt.Sprintf("duration raised to %ds (model minimum)", comfy.H3MinSeconds))
	}
	if seconds > comfy.H3MaxSeconds {
		seconds = comfy.H3MaxSeconds
		notes = append(notes, fmt.Sprintf("duration capped at %ds (model maximum)", comfy.H3MaxSeconds))
	}

	resolution := req.Resolution
	if resolution == "" {
		resolution = "480p"
	}
	if resolution == "1080p" {
		resolution = "720p"
		notes = append(notes, "1080p unavailable locally, rendered at 720p")
	}
	width, height := comfy.H3Dimensions(req.AspectRatio, resolution)

	opts := comfy.H3Options{
		Prompt:         req.Prompt,
		Width:          width,
		Height:         height,
		Length:         comfy.FramesForSeconds(seconds),
		Steps:          b.Steps,
		Seed:           req.Seed,
		FilenamePrefix: "video/" + sanitizeSlug(b.Production) + "_" + sanitizeSlug(req.ID),
	}
	if opts.Seed == 0 {
		opts.Seed = rand.Int63n(1 << 62)
	}

	// H3 takes stills as first/last frames, so they must live in ComfyUI's
	// input tree before the graph can reference them.
	first, last := req.ImagePath, req.EndImagePath
	if req.Mode == "reference" {
		if len(req.ReferencePaths) == 0 && req.SyncAudioPath == "" {
			return ClipOutput{}, fmt.Errorf("mode 'reference' requires reference_paths and/or sync_audio_path")
		}
		if len(req.ReferencePaths) > 9 {
			return ClipOutput{}, fmt.Errorf("at most 9 reference images (got %d)", len(req.ReferencePaths))
		}
		first, last = "", ""
	}
	if req.Mode == "image" && first == "" {
		return ClipOutput{}, fmt.Errorf("mode 'image' requires image_path")
	}
	upload := func(path string) (*comfy.FileRef, error) {
		delay := 10 * time.Second
		for attempt := 1; ; attempt++ {
			ref, err := client.UploadImage(ctx, path, "auteur/"+sanitizeSlug(b.Production))
			if err == nil {
				return &ref, nil
			}
			if !isInfraError(err) || attempt >= 8 {
				return nil, fmt.Errorf("upload %s: %w", filepath.Base(path), err)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			if delay < 40*time.Second {
				delay *= 2
			}
		}
	}
	if first != "" {
		ref, err := upload(first)
		if err != nil {
			return ClipOutput{}, err
		}
		opts.FirstFrame = ref
	}
	if last != "" {
		ref, err := upload(last)
		if err != nil {
			return ClipOutput{}, err
		}
		opts.LastFrame = ref
	}
	if req.SyncAudioPath != "" && req.Mode != "reference" {
		return ClipOutput{}, fmt.Errorf("sync_audio requires mode 'reference' (the REF2VA variant is " +
			"what conditions on audio)")
	}
	if req.Mode == "reference" {
		// Reference renders run the REF2VA DiT at its own step count; the
		// FL2VA turbo step override does not apply.
		opts.Steps = 0
		for _, rp := range req.ReferencePaths {
			ref, err := upload(rp)
			if err != nil {
				return ClipOutput{}, err
			}
			opts.RefImages = append(opts.RefImages, *ref)
		}
		if req.SyncAudioPath != "" {
			ref, err := upload(req.SyncAudioPath)
			if err != nil {
				return ClipOutput{}, err
			}
			opts.RefAudios = append(opts.RefAudios, *ref)
			notes = append(notes, "lip-synced to supplied audio")
		}
		notes = append(notes, fmt.Sprintf("reference render: %d ref(s), %d steps, REF2VA",
			len(opts.RefImages), comfy.H3RefSteps))
	}

	opts = opts.WithDefaults()
	promptID, err := queueWithRetry(ctx, client, comfy.BuildH3(opts), onProgress)
	if err != nil {
		return ClipOutput{}, err
	}
	loaded = opts.DiT

	res, err := client.Wait(ctx, promptID, 5*time.Second, func(p comfy.Progress) {
		if onProgress == nil {
			return
		}
		state := "rendering"
		if !p.Running && p.Position > 0 {
			state = fmt.Sprintf("queued (%d ahead)", p.Position-1)
		}
		onProgress(p.Elapsed, state)
	})
	if err != nil {
		return ClipOutput{}, err
	}
	video := pickVideo(res.Files)
	if video == nil {
		return ClipOutput{}, fmt.Errorf("render succeeded but produced no video file")
	}
	if err := client.Download(ctx, *video, req.DestPath); err != nil {
		return ClipOutput{}, fmt.Errorf("download: %w", err)
	}

	note := opts.Describe() + fmt.Sprintf(", seed %d", opts.Seed)
	if len(notes) > 0 {
		note += "; " + strings.Join(notes, "; ")
	}
	return ClipOutput{Duration: comfy.SecondsForFrames(opts.Length), Note: note}, nil
}

// pickVideo finds the rendered clip among a prompt's outputs. SaveVideo
// reports its mp4 in the "images" list, so match on extension rather than key.
func pickVideo(files []comfy.FileRef) *comfy.FileRef {
	for _, f := range files {
		switch strings.ToLower(filepath.Ext(f.Filename)) {
		case ".mp4", ".webm", ".mkv", ".mov":
			return &f
		}
	}
	return nil
}

// --- MiniMax H3 on the hosted minimax.io API (off-box rendering) ---

type MiniMaxBackend struct {
	Client     *minimax.Client
	Production string
	// Model overrides the default "MiniMax-H3" (e.g. "MiniMax-H3-Max").
	Model string
}

func (b *MiniMaxBackend) Caps() BackendCaps {
	return BackendCaps{
		Name:              "MiniMax H3 (hosted)",
		MinSeconds:        4,
		MaxSeconds:        15,
		Resolutions:       []string{"768P", "2K"},
		AspectRatios:      []string{"16:9", "9:16", "1:1", "4:3", "3:4", "adaptive"},
		SupportsReference: true,
		SupportsEndFrame:  true,
		SupportsSyncAudio: true,
		// MiniMax allows 30 in-flight tasks per account (300 RPM); run near
		// that ceiling with a little headroom so a batch of shots overlaps.
		MaxConcurrent: 25,
		Note: "MiniMax-H3 on the hosted minimax.io cloud (renders off this box's GPUs). Modes: " +
			"'text' (prompt only; set aspect via ratio), 'image' (animate a still via image_path as " +
			"the FIRST frame; add end_image_path to interpolate first->last), 'reference' (up to 9 " +
			"reference_paths inject specific subjects/style into the shot). Continuity comes from " +
			"frames: to continue a shot, extract_frame the moment you need and pass it as the next " +
			"clip's image_path. Duration 4-15 seconds; resolution 768P (studio default) or 2K. H3 " +
			"generates native stereo audio, which the edit discards in favour of the music track. " +
			"Clips render concurrently in the cloud, each taking a few minutes.",
	}
}

func (b *MiniMaxBackend) Generate(ctx context.Context, req ClipRequest, onProgress func(time.Duration, string)) (ClipOutput, error) {
	var notes []string

	model := b.Model
	if model == "" {
		model = "MiniMax-H3"
	}

	seconds := req.Seconds
	if seconds < 4 {
		seconds = 4
		notes = append(notes, "duration raised to 4s (model minimum)")
	}
	if seconds > 15 {
		seconds = 15
		notes = append(notes, "duration capped at 15s (model maximum)")
	}

	// H3's hosted API offers 768P and 2K; map auteur's ladder onto them.
	resolution := "768P"
	r := strings.ToLower(req.Resolution)
	if r == "2k" || strings.Contains(r, "1080") {
		resolution = "2K"
		if strings.Contains(r, "1080") {
			notes = append(notes, "1080p rendered at 2K")
		}
	}

	content := []minimax.Content{{Type: "text", Text: req.Prompt}}
	addImage := func(path, role string) error {
		uri, err := minimax.DataURIFromFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", filepath.Base(path), err)
		}
		content = append(content, minimax.Content{Type: "image_url", Role: role, ImageURL: &minimax.URLRef{URL: uri}})
		return nil
	}

	ratio := req.AspectRatio
	if ratio == "" {
		ratio = "16:9"
	}
	switch req.Mode {
	case "image":
		if req.ImagePath == "" {
			return ClipOutput{}, fmt.Errorf("mode 'image' requires image_path")
		}
		if err := addImage(req.ImagePath, "first_frame"); err != nil {
			return ClipOutput{}, err
		}
		if req.EndImagePath != "" {
			if err := addImage(req.EndImagePath, "last_frame"); err != nil {
				return ClipOutput{}, err
			}
			notes = append(notes, "first->last interpolation")
		}
		// image-to-video derives its frame from the still.
		ratio = "adaptive"
	case "reference":
		if len(req.ReferencePaths) == 0 && req.SyncAudioPath == "" {
			return ClipOutput{}, fmt.Errorf("mode 'reference' requires reference_paths and/or sync_audio_path")
		}
		if len(req.ReferencePaths) > 9 {
			return ClipOutput{}, fmt.Errorf("at most 9 reference images (got %d)", len(req.ReferencePaths))
		}
		for _, rp := range req.ReferencePaths {
			if err := addImage(rp, "reference_image"); err != nil {
				return ClipOutput{}, err
			}
		}
		if req.SyncAudioPath != "" {
			audioPath := req.SyncAudioPath
			// MiniMax accepts reference audio of 2-15s only; pad short lines
			// with trailing silence rather than reject the shot.
			if probe, err := media.Probe(ctx, audioPath); err == nil && probe.Duration < minimaxMinAudioSeconds {
				padded := strings.TrimSuffix(audioPath, filepath.Ext(audioPath)) + ".ref.wav"
				if err := media.PadAudio(ctx, audioPath, padded, minimaxMinAudioSeconds); err != nil {
					return ClipOutput{}, fmt.Errorf("pad sync audio: %w", err)
				}
				audioPath = padded
				notes = append(notes, fmt.Sprintf("sync audio padded %.1fs -> %.0fs", probe.Duration, minimaxMinAudioSeconds))
			}
			uri, err := minimax.DataURIFromFile(audioPath)
			if err != nil {
				return ClipOutput{}, fmt.Errorf("read sync audio: %w", err)
			}
			content = append(content, minimax.Content{Type: "audio_url", Role: "reference_audio", AudioURL: &minimax.URLRef{URL: uri}})
			notes = append(notes, "lip-synced to supplied audio")
		}
	}

	vreq := minimax.VideoRequest{
		Model:      model,
		Content:    content,
		Duration:   seconds,
		Resolution: resolution,
		Ratio:      ratio,
	}

	if onProgress != nil {
		onProgress(0, "submitting to MiniMax")
	}
	taskID, err := b.Client.SubmitVideo(ctx, vreq)
	if err != nil {
		return ClipOutput{}, err
	}
	// The task id goes into the event log so a render orphaned by a studio
	// restart can still be fetched from MiniMax by hand.
	if onProgress != nil {
		onProgress(0, "submitted to MiniMax task="+taskID)
	}
	st, err := b.Client.WaitVideo(ctx, taskID, 5*time.Second, func(elapsed time.Duration, _ *minimax.VideoStatus) {
		if onProgress != nil {
			onProgress(elapsed, "rendering")
		}
	})
	if err != nil {
		return ClipOutput{}, err
	}
	if st.Task.Content == nil || st.Task.Content.URL == "" {
		return ClipOutput{}, fmt.Errorf("render finished but no video URL")
	}
	if err := b.Client.DownloadFile(ctx, st.Task.Content.URL, req.DestPath); err != nil {
		return ClipOutput{}, fmt.Errorf("download: %w", err)
	}

	duration := float64(seconds)
	if st.Task.Duration > 0 {
		duration = st.Task.Duration
	}
	note := fmt.Sprintf("MiniMax %s, %s, %ds, ratio %s", model, resolution, seconds, ratio)
	if len(notes) > 0 {
		note += "; " + strings.Join(notes, "; ")
	}
	return ClipOutput{Duration: duration, Note: note}, nil
}

// --- engine selection ---

const (
	EngineGrok    = "grok"
	EngineH3      = "h3"
	EngineMiniMax = "minimax"
)

// VideoEngine is the studio-wide choice of renderer. It is resolved into a
// per-production VideoBackend, since the local engine namespaces its uploads
// and outputs by production.
type VideoEngine struct {
	Kind  string
	Pool  *comfy.Pool
	Steps int
	// MiniMax is the hosted-API client, set when Kind == EngineMiniMax.
	MiniMax *minimax.Client
}

// Backend returns the renderer for one production.
func (e VideoEngine) Backend(x *xai.Client, productionID string) VideoBackend {
	switch e.Kind {
	case EngineH3:
		if e.Pool != nil {
			return &H3Backend{Pool: e.Pool, Production: productionID, Steps: e.Steps}
		}
	case EngineMiniMax:
		if e.MiniMax != nil {
			return &MiniMaxBackend{Client: e.MiniMax, Production: productionID}
		}
	}
	return &GrokBackend{Client: x}
}

// queueWithRetry submits the workflow, riding out short server outages (a
// restarting ComfyUI takes a minute or two to come back) with backoff instead
// of failing the clip.
func queueWithRetry(ctx context.Context, client *comfy.Client, graph map[string]any, onProgress func(time.Duration, string)) (string, error) {
	start := time.Now()
	delay := 10 * time.Second
	for attempt := 1; ; attempt++ {
		id, err := client.Queue(ctx, graph)
		if err == nil {
			return id, nil
		}
		if !isInfraError(err) || attempt >= 8 {
			return "", err
		}
		if onProgress != nil {
			onProgress(time.Since(start), fmt.Sprintf("render server unreachable, retrying (attempt %d)", attempt))
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(delay):
		}
		if delay < 40*time.Second {
			delay *= 2
		}
	}
}

// isInfraError reports whether an error is a failure of the rendering
// infrastructure (unreachable server, reset connection, timeout) rather than
// of the render itself. Infra failures are refunded to the take budget and
// retried by the backend, because they say nothing about the shot.
func isInfraError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, sig := range []string{
		"connection refused", "connection reset", "no such host",
		"context deadline exceeded", "EOF", "broken pipe",
		"Service Unavailable", "status 502", "status 503", "status 504",
		// A dead Ray actor or a poisoned CUDA context is a renderer failure,
		// not a shot failure: the server needs a restart and the take must
		// be refunded.
		"actor died", "actor is dead", "ActorDiedError",
		"AcceleratorError", "CUDA error", "launch failure",
	} {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}
