package agents

import (
	"context"
	"strings"
	"testing"

	"auteur/pkg/harness"
)

// Cosmetic take-suffix renames must count against the same target, while
// genuinely distinct numbered shots must not be collapsed together.
func TestCountTakeFamilies(t *testing.T) {
	tb := &Toolbox{Workspace: t.TempDir()}
	tb.countTake("image", "empty-warm")
	tb.countTake("image", "empty-warm-t3")
	tb.countTake("image", "empty-warm_take4")
	take, run := tb.countTake("image", "empty-warm-v9")
	if take != 4 {
		t.Errorf("empty-warm family take = %d, want 4 (renamed retakes must share a family)", take)
	}
	if run != 4 {
		t.Errorf("run count = %d, want 4", run)
	}

	// Distinct shots stay distinct: shot-01..shot-04 are not retakes.
	for _, id := range []string{"shot-01", "shot-02", "shot-03", "shot-04"} {
		if take, _ := tb.countTake("clip", id); take != 1 {
			t.Errorf("%s take = %d, want 1 (numbered shots are not a take family)", id, take)
		}
	}
}

// A still generation past the per-target budget is refused with guidance,
// before any API call is made.
func TestImageTakeBudgetRefusal(t *testing.T) {
	tb := &Toolbox{Workspace: t.TempDir()}
	for i := 0; i < imageTakeLimit; i++ {
		tb.countTake("image", "empty-warm")
	}
	reg := harness.NewRegistry()
	tb.RegisterImageGenTools(reg)

	_, err := reg.Execute(context.Background(), harness.ToolCall{
		Name:  "generate_image",
		Input: map[string]any{"id": "empty-warm-t6", "prompt": "x"},
	})
	if err == nil {
		t.Fatal("take past budget was not refused")
	}
	for _, want := range []string{"budget", "extract_frame"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err.Error(), want)
		}
	}
}

// A clip render past the per-shot budget fails that shot with guidance
// instead of reaching the render backend.
func TestClipTakeBudgetRefusal(t *testing.T) {
	tb := &Toolbox{Workspace: t.TempDir()}
	for i := 0; i < clipTakeLimit; i++ {
		tb.countTake("clip", "shot1")
	}
	res := tb.generateOneClip(context.Background(), shotInput{ID: "shot1", Prompt: "x", Duration: 5, Mode: "text"})
	if res.Error == "" {
		t.Fatal("take past budget was not refused")
	}
	if !strings.Contains(res.Error, "budget") {
		t.Errorf("refusal %q does not mention the budget", res.Error)
	}
}

// Budgets must survive a process restart: a fresh Ledger over the same
// workspace continues the same accounting.
func TestLedgerPersistence(t *testing.T) {
	dir := t.TempDir()
	l1 := OpenLedger(dir)
	l1.CountTake("clip", "shot1")
	l1.CountTake("clip", "shot1-t2")
	l1.AddRenderTime(90e9) // 90s
	// Both takes produced a clip, so both survive reconciliation.
	l1.RecordRender(RenderRecord{ShotID: "shot1", Path: "clips/shot1.mp4", Take: 1, Duration: 5})
	l1.RecordRender(RenderRecord{ShotID: "shot1-t2", Path: "clips/shot1-t2.mp4", Take: 2, Duration: 5})

	l2 := OpenLedger(dir)
	if take, _ := l2.CountTake("clip", "shot1_take3"); take != 3 {
		t.Errorf("resumed take count = %d, want 3", take)
	}
	if got := l2.AddRenderTime(0); got != 90 {
		t.Errorf("resumed render secs = %v, want 90", got)
	}
	if rs := l2.Renders(); len(rs) != 2 || rs[0].ShotID != "shot1" {
		t.Errorf("renders journal = %+v", rs)
	}
	brief := l2.ResumeBrief()
	if !strings.Contains(brief, "shot1") || !strings.Contains(brief, "CARRY OVER") {
		t.Errorf("resume brief missing content: %q", brief)
	}
}

// A take that produced nothing (killed mid-render) must not consume budget:
// the ledger reconciles against artifacts that actually exist on disk.
func TestLedgerReconcilesPhantomTakes(t *testing.T) {
	dir := t.TempDir()
	l := OpenLedger(dir)
	for i := 0; i < 5; i++ {
		l.CountTake("clip", "s01")
	}
	if take, _ := l.CountTake("clip", "s01"); take != 6 {
		t.Fatalf("setup: take = %d, want 6", take)
	}

	// Reopen with no clips journalled and no stills on disk: budget resets.
	l2 := OpenLedger(dir)
	if take, _ := l2.CountTake("clip", "s01"); take != 1 {
		t.Errorf("phantom takes survived reconciliation: take = %d, want 1", take)
	}

	// A take that DID produce a clip keeps counting.
	l2.RecordRender(RenderRecord{ShotID: "s02", Path: "clips/s02.mp4", Take: 1, Duration: 5})
	l3 := OpenLedger(dir)
	if take, _ := l3.CountTake("clip", "s02-t2"); take != 2 {
		t.Errorf("real take was discarded: take = %d, want 2", take)
	}
}

// Notes handed to a run that dies must reach the next run.
func TestNoteCursorSurvivesInterruptedRun(t *testing.T) {
	dir := t.TempDir()
	l := OpenLedger(dir)
	l.MarkNotesDelivered(1)
	if l.NotesDelivered() != 1 {
		t.Error("in-run cursor should reflect delivery")
	}

	// Process dies before completion: a fresh ledger has not consumed it.
	if l2 := OpenLedger(dir); l2.NotesDelivered() != 0 {
		t.Errorf("note consumed by an interrupted run: cursor = %d, want 0", l2.NotesDelivered())
	}

	// On completion the cursor persists.
	l.CommitNotes()
	if l3 := OpenLedger(dir); l3.NotesDelivered() != 1 {
		t.Errorf("committed cursor lost: %d, want 1", l3.NotesDelivered())
	}
}
