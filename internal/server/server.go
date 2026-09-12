// Package server exposes the Auteur studio over HTTP: a REST API for
// productions, an SSE stream of live agent events, workspace file serving,
// and the embedded control-room UI.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"auteur/internal/agents"
	"auteur/internal/media"
	"auteur/internal/studio"
	"auteur/pkg/elevenlabs"
	"auteur/pkg/harness"
	"auteur/pkg/xai"
)

//go:embed ui
var uiFS embed.FS

type Server struct {
	Studio *studio.Studio
	XAI    *xai.Client
	Skills *harness.SkillLibrary
	Model  string
	// Video selects the clip renderer used by every production.
	Video agents.VideoEngine
	// Eleven voices narrated films; nil disables film-mode speech.
	Eleven *elevenlabs.Client
	// Viewer disables run control (start/cancel): another process owns the
	// runs and this instance only observes the shared workspace.
	Viewer bool
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/styles", s.handleStyles)
	mux.HandleFunc("GET /api/productions", s.handleList)
	mux.HandleFunc("POST /api/productions", s.handleCreate)
	mux.HandleFunc("GET /api/productions/{id}", s.handleGet)
	mux.HandleFunc("POST /api/productions/{id}/start", s.handleStart)
	mux.HandleFunc("POST /api/productions/{id}/cancel", s.handleCancel)
	mux.HandleFunc("POST /api/productions/{id}/notes", s.handleNote)
	mux.HandleFunc("POST /api/productions/{id}/approval", s.handleApproval)
	mux.HandleFunc("POST /api/productions/{id}/finalize", s.handleFinalize)
	mux.HandleFunc("GET /api/productions/{id}/events", s.handleEvents)
	mux.HandleFunc("GET /api/productions/{id}/files/{path...}", s.handleFile)

	ui, _ := fs.Sub(uiFS, "ui")
	mux.Handle("/", http.FileServerFS(ui))
	return mux
}

// firstWords titles a film from the opening words of its source text.
func firstWords(text string, n int) string {
	words := strings.Fields(text)
	if len(words) > n {
		words = words[:n]
	}
	t := strings.Join(words, " ")
	t = strings.TrimRight(t, ".,;:!?\"'")
	if t == "" {
		return "Untitled film"
	}
	return t
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Studio.List()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if list == nil {
		list = []*studio.Production{}
	}
	writeJSON(w, 200, list)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	p, err := s.Studio.Load(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	writeJSON(w, 200, p)
}

// handleStyles lists the animation styles offered for narrated films.
func (s *Server) handleStyles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, agents.AnimationStyles)
}

// handleCreate accepts multipart form data: title, prompt, mode, music (file),
// references (files, repeatable), and for film mode source_text +
// animation_style.
func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		writeErr(w, 400, fmt.Errorf("parse form: %w", err))
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	prompt := r.FormValue("prompt")
	mode := strings.TrimSpace(r.FormValue("mode"))
	if mode == "" {
		mode = studio.ModeMusicVideo
	}
	sourceText := strings.TrimSpace(r.FormValue("source_text"))
	styleID := strings.TrimSpace(r.FormValue("animation_style"))

	musicFiles := r.MultipartForm.File["music"]
	switch mode {
	case studio.ModeFilm:
		if sourceText == "" {
			writeErr(w, 400, fmt.Errorf("source text is required for a film"))
			return
		}
		if _, ok := agents.StyleByID(styleID); !ok {
			writeErr(w, 400, fmt.Errorf("choose an animation style"))
			return
		}
		if title == "" {
			title = firstWords(sourceText, 6)
		}
	case studio.ModeMusicVideo:
		if len(musicFiles) == 0 {
			writeErr(w, 400, fmt.Errorf("a music file is required"))
			return
		}
		if title == "" {
			title = strings.TrimSuffix(musicFiles[0].Filename, filepath.Ext(musicFiles[0].Filename))
		}
	default:
		writeErr(w, 400, fmt.Errorf("unknown mode %q", mode))
		return
	}

	p, err := s.Studio.Create(title, prompt)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	p.Mode = mode
	if mode == studio.ModeFilm {
		rel := path.Join("source", "source.txt")
		if err := os.WriteFile(filepath.Join(s.Studio.Dir(p.ID), rel), []byte(sourceText+"\n"), 0o644); err != nil {
			writeErr(w, 500, err)
			return
		}
		p.SourceTextFile = rel
		p.AnimationStyle = styleID
	}
	saveUpload := func(fhs []*multipart.FileHeader, prefix string) ([]string, error) {
		var rels []string
		for i, fh := range fhs {
			ext := strings.ToLower(filepath.Ext(fh.Filename))
			name := fmt.Sprintf("%s-%02d%s", prefix, i+1, ext)
			if prefix == "music" {
				name = "music" + ext
			}
			rel := path.Join("source", name)
			dst := filepath.Join(s.Studio.Dir(p.ID), rel)
			if err := copyUpload(fh, dst); err != nil {
				return nil, err
			}
			if prefix != "music" {
				final, err := normalizeImage(r.Context(), dst)
				if err != nil {
					slog.Warn("skipping unusable reference", "file", fh.Filename, "err", err)
					continue
				}
				rel = path.Join("source", filepath.Base(final))
			}
			rels = append(rels, rel)
		}
		return rels, nil
	}

	// Music is the source of a music video and an optional score for a film.
	if len(musicFiles) > 0 {
		music, err := saveUpload(musicFiles[:1], "music")
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		p.MusicFile = music[0]
	}

	refs, err := saveUpload(r.MultipartForm.File["references"], "ref")
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	p.ReferenceFiles = refs

	if opening := r.MultipartForm.File["opening"]; len(opening) > 0 {
		ext := strings.ToLower(filepath.Ext(opening[0].Filename))
		rel := path.Join("source", "opening"+ext)
		dst := filepath.Join(s.Studio.Dir(p.ID), rel)
		if err := copyUpload(opening[0], dst); err != nil {
			writeErr(w, 500, err)
			return
		}
		final, nerr := normalizeImage(r.Context(), dst)
		if nerr != nil {
			writeErr(w, 400, fmt.Errorf("opening image unusable: %w", nerr))
			return
		}
		p.OpeningImage = path.Join("source", filepath.Base(final))
	}

	if lyrics := strings.TrimSpace(r.FormValue("lyrics")); lyrics != "" {
		rel := path.Join("source", "lyrics.txt")
		if err := os.WriteFile(filepath.Join(s.Studio.Dir(p.ID), rel), []byte(lyrics+"\n"), 0o644); err != nil {
			writeErr(w, 500, err)
			return
		}
		p.LyricsFile = rel
	}

	if err := s.Studio.Save(p); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 201, p)
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if s.Viewer {
		writeErr(w, 409, fmt.Errorf("this instance is a viewer; start productions from the primary studio process"))
		return
	}
	id := r.PathValue("id")
	p, err := s.Studio.Load(id)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if p.Status == studio.StatusRunning {
		writeErr(w, 409, fmt.Errorf("production already running"))
		return
	}
	if p.Mode == studio.ModeFilm {
		if p.SourceTextFile == "" {
			writeErr(w, 400, fmt.Errorf("film production has no source text"))
			return
		}
		if s.Eleven == nil {
			writeErr(w, 400, fmt.Errorf("film mode needs a voice engine: set ELEVENLABS_API_KEY"))
			return
		}
	} else if p.MusicFile == "" {
		writeErr(w, 400, fmt.Errorf("production has no music file"))
		return
	}

	// Fresh event segment per run: the UI replays only this run on connect.
	s.Studio.Hub(id).Rotate()

	ctx, cancel := context.WithCancel(context.Background())
	s.Studio.SetCancel(id, cancel)

	crew := &agents.Crew{
		XAI:        s.XAI,
		Provider:   &agents.XAIProvider{Client: s.XAI},
		Skills:     s.Skills,
		Studio:     s.Studio,
		Production: p,
		Sink:       s.Studio.Hub(id),
		Model:      s.Model,
		Video:      s.Video,
		Eleven:     s.Eleven,
	}
	go func() {
		defer cancel()
		defer s.Studio.Cancel(id) // clear the cancel registration
		if err := crew.RunProduction(ctx); err != nil {
			slog.Error("production run ended with error", "id", id, "err", err)
		} else {
			slog.Info("production completed", "id", id)
		}
	}()

	writeJSON(w, 202, map[string]string{"status": "started", "id": id})
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	if s.Viewer {
		writeErr(w, 409, fmt.Errorf("this instance is a viewer; cancel from the primary studio process"))
		return
	}
	id := r.PathValue("id")
	if !s.Studio.Cancel(id) {
		writeErr(w, 409, fmt.Errorf("production is not running"))
		return
	}
	writeJSON(w, 200, map[string]string{"status": "cancelling"})
}

// handleEvents streams the production's event log over SSE: full replay
// (or resume from Last-Event-ID / ?after=) followed by live events.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.Studio.Load(id); err != nil {
		writeErr(w, 404, err)
		return
	}
	var after int64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		after, _ = strconv.ParseInt(v, 10, 64)
	} else if v := r.URL.Query().Get("after"); v != "" {
		after, _ = strconv.ParseInt(v, 10, 64)
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)

	lastSeq := after
	send := func(se studio.SeqEvent) bool {
		if se.Seq <= lastSeq {
			return true
		}
		data, err := json.Marshal(se.Event)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", se.Seq, data); err != nil {
			return false
		}
		lastSeq = se.Seq
		return true
	}

	hub := s.Studio.Hub(id)
	replay, live, cancelSub := hub.Subscribe(after)
	defer cancelSub()
	for _, se := range studio.CoalesceReplay(replay) {
		if !send(se) {
			return
		}
	}
	flusher.Flush()

	// Live events arrive on the in-process channel; the ticker re-reads the
	// log to catch events appended by another process on this workspace.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case se := <-live:
			if !send(se) {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			sent := false
			for _, se := range hub.ReadSince(lastSeq) {
				if !send(se) {
					return
				}
				sent = true
			}
			if sent {
				flusher.Flush()
			}
		}
	}
}

// handleFile serves workspace files (clips, thumbnails, finals, documents).
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rel := r.PathValue("path")
	root, err := filepath.Abs(s.Studio.Dir(id))
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	// Confine to the workspace.
	if resolved, err := filepath.Abs(abs); err != nil || !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		writeErr(w, 400, fmt.Errorf("invalid path"))
		return
	}
	if ct := mime.TypeByExtension(filepath.Ext(abs)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeFile(w, r, abs)
}

func copyUpload(fh *multipart.FileHeader, dst string) error {
	src, err := fh.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, src)
	return err
}

// handleNote records live producer direction; a running director picks it up
// on its next iteration, and a dark production receives it at next kickoff.
func (s *Server) handleNote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.Studio.Load(id); err != nil {
		writeErr(w, 404, err)
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Text) == "" {
		writeErr(w, 400, fmt.Errorf("provide {\"text\": ...}"))
		return
	}
	if err := studio.AppendNote(s.Studio.Dir(id), strings.TrimSpace(in.Text)); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.Studio.Hub(id).Emit(r.Context(), harness.Event{
		Type: "producer_note", Message: strings.TrimSpace(in.Text), Time: time.Now(),
		AgentName: "producer",
	})
	writeJSON(w, 200, map[string]string{"status": "noted"})
}

// handleApproval answers a pending request_approval gate.
func (s *Server) handleApproval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.Studio.Load(id); err != nil {
		writeErr(w, 404, err)
		return
	}
	var in struct {
		Approve bool   `json:"approve"`
		Notes   string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, err)
		return
	}
	path := filepath.Join(s.Studio.Dir(id), "state", "approval.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		writeErr(w, 404, fmt.Errorf("no pending approval request"))
		return
	}
	var req map[string]any
	if json.Unmarshal(raw, &req) != nil || req["status"] != "pending" {
		writeErr(w, 409, fmt.Errorf("no pending approval request"))
		return
	}
	if in.Approve {
		req["status"] = "approved"
	} else {
		req["status"] = "changes"
	}
	req["notes"] = in.Notes
	data, _ := json.MarshalIndent(req, "", "  ")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": req["status"].(string)})
}

// handleFinalize records an externally produced deliverable, so a production
// finished outside the crew (a hand-fixed master) still closes as completed.
func (s *Server) handleFinalize(w http.ResponseWriter, r *http.Request) {
	if s.Viewer {
		writeErr(w, 403, fmt.Errorf("viewer instance"))
		return
	}
	id := r.PathValue("id")
	p, err := s.Studio.Load(id)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if p.Status == studio.StatusRunning {
		writeErr(w, 409, fmt.Errorf("production is running; cancel first or let it finalize itself"))
		return
	}
	var in struct {
		VideoPath string `json:"video_path"`
		Summary   string `json:"summary"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.VideoPath == "" {
		writeErr(w, 400, fmt.Errorf("provide {\"video_path\": ..., \"summary\": ...}"))
		return
	}
	abs := filepath.Join(s.Studio.Dir(id), in.VideoPath)
	probe, err := media.Probe(r.Context(), abs)
	if err != nil || probe.Duration < 1 {
		writeErr(w, 400, fmt.Errorf("video not usable: %v", err))
		return
	}
	p.FinalVideo = in.VideoPath
	p.Status = studio.StatusCompleted
	p.Error = ""
	s.Studio.Save(p)
	s.Studio.Hub(id).Emit(r.Context(), harness.Event{
		Type: "artifact", Message: "final delivered (producer finalize)", Time: time.Now(),
		AgentName: "producer",
		Data: map[string]any{"kind": "final", "path": in.VideoPath,
			"duration": probe.Duration, "summary": in.Summary},
	})
	writeJSON(w, 200, map[string]string{"status": "completed"})
}
