// Package studio owns the production domain: a Production is one run of
// the agentic music-video pipeline, with a filesystem workspace, a
// persisted event log, and a live event hub feeding the UI.
package studio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"auteur/pkg/harness"
)

type Status string

const (
	StatusDraft     Status = "draft"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Production modes.
const (
	// ModeMusicVideo builds a film on a music track (the original studio).
	ModeMusicVideo = "music-video"
	// ModeFilm builds a narrated, animated motion picture from source text:
	// the words become a screenplay, characters are cast with voices, and
	// every shot is rendered to its spoken line.
	ModeFilm = "film"
)

// Production is one project: a music video or a narrated film.
type Production struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Status         Status `json:"status"`
	Mode           string `json:"mode,omitempty"` // "" or music-video | film
	ProducerPrompt string `json:"producer_prompt"`
	MusicFile      string `json:"music_file"` // relative to workspace
	LyricsFile     string `json:"lyrics_file,omitempty"`
	// Film mode: the source text the screenplay adapts, the chosen animation
	// style id, and the assembled narration/dialogue track once voiced.
	SourceTextFile string    `json:"source_text_file,omitempty"`
	AnimationStyle string    `json:"animation_style,omitempty"`
	NarrationFile  string    `json:"narration_file,omitempty"`
	ReferenceFiles []string  `json:"reference_files"` // relative to workspace
	OpeningImage   string    `json:"opening_image,omitempty"`
	FinalVideo     string    `json:"final_video,omitempty"`
	Error          string    `json:"error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	FinishedAt     time.Time `json:"finished_at,omitempty"`
	TotalTokens    int64     `json:"total_tokens,omitempty"`
}

// Studio manages productions under a root directory:
//
//	<root>/<production-id>/production.json
//	<root>/<production-id>/events.jsonl
//	<root>/<production-id>/source/    uploaded music + reference images
//	<root>/<production-id>/analysis/  audio analysis, treatments, notes
//	<root>/<production-id>/frames/    generated keyframes
//	<root>/<production-id>/clips/     generated video clips + thumbnails
//	<root>/<production-id>/final/     stitched and mastered output
type Studio struct {
	Root string

	mu     sync.RWMutex
	hubs   map[string]*EventHub
	cancel map[string]func()
}

func New(root string) (*Studio, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	s := &Studio{
		Root:   root,
		hubs:   map[string]*EventHub{},
		cancel: map[string]func(){},
	}
	s.recoverStale()
	return s, nil
}

// NewViewer opens a studio root read-only-ish: it never rewrites stale
// state, so a second process can safely observe a workspace while another
// process owns the runs.
func NewViewer(root string) (*Studio, error) {
	return &Studio{
		Root:   root,
		hubs:   map[string]*EventHub{},
		cancel: map[string]func(){},
	}, nil
}

// recoverStale marks productions left in "running" by a crashed or killed
// process as failed, so they can be started again.
func (s *Studio) recoverStale() {
	list, err := s.List()
	if err != nil {
		return
	}
	for _, p := range list {
		if p.Status == StatusRunning {
			p.Status = StatusFailed
			p.Error = "interrupted: the studio process exited mid-run"
			p.FinishedAt = time.Now()
			s.Save(p)
		}
	}
}

func (s *Studio) Dir(id string) string { return filepath.Join(s.Root, id) }

// Create makes a new draft production with an empty workspace.
func (s *Studio) Create(title, producerPrompt string) (*Production, error) {
	p := &Production{
		ID:             harness.NewID("prod"),
		Title:          title,
		Status:         StatusDraft,
		ProducerPrompt: producerPrompt,
		CreatedAt:      time.Now(),
	}
	for _, sub := range []string{"source", "analysis", "frames", "clips", "final"} {
		if err := os.MkdirAll(filepath.Join(s.Dir(p.ID), sub), 0o755); err != nil {
			return nil, err
		}
	}
	if err := s.Save(p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Studio) Save(p *Production) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Dir(p.ID), "production.json"), data, 0o644)
}

func (s *Studio) Load(id string) (*Production, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir(id), "production.json"))
	if err != nil {
		return nil, fmt.Errorf("production %s not found: %w", id, err)
	}
	var p Production
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// List returns all productions, newest first.
func (s *Studio) List() ([]*Production, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return nil, err
	}
	var out []*Production
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, err := s.Load(e.Name())
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// Hub returns (creating if needed) the event hub for a production.
func (s *Studio) Hub(id string) *EventHub {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.hubs[id]; ok {
		return h
	}
	h := NewEventHub(filepath.Join(s.Dir(id), "events.jsonl"))
	s.hubs[id] = h
	return h
}

// SetCancel registers the cancel func for a running production.
func (s *Studio) SetCancel(id string, cancel func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel[id] = cancel
}

// Cancel stops a running production, returning false if it was not running.
func (s *Studio) Cancel(id string) bool {
	s.mu.Lock()
	cancel, ok := s.cancel[id]
	delete(s.cancel, id)
	s.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}
