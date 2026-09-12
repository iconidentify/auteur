// h3smoke renders one clip through the local MiniMax H3 renderer, using the
// same backend the studio uses. It is the fastest way to confirm the box is
// ready to shoot before committing a production to it.
//
//	go run ./cmd/h3smoke -seconds 3 -out /tmp/smoke.mp4
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"auteur/internal/agents"
	"auteur/pkg/comfy"
)

func main() {
	url := flag.String("url", envOr("AUTEUR_H3_URL", "http://127.0.0.1:8189"), "ComfyUI base URL")
	prompt := flag.String("prompt", "A lone figure walks along a rain-slicked city street at night, "+
		"neon signs reflected in the puddles. Slow dolly follow at chest height, shallow depth of "+
		"field, cyan and magenta grade, 35mm anamorphic. Rain on metal, distant traffic.", "shot prompt")
	seconds := flag.Int("seconds", 3, "clip duration in seconds")
	res := flag.String("resolution", "480p", "480p or 720p")
	aspect := flag.String("aspect", "16:9", "aspect ratio")
	image := flag.String("image", "", "optional still to animate (mode 'image')")
	endImage := flag.String("end-image", "", "optional still to land on")
	refs := flag.String("refs", "", "comma-separated reference images (mode 'reference', REF2VA); bind in prompt as <Picture 1>..")
	syncAudio := flag.String("sync-audio", "", "audio file to lip-sync to (forces mode 'reference')")
	steps := flag.Int("steps", 8, "sampling steps")
	seed := flag.Int64("seed", 0, "noise seed (0 = random)")
	out := flag.String("out", "h3smoke.mp4", "output path")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var urls []string
	for _, u := range strings.Split(*url, ",") {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	pool := comfy.NewPool(urls)
	nodes, err := pool.Ping(ctx)
	if err != nil {
		fatal(fmt.Sprintf("cannot reach ComfyUI at %s: %v\n  start it with: sudo systemctl start comfyui-h3", *url, err))
	}
	for _, n := range nodes {
		fmt.Println("node:", n)
	}

	backend := &agents.H3Backend{Pool: pool, Production: "smoke", Steps: *steps}
	mode := "text"
	if *image != "" {
		mode = "image"
	}
	var refPaths []string
	if *refs != "" {
		mode = "reference"
		for _, r := range strings.Split(*refs, ",") {
			refPaths = append(refPaths, abs(strings.TrimSpace(r)))
		}
	}
	if *syncAudio != "" {
		mode = "reference"
	}
	absOut, _ := filepath.Abs(*out)

	req := agents.ClipRequest{
		ID:             "smoke",
		Prompt:         *prompt,
		Seconds:        *seconds,
		Mode:           mode,
		ImagePath:      abs(*image),
		EndImagePath:   abs(*endImage),
		ReferencePaths: refPaths,
		SyncAudioPath:  abs(*syncAudio),
		AspectRatio:    *aspect,
		Resolution:     *res,
		Seed:           *seed,
		DestPath:       absOut,
	}

	w, h := comfy.H3Dimensions(*aspect, *res)
	frames := comfy.FramesForSeconds(float64(*seconds))
	fmt.Printf("rendering %dx%d, %d frames (%.2fs), %d steps, mode %s\n",
		w, h, frames, comfy.SecondsForFrames(frames), *steps, mode)

	start := time.Now()
	result, err := backend.Generate(ctx, req, func(elapsed time.Duration, state string) {
		fmt.Printf("  [%4.0fs] %s\n", elapsed.Seconds(), state)
	})
	if err != nil {
		fatal(err.Error())
	}
	wall := time.Since(start)
	fmt.Printf("\ndone in %.1fs (%.1f min) -> %s\n", wall.Seconds(), wall.Minutes(), absOut)
	fmt.Printf("clip: %.2fs video | %s\n", result.Duration, result.Note)
	fmt.Printf("cost: %.1fs of render per second of video\n", wall.Seconds()/result.Duration)
}

func abs(p string) string {
	if p == "" {
		return ""
	}
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "h3smoke: "+msg)
	os.Exit(1)
}
