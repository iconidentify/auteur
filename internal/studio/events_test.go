package studio

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"auteur/pkg/harness"
)

func TestEventHubPersistReplayAndLive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	hub := NewEventHub(path)

	ctx := context.Background()
	hub.Emit(ctx, harness.Event{Type: "agent_start", AgentName: "director"})
	hub.Emit(ctx, harness.Event{Type: "text", Message: "hello"})

	// Replay from the beginning.
	replay, live, cancel := hub.Subscribe(0)
	defer cancel()
	if len(replay) != 2 {
		t.Fatalf("replay = %d events, want 2", len(replay))
	}
	if replay[0].Seq != 1 || replay[1].Seq != 2 {
		t.Errorf("bad sequence numbers: %d, %d", replay[0].Seq, replay[1].Seq)
	}

	// Live delivery.
	hub.Emit(ctx, harness.Event{Type: "complete"})
	select {
	case se := <-live:
		if se.Event.Type != "complete" || se.Seq != 3 {
			t.Errorf("live event = %+v", se)
		}
	case <-time.After(time.Second):
		t.Fatal("live event not delivered")
	}

	// Resume from mid-log.
	replay2, _, cancel2 := hub.Subscribe(2)
	defer cancel2()
	if len(replay2) != 1 || replay2[0].Event.Type != "complete" {
		t.Errorf("resume replay = %+v", replay2)
	}

	// A new hub over the same file recovers the sequence counter.
	hub.Close()
	hub2 := NewEventHub(path)
	hub2.Emit(ctx, harness.Event{Type: "text", Message: "after restart"})
	replay3, _, cancel3 := hub2.Subscribe(0)
	defer cancel3()
	if len(replay3) != 4 || replay3[3].Seq != 4 {
		t.Errorf("sequence not recovered across restart: %+v", replay3)
	}
}

func TestStudioCreateLoadList(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Create("Test Film", "make it great")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Title != "Test Film" || loaded.Status != StatusDraft {
		t.Errorf("loaded = %+v", loaded)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 {
		t.Errorf("list = %v, %v", list, err)
	}
}

func TestRecoverStale(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	p, _ := s.Create("Stuck", "")
	p.Status = StatusRunning
	s.Save(p)

	// A fresh studio over the same root must fail the stuck production.
	s2, _ := New(dir)
	loaded, err := s2.Load(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != StatusFailed {
		t.Errorf("status = %s, want failed", loaded.Status)
	}
}
