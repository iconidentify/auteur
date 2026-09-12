package studio

import (
	"context"
	"path/filepath"
	"testing"

	"auteur/pkg/harness"
)

func ev(seq int64, typ, agent, msg string) SeqEvent {
	return SeqEvent{Seq: seq, Event: harness.Event{Type: typ, AgentID: agent, Message: msg}}
}

func TestCoalesceReplay(t *testing.T) {
	in := []SeqEvent{
		ev(1, "agent_start", "a", "go"),
		ev(2, "reasoning_delta", "a", "th"),
		ev(3, "reasoning_delta", "a", "ink"),
		ev(4, "reasoning_delta", "b", "other"), // different agent: no merge
		ev(5, "text_delta", "a", "he"),         // different type: no merge
		ev(6, "text_delta", "a", "llo"),
		ev(7, "tool_call", "a", "x"),
		ev(8, "text_delta", "a", "again"), // separated by tool_call: new run
	}
	out := CoalesceReplay(in)
	if len(out) != 6 {
		t.Fatalf("got %d events, want 6: %+v", len(out), out)
	}
	if out[1].Event.Message != "think" || out[1].Seq != 3 {
		t.Errorf("merged reasoning = %q seq %d, want %q seq 3", out[1].Event.Message, out[1].Seq, "think")
	}
	if out[3].Event.Message != "hello" || out[3].Seq != 6 {
		t.Errorf("merged text = %q seq %d, want %q seq 6", out[3].Event.Message, out[3].Seq, "hello")
	}
	if out[5].Event.Message != "again" {
		t.Errorf("post-tool delta = %q, want isolated 'again'", out[5].Event.Message)
	}
	// Resume correctness: every original seq must be <= some replayed seq run end.
	if out[len(out)-1].Seq != 8 {
		t.Errorf("last seq = %d, want 8", out[len(out)-1].Seq)
	}
}

// Rotation archives the log and keeps sequence numbers climbing, so a
// restarted run replays fast without breaking Last-Event-ID resume.
func TestEventHubRotate(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/events.jsonl"
	h := NewEventHub(path)
	h.Emit(context.Background(), harness.Event{Type: "text", Message: "run1"})
	h.Emit(context.Background(), harness.Event{Type: "text", Message: "run1b"})
	h.Rotate()
	h.Emit(context.Background(), harness.Event{Type: "text", Message: "run2"})

	replay, _, cancel := h.Subscribe(0)
	defer cancel()
	if len(replay) != 1 || replay[0].Event.Message != "run2" {
		t.Errorf("replay after rotate = %+v, want just run2", replay)
	}
	if replay[0].Seq != 3 {
		t.Errorf("seq after rotate = %d, want 3 (monotonic across runs)", replay[0].Seq)
	}
	archives, _ := filepath.Glob(path + ".*")
	if len(archives) != 1 {
		t.Errorf("want 1 archived segment, got %v", archives)
	}
}
