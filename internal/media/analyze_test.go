package media

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

// makeBeatTrack synthesizes a short 120 BPM pulse track with ffmpeg.
func makeBeatTrack(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	path := filepath.Join(t.TempDir(), "beat.wav")
	cmd := exec.Command("ffmpeg", "-y", "-v", "error",
		"-f", "lavfi", "-i", "sine=frequency=80:duration=20",
		"-af", "volume='0.9*lt(mod(t\\,0.5)\\,0.1)':eval=frame",
		path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("synthesize: %v: %s", err, out)
	}
	return path
}

func TestAnalyzeAudioTempoAndStructure(t *testing.T) {
	path := makeBeatTrack(t)
	a, err := AnalyzeAudio(context.Background(), path)
	if err != nil {
		t.Fatalf("AnalyzeAudio: %v", err)
	}
	if a.Duration < 19.5 || a.Duration > 20.5 {
		t.Errorf("duration = %.2f, want ~20", a.Duration)
	}
	// 120 BPM pulse; allow the octave-folded neighborhood.
	if a.Tempo < 110 || a.Tempo > 130 {
		t.Errorf("tempo = %.1f, want ~120", a.Tempo)
	}
	if len(a.EnergyCurve) < 19 {
		t.Errorf("energy curve has %d entries, want ~20", len(a.EnergyCurve))
	}
	if len(a.Sections) == 0 {
		t.Error("no sections detected")
	}
}

func TestProbe(t *testing.T) {
	path := makeBeatTrack(t)
	p, err := Probe(context.Background(), path)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if p.Duration < 19.5 || p.Duration > 20.5 {
		t.Errorf("duration = %.2f", p.Duration)
	}
	if len(p.Streams) != 1 || p.Streams[0].CodecType != "audio" {
		t.Errorf("streams = %+v", p.Streams)
	}
}
