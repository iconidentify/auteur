package agents

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"auteur/pkg/minimax"
)

// TestMiniMaxLiveSmoke exercises the hosted MiniMax backend end to end: submit a
// short text-to-video, poll it to completion, and download the mp4. It hits the
// real (paid) API, so it is gated behind MINIMAX_LIVE=1 and never runs in a
// normal `go test ./...`.
//
//	MINIMAX_LIVE=1 MINIMAX_API_KEY=... go test ./internal/agents -run MiniMaxLiveSmoke -v -timeout 15m
func TestMiniMaxLiveSmoke(t *testing.T) {
	if os.Getenv("MINIMAX_LIVE") != "1" {
		t.Skip("set MINIMAX_LIVE=1 (with MINIMAX_API_KEY) to run the live MiniMax smoke test")
	}
	key := os.Getenv("MINIMAX_API_KEY")
	if key == "" {
		t.Skip("MINIMAX_API_KEY not set")
	}

	b := &MiniMaxBackend{Client: minimax.NewClient(key), Production: "smoke"}
	dest := filepath.Join(t.TempDir(), "smoke.mp4")

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	out, err := b.Generate(ctx, ClipRequest{
		ID:          "smoke",
		Prompt:      "a calm ocean wave at golden hour, cinematic slow motion",
		Seconds:     6,
		Mode:        "text",
		AspectRatio: "16:9",
		Resolution:  "768p",
		DestPath:    dest,
	}, func(el time.Duration, state string) { t.Logf("[%s] %s", el.Round(time.Second), state) })
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if fi.Size() < 1024 {
		t.Fatalf("output mp4 is suspiciously small: %d bytes", fi.Size())
	}
	t.Logf("OK: wrote %s (%d bytes), duration %.1fs, note=%q", dest, fi.Size(), out.Duration, out.Note)
}
