package studio

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"auteur/pkg/harness"
)

// EventHub persists harness events to a JSONL file and fans them out to
// live subscribers (SSE connections). Late subscribers replay the log
// first, so a reloaded browser reconstructs the full session.
type EventHub struct {
	path string

	mu   sync.Mutex
	file *os.File
	seq  int64
	subs map[chan SeqEvent]struct{}
}

// SeqEvent wraps a harness event with a monotonically increasing sequence
// number used for SSE resume (Last-Event-ID).
type SeqEvent struct {
	Seq   int64         `json:"seq"`
	Event harness.Event `json:"event"`
}

func NewEventHub(path string) *EventHub {
	h := &EventHub{path: path, subs: map[chan SeqEvent]struct{}{}}
	// Recover the sequence counter from an existing log.
	if events, err := readLog(path); err == nil && len(events) > 0 {
		h.seq = events[len(events)-1].Seq
	}
	return h
}

// Emit implements harness.EventSink.
func (h *EventHub) Emit(ctx context.Context, e harness.Event) {
	h.mu.Lock()
	h.seq++
	se := SeqEvent{Seq: h.seq, Event: e}
	if h.file == nil {
		f, err := os.OpenFile(h.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			h.file = f
		}
	}
	if h.file != nil {
		if data, err := json.Marshal(se); err == nil {
			h.file.Write(append(data, '\n'))
		}
	}
	for ch := range h.subs {
		select {
		case ch <- se:
		default: // drop for slow subscribers rather than stall the loop
		}
	}
	h.mu.Unlock()
}

// Subscribe returns a channel of live events plus the replay of everything
// after seq (0 replays the full history). Call the returned cancel func to
// unsubscribe.
func (h *EventHub) Subscribe(afterSeq int64) (replay []SeqEvent, live chan SeqEvent, cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	all, _ := readLog(h.path)
	for _, se := range all {
		if se.Seq > afterSeq {
			replay = append(replay, se)
		}
	}
	live = make(chan SeqEvent, 256)
	h.subs[live] = struct{}{}
	cancel = func() {
		h.mu.Lock()
		delete(h.subs, live)
		h.mu.Unlock()
	}
	return replay, live, cancel
}

// ReadSince returns persisted events with sequence numbers greater than
// seq. It re-reads the log file, so it also surfaces events appended by a
// different process sharing the workspace.
func (h *EventHub) ReadSince(seq int64) []SeqEvent {
	all, _ := readLog(h.path)
	var out []SeqEvent
	for _, se := range all {
		if se.Seq > seq {
			out = append(out, se)
		}
	}
	return out
}

// Close flushes and closes the underlying log file.
func (h *EventHub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.file != nil {
		h.file.Close()
		h.file = nil
	}
}

func readLog(path string) ([]SeqEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []SeqEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var se SeqEvent
		if err := json.Unmarshal(sc.Bytes(), &se); err == nil {
			out = append(out, se)
		}
	}
	return out, sc.Err()
}

// CoalesceReplay compresses a replay for transport: consecutive streaming
// delta events from the same agent merge into one event carrying the
// concatenated text and the LAST sequence number of the run, so Last-Event-ID
// resume still lands correctly. Live streaming is untouched — this exists
// because a mature production's log is dominated by single-token deltas
// (typically ~90% of events), and replaying them one at a time makes the UI
// crawl on connect.
func CoalesceReplay(events []SeqEvent) []SeqEvent {
	out := make([]SeqEvent, 0, len(events))
	for _, se := range events {
		t := se.Event.Type
		if (t == "reasoning_delta" || t == "text_delta") && len(out) > 0 {
			prev := &out[len(out)-1]
			if prev.Event.Type == t && prev.Event.AgentID == se.Event.AgentID {
				prev.Event.Message += se.Event.Message
				prev.Seq = se.Seq
				continue
			}
		}
		out = append(out, se)
	}
	return out
}

// Rotate archives the current event log and starts a fresh one, so each run
// replays quickly instead of dragging every predecessor behind it. Sequence
// numbers keep climbing across rotations, preserving Last-Event-ID resume.
func (h *EventHub) Rotate() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.file != nil {
		h.file.Close()
		h.file = nil
	}
	if _, err := os.Stat(h.path); err == nil {
		os.Rename(h.path, fmt.Sprintf("%s.%d", h.path, time.Now().Unix()))
	}
}
