package server

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mk(t *testing.T, path string, spec ...string) {
	t.Helper()
	args := append(spec, path)
	if out, err := exec.Command("magick", args...).CombinedOutput(); err != nil {
		t.Skipf("magick unavailable: %v %s", err, out)
	}
}

func TestNormalizeImage(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// A small webp is already safe: untouched, same path.
	webp := filepath.Join(dir, "ref-01.webp")
	mk(t, webp, "-size", "640x480", "xc:orange")
	if got, err := normalizeImage(ctx, webp); err != nil || got != webp {
		t.Errorf("small webp: got %q err %v, want untouched", got, err)
	}

	// A HEIC converts to PNG.
	heic := filepath.Join(dir, "ref-02.heic")
	mk(t, heic, "-size", "800x600", "xc:teal")
	got, err := normalizeImage(ctx, heic)
	if err != nil || !strings.HasSuffix(got, "ref-02.png") {
		t.Errorf("heic: got %q err %v, want ref-02.png", got, err)
	}

	// An oversized jpeg downscales in place, keeping png name.
	big := filepath.Join(dir, "ref-03.jpg")
	mk(t, big, "-size", "5000x2000", "xc:gray")
	got, err = normalizeImage(ctx, big)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command("magick", "identify", "-format", "%w", got).Output()
	if string(out) != "3072" {
		t.Errorf("oversize: width %s, want 3072", out)
	}

	// Garbage is rejected, not passed through.
	junk := filepath.Join(dir, "ref-04.png")
	if err := exec.Command("sh", "-c", "echo notanimage > "+junk).Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeImage(ctx, junk); err == nil {
		t.Error("garbage file accepted")
	}
}
