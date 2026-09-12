package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// safeImageExts are formats every downstream consumer handles natively:
// Grok vision, ComfyUI's LoadImage, and ffmpeg crops.
var safeImageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true}

// maxImageEdge caps uploaded pictures; anything larger is downscaled so
// vision calls stay within API limits and renders never pay for pixels the
// pipeline immediately throws away.
const maxImageEdge = 3072

// normalizeImage makes one uploaded picture safe for the whole pipeline.
// Formats outside the safe set (HEIC, AVIF, TIFF, BMP, GIF, ...) convert to
// PNG, EXIF orientation is baked in, and oversized images downscale. Returns
// the possibly-renamed absolute path; a file that cannot be read as an image
// at all is removed and an error returned so the caller can skip it.
func normalizeImage(ctx context.Context, abs string) (string, error) {
	ext := strings.ToLower(filepath.Ext(abs))

	ictx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// [0] pins the first frame of animated/multi-page inputs.
	out, err := exec.CommandContext(ictx, "magick", "identify", "-format", "%w %h", abs+"[0]").Output()
	if err != nil {
		os.Remove(abs)
		return "", fmt.Errorf("%s is not a readable image", filepath.Base(abs))
	}
	var w, h int
	fmt.Sscanf(string(out), "%d %d", &w, &h)

	if safeImageExts[ext] && w <= maxImageEdge && h <= maxImageEdge {
		return abs, nil
	}

	tmp := abs + ".norm.png"
	cctx, cancel2 := context.WithTimeout(ctx, 60*time.Second)
	defer cancel2()
	cmd := exec.CommandContext(cctx, "magick", abs+"[0]",
		"-auto-orient", "-resize", fmt.Sprintf("%dx%d>", maxImageEdge, maxImageEdge), tmp)
	if msg, err := cmd.CombinedOutput(); err != nil {
		os.Remove(tmp)
		if safeImageExts[ext] {
			return abs, nil // usable as-is even though the resize failed
		}
		os.Remove(abs)
		return "", fmt.Errorf("cannot convert %s: %.120s", filepath.Base(abs), msg)
	}
	final := strings.TrimSuffix(abs, ext) + ".png"
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return abs, nil
	}
	if final != abs {
		os.Remove(abs)
	}
	slog.Info("normalized upload", "from", filepath.Base(abs), "to", filepath.Base(final),
		"was", fmt.Sprintf("%dx%d %s", w, h, ext))
	return final, nil
}
