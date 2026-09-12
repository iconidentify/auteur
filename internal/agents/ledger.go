package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Ledger is a production's durable memory of spend: take counts per target,
// cumulative GPU time, rendered clips, and how many producer notes have been
// delivered. It lives at state/ledger.json in the workspace so budgets and
// history survive process restarts — a resumed run continues the same
// production instead of starting a fresh accounting.
type Ledger struct {
	dir string
	mu  sync.Mutex
	d   ledgerData
	// pendingNotes is the note cursor for the current run, persisted only
	// when the production completes (see CommitNotes).
	pendingNotes int
}

type ledgerData struct {
	Takes          map[string]int `json:"takes"`
	RenderSecs     float64        `json:"render_secs"`
	NotesDelivered int            `json:"notes_delivered"`
}

// RenderRecord is one rendered clip, journaled to state/renders.jsonl.
type RenderRecord struct {
	Time     time.Time `json:"time"`
	ShotID   string    `json:"shot_id"`
	Path     string    `json:"path"`
	Take     int       `json:"take"`
	Duration float64   `json:"duration_seconds"`
	Render   string    `json:"render"`
}

// OpenLedger loads (or initializes) the ledger for a workspace and
// reconciles it against what is actually on disk.
func OpenLedger(workspace string) *Ledger {
	l := &Ledger{dir: filepath.Join(workspace, "state"), d: ledgerData{Takes: map[string]int{}}}
	if data, err := os.ReadFile(filepath.Join(l.dir, "ledger.json")); err == nil {
		var d ledgerData
		if json.Unmarshal(data, &d) == nil {
			if d.Takes == nil {
				d.Takes = map[string]int{}
			}
			l.d = d
		}
	}
	l.reconcile(workspace)
	return l
}

// reconcile rebuilds take counts from artifacts that actually exist. A take
// only counts if it produced something: a render killed mid-flight (crashed
// box, restarted service) leaves no clip and no journal entry, and must not
// consume the shot's budget — otherwise a few interruptions can lock a shot
// out of the shoot entirely.
func (l *Ledger) reconcile(workspace string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	counts := map[string]int{}
	for _, r := range l.rendersLocked() {
		counts["clip:"+family(r.ShotID)]++
		counts["clip"]++
	}
	// Stills are not journalled; count the files they leave in frames/.
	if entries, err := os.ReadDir(filepath.Join(workspace, "frames")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if ext != ".png" && ext != ".jpg" && ext != ".jpeg" {
				continue
			}
			base := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
			counts["image:"+family(base)]++
			counts["image"]++
		}
	}

	changed := false
	for k, was := range l.d.Takes {
		if !strings.HasPrefix(k, "clip") && !strings.HasPrefix(k, "image") {
			continue
		}
		if now := counts[k]; now != was {
			changed = true
		}
	}
	for k, now := range counts {
		if l.d.Takes[k] != now {
			changed = true
		}
	}
	if !changed {
		return
	}
	next := map[string]int{}
	for k, v := range l.d.Takes {
		if !strings.HasPrefix(k, "clip") && !strings.HasPrefix(k, "image") {
			next[k] = v
		}
	}
	for k, v := range counts {
		next[k] = v
	}
	l.d.Takes = next
	l.save()
}

// family is the take-family key for an artifact id (retake suffixes stripped).
func family(id string) string {
	return takeMarker.ReplaceAllString(strings.ToLower(sanitizeSlug(id)), "")
}

// save writes atomically; called with the mutex held.
func (l *Ledger) save() {
	os.MkdirAll(l.dir, 0o755)
	data, err := json.MarshalIndent(l.d, "", "  ")
	if err != nil {
		return
	}
	tmp := filepath.Join(l.dir, ".ledger.json.tmp")
	if os.WriteFile(tmp, data, 0o644) == nil {
		os.Rename(tmp, filepath.Join(l.dir, "ledger.json"))
	}
}

// CountTake records one generation attempt and returns the per-target and
// per-run-kind counts.
func (l *Ledger) CountTake(kind, id string) (target, run int) {
	family := kind + ":" + takeMarker.ReplaceAllString(strings.ToLower(sanitizeSlug(id)), "")
	l.mu.Lock()
	defer l.mu.Unlock()
	l.d.Takes[family]++
	l.d.Takes[kind]++
	l.save()
	return l.d.Takes[family], l.d.Takes[kind]
}

// RefundTake returns one attempt to the budget (infra failures).
func (l *Ledger) RefundTake(kind, id string) {
	family := kind + ":" + takeMarker.ReplaceAllString(strings.ToLower(sanitizeSlug(id)), "")
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.d.Takes[family] > 0 {
		l.d.Takes[family]--
	}
	if l.d.Takes[kind] > 0 {
		l.d.Takes[kind]--
	}
	l.save()
}

// AddRenderTime accumulates render wall-clock and returns the total seconds.
func (l *Ledger) AddRenderTime(d time.Duration) float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.d.RenderSecs += d.Seconds()
	l.save()
	return l.d.RenderSecs
}

// RecordRender journals one finished clip.
func (l *Ledger) RecordRender(r RenderRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	os.MkdirAll(l.dir, 0o755)
	f, err := os.OpenFile(filepath.Join(l.dir, "renders.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if data, err := json.Marshal(r); err == nil {
		f.Write(append(data, '\n'))
	}
}

// Renders returns the journaled clips.
func (l *Ledger) Renders() []RenderRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rendersLocked()
}

func (l *Ledger) rendersLocked() []RenderRecord {
	data, err := os.ReadFile(filepath.Join(l.dir, "renders.jsonl"))
	if err != nil {
		return nil
	}
	var out []RenderRecord
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var r RenderRecord
		if json.Unmarshal([]byte(line), &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

// NotesDelivered / MarkNotesDelivered track the producer-note cursor. The
// cursor advances in memory during a run and is only persisted when the
// production completes: a note handed to a run that then dies must reach the
// next run, or the producer's direction is silently swallowed by a crash.
func (l *Ledger) NotesDelivered() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pendingNotes > l.d.NotesDelivered {
		return l.pendingNotes
	}
	return l.d.NotesDelivered
}

func (l *Ledger) MarkNotesDelivered(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n > l.pendingNotes {
		l.pendingNotes = n
	}
}

// CommitNotes persists the in-memory note cursor; call it only when the
// production actually completed.
func (l *Ledger) CommitNotes() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pendingNotes > l.d.NotesDelivered {
		l.d.NotesDelivered = l.pendingNotes
		l.save()
	}
}

// ResumeBrief summarizes prior spend for a restarted run's kickoff; empty
// when the production has no history worth mentioning.
func (l *Ledger) ResumeBrief() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	renders := l.rendersLocked()
	if len(renders) == 0 && l.d.RenderSecs == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## RESUMED PRODUCTION — prior work on disk\n")
	fmt.Fprintf(&b, "This production was interrupted and restarted. %.1f GPU minutes are already "+
		"spent and take budgets CARRY OVER (they do not reset). Rendered clips on disk:\n",
		l.d.RenderSecs/60)
	for _, r := range renders {
		fmt.Fprintf(&b, "- %s (take %d): %s, %.1fs — %s\n", r.ShotID, r.Take, r.Path, r.Duration, r.Render)
	}
	b.WriteString("Audit existing clips and analysis documents against the brief and REUSE " +
		"everything that passes; re-shoot only what fails. Do not re-run analysis that exists.\n")
	return b.String()
}
