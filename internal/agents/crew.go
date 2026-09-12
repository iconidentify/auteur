package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"auteur/internal/media"
	"auteur/internal/studio"
	"auteur/pkg/elevenlabs"
	"auteur/pkg/harness"
	"auteur/pkg/xai"
)

// Crew wires the studio agents for one production run.
type Crew struct {
	XAI        *xai.Client
	Provider   *XAIProvider
	Skills     *harness.SkillLibrary
	Studio     *studio.Studio
	Production *studio.Production
	Sink       harness.EventSink
	Model      string
	// Video selects the clip renderer: hosted Grok Imagine, local H3, or
	// hosted MiniMax.
	Video VideoEngine
	// Eleven voices narration and dialogue for film productions.
	Eleven *elevenlabs.Client

	// ledger is the production's durable spend/budget memory, shared by
	// every agent in the crew.
	ledger *Ledger
}

// Ledger lazily opens the production ledger.
func (c *Crew) Ledger() *Ledger {
	if c.ledger == nil {
		c.ledger = OpenLedger(c.workspace())
	}
	return c.ledger
}

func (c *Crew) workspace() string { return c.Studio.Dir(c.Production.ID) }

// mode is the production mode, defaulting to the music-video studio.
func (c *Crew) mode() string {
	if c.Production.Mode == "" {
		return studio.ModeMusicVideo
	}
	return c.Production.Mode
}

// brief renders the production brief shared by every agent's prompt.
func (c *Crew) brief() string {
	if c.mode() == studio.ModeFilm {
		return c.filmBrief()
	}
	var b strings.Builder
	b.WriteString("## The production\n")
	fmt.Fprintf(&b, "Title: %s\n", c.Production.Title)
	fmt.Fprintf(&b, "Music track: %s\n", c.Production.MusicFile)
	if len(c.Production.ReferenceFiles) > 0 {
		fmt.Fprintf(&b, "Reference images (%d): %s\n", len(c.Production.ReferenceFiles), strings.Join(c.Production.ReferenceFiles, ", "))
	} else {
		b.WriteString("Reference images: none provided\n")
	}
	if c.Production.OpeningImage != "" {
		fmt.Fprintf(&b, "OPENING FRAME: %s. The producer designated this image as the film's first frame. "+
			"The opening shot MUST be generated image-to-video from this exact file, and its look "+
			"(palette, light, subject, mood) is the tonal keystone the whole film grows out of. "+
			"The curator treats it as the primary style anchor; the cinematographer animates it as shot one.\n",
			c.Production.OpeningImage)
	}
	if c.Production.LyricsFile != "" {
		fmt.Fprintf(&b, "LYRICS: the producer supplied the song's lyrics at %s — these are the "+
			"AUTHORITATIVE words. Run transcribe_lyrics on the track for line timing, align it "+
			"against the provided text, and let the words drive the film: the treatment, the shot "+
			"list, and the cut must flow with what is being said, line by line.\n",
			c.Production.LyricsFile)
	} else {
		b.WriteString("Lyrics: none provided. If the track has vocals, transcribe_lyrics recovers " +
			"the words and their timing — a vocal track's words still drive the film.\n")
	}
	prompt := strings.TrimSpace(c.Production.ProducerPrompt)
	if prompt == "" {
		prompt = "(none given: you have full creative latitude, guided by the music)"
	}
	fmt.Fprintf(&b, "\nPRODUCER'S PROMPT (the client's direction, rules everything):\n%s\n", prompt)
	return b.String()
}

// filmBrief renders the brief for a narrated-film production: the source
// text, the animation style (as language every prompt must carry), and the
// producer's direction.
func (c *Crew) filmBrief() string {
	var b strings.Builder
	b.WriteString("## The production\n")
	fmt.Fprintf(&b, "Title: %s\n", c.Production.Title)
	b.WriteString("MODE: FILM — a narrated, animated motion picture adapted from source text.\n")
	fmt.Fprintf(&b, "SOURCE TEXT: %s — these are the AUTHORITATIVE words. The screenplay adapts them "+
		"faithfully; every spoken line comes from them; the film shows what they say.\n",
		c.Production.SourceTextFile)
	if st, ok := StyleByID(c.Production.AnimationStyle); ok {
		fmt.Fprintf(&b, "ANIMATION STYLE: %s — %s\n", st.Name, st.Blurb)
		fmt.Fprintf(&b, "STYLE LANGUAGE (bake this into EVERY image prompt and EVERY clip prompt, without "+
			"exception — it is what makes the film one consistent hand): %s\n", st.Prompt)
	} else if c.Production.AnimationStyle != "" {
		fmt.Fprintf(&b, "ANIMATION STYLE: %s (bake it into every prompt)\n", c.Production.AnimationStyle)
	}
	if len(c.Production.ReferenceFiles) > 0 {
		fmt.Fprintf(&b, "Reference images (%d): %s — the producer's visual anchors; the art department "+
			"reconciles the style bible to them.\n", len(c.Production.ReferenceFiles),
			strings.Join(c.Production.ReferenceFiles, ", "))
	}
	if c.Production.OpeningImage != "" {
		fmt.Fprintf(&b, "OPENING FRAME: %s — designated as the film's first image; shot one animates "+
			"out of it and the bible's global look is derived from it.\n", c.Production.OpeningImage)
	}
	if c.Production.MusicFile != "" {
		fmt.Fprintf(&b, "SCORE: an optional music bed at %s. The narration track is the film's voice; "+
			"the score, if used, sits under it.\n", c.Production.MusicFile)
	}
	if c.Eleven == nil {
		b.WriteString("NOTE: no voice engine is configured; lines cannot be voiced this run.\n")
	}
	prompt := strings.TrimSpace(c.Production.ProducerPrompt)
	if prompt == "" {
		prompt = "(none given: adapt the source text faithfully, at a length that serves it, guided by the style)"
	}
	fmt.Fprintf(&b, "\nPRODUCER'S PROMPT (the client's direction, rules everything):\n%s\n", prompt)
	return b.String()
}

// engineBlock states which renderer this production actually shoots on. The
// role prompts and the promptcraft skills are written around Grok Imagine, so
// when a different engine is wired in this block is the authority that
// overrides them.
func (c *Crew) engineBlock() string {
	if c.Video.Kind == EngineMiniMax {
		return minimaxEngineBlock
	}
	if c.Video.Kind != EngineH3 {
		return ""
	}
	renderPar := ""
	if c.Video.Pool != nil && c.Video.Pool.Size() > 1 {
		renderPar = fmt.Sprintf("\n- RENDER POOL: %d render nodes are available; up to %d clips "+
			"render in parallel. Submit independent shots as one batch to overlap them. Chained "+
			"shots still serialize (each needs the previous one's last frame).", c.Video.Pool.Size(), c.Video.Pool.Size())
	}
	return `## THE CAMERA: MiniMax H3, running locally

This production does NOT shoot on Grok Imagine. Clips are rendered by MiniMax
H3 on this machine's two GPUs. Where the grok-video-promptcraft skill and your
role brief disagree with this block, this block wins. Read the
h3-video-promptcraft skill before writing prompts.

THE ONE RULE: continuity comes from frames, never from descriptions. H3 is a
first/last-frame model — mode 'image' animates forward from image_path, and
adding end_image_path interpolates between two stills. So when a shot must
match existing imagery (the opening frame, a reference, the previous shot),
take the frame FROM that imagery: animate it, extract_frame the moment you
need, and seed the next shot with it. The empty room after a character leaves
IS the last frame of the shot where they leave — render that shot and harvest
it. Never try to regenerate a matching still with generate_image: a
text-to-image model cannot see the target, every take re-rolls the
composition, and no number of takes converges. generate_image is for NEW
compositions only — establishing looks, inserts, cutaways that match nothing.

The practical consequences:

- Mode 'reference' (REF2VA) injects SPECIFIC CONTENT into a scene: pass up to
  9 reference_paths and bind each in the prompt as <Picture 1>, <Picture 2>
  in list order ("the monitor displays <Picture 1>", "<Picture 2> defines the
  logo"). Use it when something exact must appear INSIDE the world — a
  screenshot as a screen's contents, a logo, a product, a subject's identity.
  It costs ~2.5x a normal render (20 steps, no turbo LoRA) and cannot take
  image_path, so it does NOT replace chaining for shot-to-shot continuity or
  cropped-pixel inserts — it is for content the footage cannot supply.
- Take budgets are ENFORCED by the tools: 3 takes per still, 3 takes per
  shot, hard run caps behind those. A refused take is an instruction to use
  the best take on disk or change technique, not an obstacle to work around.
  Judge takes like a professional: is it usable, not is it perfect.
- Shoot at 480p (864x480); it is the studio default. Use 720p (1344x768,
  ~3x slower) only when the producer's brief explicitly asks for it. Renders
  are sequential on the local GPUs at roughly 40 seconds per second of 480p
  video, and every clip result reports the run's cumulative GPU minutes:
  watch it, and spend it on the film, not on retakes.
- Duration is 2-15s per clip, snapped to the model's frame grid (3s=73,
  5s=124, 10s=243, 15s=362 frames at 24fps).
- Every clip arrives with model-generated audio. Ignore it: the music track
  is the film's only audio, and the edit strips the rest.
- Each clip reports its seed. Reuse the seed to refine a take with a tweaked
  prompt; omit it for a genuinely fresh take.
- LIP SYNC. A shot where a face performs the track (singing, rapping,
  speaking) uses mode 'reference' with sync_audio_path (usually the music
  file) and sync_audio_from_seconds (where in the track the shot's lines
  start — take it from analysis/lyrics.json). The render's performance syncs
  to that exact slice. Reserve it for shots where the face is close enough
  to read; it costs reference-mode time. THE EDIT CONTRACT: a synced clip
  goes on the timeline at exactly sync_audio_from_seconds with no head trim,
  or the sync is destroyed — the clip result restates this.
- CUTTING GRAMMAR. Chained shots join invisibly only at the exact shared
  frame: outgoing plays to its last frame, incoming starts at 0.0, no trims
  at the join, no beat-alignment needed there. Seed only from the outgoing
  shot's END — never from a frame the audience already saw, and never morph
  a chain back toward an already-shown framing (it reads as the camera
  snapping back). Visible cuts need two or more steps of size contrast.
  Inserts and close-ups of anything already on screen come from CROPPED REAL
  PIXELS of the plate or a harvested frame, never from generate_image — a
  generated insert is a different object and the audience sees it instantly.
` + renderPar + `
- The render server is managed OUTSIDE the studio. If it is unreachable, the
  tools retry on their own and outages do not count against take budgets.
  NEVER start, stop, restart, or kill server or GPU processes yourself — if
  the outage persists, note it in your report and move to work that does not
  need the renderer.`
}

// minimaxEngineBlock is the camera authority for productions shooting on the
// hosted MiniMax H3 API.
const minimaxEngineBlock = `## THE CAMERA: MiniMax H3, hosted (minimax.io)

This production does NOT shoot on Grok Imagine or on local GPUs. Clips render
on the hosted MiniMax H3 API, off this machine. Where a promptcraft skill or
your role brief disagrees with this block, this block wins.

- MODES. 'text' (prompt only), 'image' (animate a still: image_path is the
  FIRST frame; add end_image_path to interpolate to a LAST frame), and
  'reference' (up to 9 reference_paths injected into the shot; bind each in
  the prompt as <Picture 1>, <Picture 2> in list order). Reference mode is
  how characters and places stay consistent: attach their sheets/keyframes.
- DURATION is 4-15 WHOLE seconds per clip. Resolution 768P is the studio
  default; 2K only when the brief explicitly asks.
- PARALLEL RENDERING. The camera renders up to 25 clips at once. Submit
  independent shots as one large batch (they all render simultaneously, a
  few minutes each); only chained continuity shots need to serialize.
  Serializing independent shots wastes the film's time.
- LIP SYNC. A shot where a pictured character speaks uses mode 'reference'
  with sync_audio_path = that shot's own voiced WAV and
  sync_audio_from_seconds = 0: the character performs the audio. Name in the
  prompt which pictured character is speaking. Reserve it for shots where a
  character speaks on camera; narrator-over shots take no sync audio.
- Every clip arrives with model-generated audio. Ignore it: the edit
  replaces it with the film's narration track.
- No seeds and no frame grid on this camera: durations are plain seconds,
  and a retake is a fresh render with a better prompt.
- Take budgets are ENFORCED by the tools (3 takes per shot). A refused take
  means use the best take on disk or change technique.
- The camera is a cloud service: transient failures retry on their own and
  do not count against budgets. Never try to start or stop render servers.`

func (c *Crew) systemPrompt(role string) string {
	parts := []string{
		strings.TrimSpace(rolePromptFor(role, c.mode())),
		strings.TrimSpace(sharedContextFor(c.mode())),
		strings.TrimSpace(c.brief()),
	}
	if eb := c.engineBlock(); eb != "" {
		parts = append(parts, strings.TrimSpace(eb))
	}
	if block := c.Skills.PromptBlock(role); block != "" {
		parts = append(parts, strings.TrimSpace(block))
	}
	return strings.Join(parts, "\n\n")
}

// buildAgent constructs an agent for a role with its toolset and config.
func (c *Crew) buildAgent(role string) (*StudioAgent, harness.Config) {
	agentID := harness.NewID(role)
	reg := harness.NewRegistry()
	tb := &Toolbox{
		XAI:       c.XAI,
		Ledger:    c.Ledger(),
		Video:     c.Video.Backend(c.XAI, c.Production.ID),
		Workspace: c.workspace(),
		Sink:      c.Sink,
		AgentID:   agentID,
		AgentName: role,
		Eleven:    c.Eleven,
		Mode:      c.mode(),
	}

	// Everyone gets the local shell, workspace files, media probing, and skills.
	tb.RegisterFileTools(reg)
	tb.RegisterMediaTools(reg)
	c.Skills.RegisterSkillTool(reg, role)

	film := c.mode() == studio.ModeFilm
	switch role {
	case RoleScreenwriter:
		tb.RegisterVisionTools(reg)
		if film {
			tb.RegisterSpeechTools(reg) // list_voices, for casting
			tb.RegisterScreenplayTools(reg)
		}
	case RoleCurator:
		tb.RegisterVisionTools(reg)
		tb.RegisterImageGenTools(reg)
	case RoleCinematographer:
		tb.RegisterVisionTools(reg)
		tb.RegisterClipReviewTool(reg)
		tb.RegisterImageGenTools(reg)
		tb.RegisterVideoGenTools(reg)
		if film {
			tb.RegisterSpeechTools(reg) // synthesize_speech, per line
		}
	case RoleEditor:
		tb.RegisterVisionTools(reg)
		tb.RegisterClipReviewTool(reg)
		tb.RegisterEditTools(reg)
		if film {
			tb.RegisterSpeechTools(reg) // concat_audio, the narration track
		}
	case RoleDirector:
		tb.RegisterVisionTools(reg)
		tb.RegisterClipReviewTool(reg)
		if film {
			tb.RegisterSpeechTools(reg)
			tb.RegisterScreenplayTools(reg)
		}
	}

	ag := NewStudioAgent(role, c.systemPrompt(role), reg)

	cfg := harness.DefaultConfig()
	cfg.Model = c.Model
	cfg.AgentID = agentID
	cfg.AgentName = role
	cfg.MaxIterations = 60
	if role == RoleDirector {
		cfg.MaxIterations = 80
		// The director's kickoff can include a RESUMED PRODUCTION brief that
		// invites it to audit prior work out loud for a few turns before it
		// delegates. Give it more slack than the default 3 so a resumed run
		// isn't killed for orienting before it acts.
		cfg.MaxConsecutiveTextReplies = 8
	}
	return ag, cfg
}

// Factory returns the harness.AgentFactory used by the delegate tools.
func (c *Crew) Factory() harness.AgentFactory {
	return func(ctx context.Context, role string) (harness.Agent, harness.Config, error) {
		if _, ok := rolePrompts[role]; !ok || role == RoleDirector {
			return nil, harness.Config{}, fmt.Errorf("unknown role %q", role)
		}
		ag, cfg := c.buildAgent(role)
		return ag, cfg, nil
	}
}

// RunProduction runs the director agent to completion, maintaining the
// production state throughout. It is the entry point for a full run.
func (c *Crew) RunProduction(ctx context.Context) error {
	p := c.Production
	p.Status = studio.StatusRunning
	p.StartedAt = time.Now()
	p.Error = ""
	c.Studio.Save(p)

	director, cfg := c.buildAgent(RoleDirector)
	directorReg := directorRegistry(director)

	// Delegation to the specialist crew.
	harness.RegisterDelegateTools(directorReg, c.Factory(), c.Provider, c.Sink, cfg.AgentID, SubAgentRoles, 4)

	// finalize_production is the director's completion tool: it validates
	// and records the deliverable, then ends the run.
	directorReg.Register(harness.ToolDefinition{
		Name: "finalize_production",
		Description: "Deliver the finished film. Validates the file, records it as the production's " +
			"final video, and completes the production. Call only when the mastered final satisfies the brief.",
		InputSchema: harness.Obj(map[string]any{
			"video_path": harness.Str("Path to the finished video (typically final/master.mp4)"),
			"summary":    harness.Str("A director's statement: what the film is and how it interprets the music and brief"),
		}, "video_path", "summary"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		vp, _ := input["video_path"].(string)
		summary, _ := input["summary"].(string)
		abs := vp
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(c.workspace(), vp)
		}
		probe, err := media.Probe(ctx, abs)
		if err != nil {
			return "", fmt.Errorf("final video not usable: %w", err)
		}
		if probe.Duration < 1 {
			return "", fmt.Errorf("final video has no duration")
		}
		rel, rerr := filepath.Rel(c.workspace(), abs)
		if rerr != nil || strings.HasPrefix(rel, "..") {
			rel = vp
		}
		p.FinalVideo = rel
		c.Studio.Save(p)
		if c.Sink != nil {
			c.Sink.Emit(ctx, harness.Event{
				Type: "artifact", Message: "final delivered", Time: time.Now(),
				AgentID: cfg.AgentID, AgentName: RoleDirector,
				Data: map[string]any{"kind": "final", "path": rel, "duration": probe.Duration, "summary": summary},
			})
		}
		director.done = true
		director.report = summary
		return fmt.Sprintf("Production finalized: %s (%.1fs)", rel, probe.Duration), nil
	})

	// request_approval pauses the production for the producer's sign-off.
	directorReg.Register(harness.ToolDefinition{
		Name: "request_approval",
		Description: "Pause and ask the producer to approve before proceeding. Use it at the moments " +
			"the brief asks for sign-off (typically after the shot list, before any GPU spend). " +
			"Blocks until the producer approves or requests changes; returns their decision.",
		InputSchema: harness.Obj(map[string]any{
			"stage":   harness.Str("What is being approved, e.g. 'shot list'"),
			"summary": harness.Str("A concise producer-facing summary of what you intend to do"),
		}, "stage", "summary"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		stage, _ := input["stage"].(string)
		summary, _ := input["summary"].(string)
		req := map[string]any{"stage": stage, "summary": summary,
			"status": "pending", "time": time.Now()}
		path := filepath.Join(c.workspace(), "state", "approval.json")
		os.MkdirAll(filepath.Dir(path), 0o755)
		data, _ := json.MarshalIndent(req, "", "  ")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return "", err
		}
		if c.Sink != nil {
			c.Sink.Emit(ctx, harness.Event{
				Type: "approval_requested", Message: stage, Time: time.Now(),
				AgentID: cfg.AgentID, AgentName: RoleDirector,
				Data: map[string]any{"stage": stage, "summary": summary},
			})
		}
		for {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(3 * time.Second):
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var resp struct {
				Status string `json:"status"`
				Notes  string `json:"notes"`
			}
			if json.Unmarshal(raw, &resp) != nil {
				continue
			}
			switch resp.Status {
			case "approved":
				return "APPROVED by the producer. Proceed.", nil
			case "changes":
				return "CHANGES REQUESTED by the producer: " + resp.Notes +
					"\nRevise accordingly before proceeding (re-request approval if the brief requires it).", nil
			}
		}
	})

	// The plain finish tool must not bypass delivery.
	directorReg.Register(harness.ToolDefinition{
		Name:        "finish",
		Description: "Do not use: directors deliver via finalize_production.",
		InputSchema: harness.Obj(map[string]any{"report": harness.Str("unused")}),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		if p.FinalVideo == "" {
			return "", fmt.Errorf("nothing delivered yet: produce the film and call finalize_production")
		}
		director.done = true
		return "Production already finalized.", nil
	})

	cfg.InitialMessage = c.kickoffMessage()
	cfg.InitialImages = c.referenceImagePaths(8)
	cfg.Inbox = c.producerInbox()

	res, err := harness.RunLoop(ctx, director, c.Provider, cfg, c.Sink)
	if res != nil {
		p.TotalTokens = res.TotalTokens
	}
	p.FinishedAt = time.Now()
	switch {
	case err != nil && ctx.Err() != nil:
		p.Status = studio.StatusCancelled
		p.Error = "cancelled"
	case err != nil:
		p.Status = studio.StatusFailed
		p.Error = err.Error()
	case p.FinalVideo == "":
		p.Status = studio.StatusFailed
		p.Error = "run ended without a finalized video (stop reason: " + res.StopReason + ")"
	default:
		p.Status = studio.StatusCompleted
		// The producer's notes are only spent once the film is actually
		// delivered; an interrupted run's notes reach the next one.
		c.Ledger().CommitNotes()
	}
	c.Studio.Save(p)
	if err != nil {
		return err
	}
	if p.Status == studio.StatusFailed {
		return fmt.Errorf("%s", p.Error)
	}
	return nil
}

func (c *Crew) kickoffMessage() string {
	var b strings.Builder
	b.WriteString("The production is greenlit. The source files are in place: ")
	if c.mode() == studio.ModeFilm {
		fmt.Fprintf(&b, "the source text at %s", c.Production.SourceTextFile)
		if st, ok := StyleByID(c.Production.AnimationStyle); ok {
			fmt.Fprintf(&b, ", to be drawn in the %s style", st.Name)
		}
	} else {
		fmt.Fprintf(&b, "music at %s", c.Production.MusicFile)
	}
	if n := len(c.Production.ReferenceFiles); n > 0 {
		fmt.Fprintf(&b, ", %d reference image(s) attached to this message (files: %s)",
			n, strings.Join(c.Production.ReferenceFiles, ", "))
	}
	if c.mode() == studio.ModeFilm {
		b.WriteString(". Take it from the top: read the source text, run your crew, and deliver the film.")
	} else {
		b.WriteString(". Take it from the top: study the brief, run your crew, and deliver the film.")
	}
	if brief := c.Ledger().ResumeBrief(); brief != "" {
		b.WriteString("\n\n" + brief)
	}
	if notes := c.undeliveredNotes(); notes != "" {
		b.WriteString("\n\n" + notes)
	}
	return b.String()
}

// producerInbox delivers new producer notes into the director's loop, one
// poll per iteration, with the delivery cursor persisted in the ledger.
func (c *Crew) producerInbox() func() []string {
	return func() []string {
		notes, err := studio.ReadNotes(c.workspace())
		if err != nil {
			return nil
		}
		cursor := c.Ledger().NotesDelivered()
		if len(notes) <= cursor {
			return nil
		}
		var out []string
		for _, n := range notes[cursor:] {
			out = append(out, "PRODUCER'S NOTE (live direction from the client — it outranks the "+
				"original brief and every planning document where they conflict; act on it and "+
				"relay it to any specialist it affects): "+n.Text)
		}
		c.Ledger().MarkNotesDelivered(len(notes))
		return out
	}
}

// undeliveredNotes folds notes that arrived between runs into the kickoff.
func (c *Crew) undeliveredNotes() string {
	notes, err := studio.ReadNotes(c.workspace())
	if err != nil {
		return ""
	}
	cursor := c.Ledger().NotesDelivered()
	if len(notes) <= cursor {
		return ""
	}
	var b strings.Builder
	b.WriteString("## PRODUCER'S NOTES received while the studio was dark (act on all of them):\n")
	for _, n := range notes[cursor:] {
		fmt.Fprintf(&b, "- %s\n", n.Text)
	}
	c.Ledger().MarkNotesDelivered(len(notes))
	return b.String()
}

func (c *Crew) referenceImagePaths(max int) []string {
	var out []string
	// The opening frame, when set, always rides first in the director's view.
	files := c.Production.ReferenceFiles
	if c.Production.OpeningImage != "" {
		files = append([]string{c.Production.OpeningImage}, files...)
	}
	for _, rf := range files {
		if len(out) >= max {
			break
		}
		abs := filepath.Join(c.workspace(), rf)
		if _, err := os.Stat(abs); err == nil {
			out = append(out, abs)
		}
	}
	return out
}

// directorRegistry exposes the private registry of a StudioAgent for the
// director-specific tool wiring above.
func directorRegistry(a *StudioAgent) *harness.Registry { return a.reg }
