package media

import (
	"strings"
	"testing"
)

func TestBuildVideoGraphJoins(t *testing.T) {
	spec := StitchSpec{
		Width: 640, Height: 480, FPS: 24,
		Clips: []TimelineClip{
			{Path: "a", Duration: 10},                    // opening
			{Path: "b", Duration: 5},                     // hard cut in
			{Path: "c", Duration: 8, TransitionSec: 1.0}, // dissolve in
			{Path: "d", Duration: 4},                     // hard cut in
		},
	}
	graph, last, total := buildVideoGraph(spec)

	if !strings.Contains(graph, "[v0][v1]concat=n=2") {
		t.Error("join 1 should be a hard concat")
	}
	// The dissolve starts 1s before the composed stream's end (10+5-1=14).
	if !strings.Contains(graph, "xfade=transition=fade:duration=1.0000:offset=14.0000") {
		t.Errorf("join 2 xfade wrong: %s", graph)
	}
	if !strings.Contains(graph, "[j2][v3]concat=n=2") {
		t.Error("join 3 should be a hard concat off the xfade output")
	}
	if last != "j3" {
		t.Errorf("last label = %s, want j3", last)
	}
	// 10 + 5 + (8-1) + 4 = 26: the dissolve overlaps one second.
	if total != 26 {
		t.Errorf("total = %v, want 26", total)
	}
}

func TestBuildVideoGraphGlobalCrossfadeFallback(t *testing.T) {
	spec := StitchSpec{
		Width: 640, Height: 480, FPS: 24, CrossfadeSec: 0.5,
		Clips: []TimelineClip{{Path: "a", Duration: 6}, {Path: "b", Duration: 6}},
	}
	graph, _, total := buildVideoGraph(spec)
	if !strings.Contains(graph, "xfade=transition=fade:duration=0.5000:offset=5.5000") {
		t.Errorf("global crossfade not applied: %s", graph)
	}
	if total != 11.5 {
		t.Errorf("total = %v, want 11.5", total)
	}
}
