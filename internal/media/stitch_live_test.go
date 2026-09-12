package media

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

// TestStitchLiveDrift renders a real timeline (STITCH_SPEC=path to a
// StitchSpec JSON) and checks the output runs the requested length to within
// one frame — the cumulative-rounding guarantee. Skipped unless STITCH_SPEC
// is set, since it needs the production's clips on disk.
func TestStitchLiveDrift(t *testing.T) {
	specPath := os.Getenv("STITCH_SPEC")
	if specPath == "" {
		t.Skip("set STITCH_SPEC=<spec.json> to run against real clips")
	}
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	var spec StitchSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	want := 0.0
	for _, c := range spec.Clips {
		want += c.Duration
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := Stitch(ctx, spec); err != nil {
		t.Fatal(err)
	}
	probe, err := Probe(ctx, spec.OutPath)
	if err != nil {
		t.Fatal(err)
	}
	fps := 24.0
	if spec.FPS != 0 {
		fps = float64(spec.FPS)
	}
	drift := probe.Duration - want
	t.Logf("requested %.3fs, rendered %.3fs, drift %+.3fs (%d clips)", want, probe.Duration, drift, len(spec.Clips))
	// Container duration rounds to the audio frame; allow one video frame.
	if math.Abs(drift) > 1/fps {
		t.Fatalf("timeline drift %.3fs exceeds one frame (%.4fs)", drift, 1/fps)
	}
}
