package media

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ConcatAudio joins audio files end to end into one 44.1kHz stereo WAV. Inputs
// may differ in format or sample rate; each is resampled on the way in. This
// is how a film's per-shot narration and dialogue become one continuous track
// that the timeline is cut against.
func ConcatAudio(ctx context.Context, inputs []string, out string) error {
	if len(inputs) == 0 {
		return fmt.Errorf("no audio inputs to concatenate")
	}
	args := []string{"-y", "-v", "error"}
	for _, in := range inputs {
		args = append(args, "-i", in)
	}
	var fg strings.Builder
	for i := range inputs {
		fmt.Fprintf(&fg, "[%d:a:0]aformat=sample_rates=44100:channel_layouts=stereo,asetpts=PTS-STARTPTS[a%d];", i, i)
	}
	for i := range inputs {
		fmt.Fprintf(&fg, "[a%d]", i)
	}
	fmt.Fprintf(&fg, "concat=n=%d:v=0:a=1[aout]", len(inputs))
	args = append(args,
		"-filter_complex", fg.String(),
		"-map", "[aout]",
		"-c:a", "pcm_s16le", "-ar", "44100", "-ac", "2",
		out,
	)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if o, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg concat audio: %w: %s", err, truncate(string(o), 1500))
	}
	return nil
}

// ToWAV transcodes any audio file to a 44.1kHz stereo WAV, the studio's
// working format for slicing, syncing, and concatenation.
// PadAudio writes in to out as WAV, appending silence so the result is at
// least minSeconds long. Audio already that long is copied unchanged.
// MiniMax rejects reference audio shorter than 2s, and one-second lines
// ("Let there be light:") are common in dialogue.
func PadAudio(ctx context.Context, in, out string, minSeconds float64) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-v", "error", "-i", in,
		"-af", fmt.Sprintf("apad=whole_dur=%.2f", minSeconds),
		"-ar", "44100", "-ac", "2", "-c:a", "pcm_s16le", out)
	if outb, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg pad: %w: %s", err, strings.TrimSpace(string(outb)))
	}
	return nil
}

func ToWAV(ctx context.Context, in, out string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-v", "error", "-i", in,
		"-c:a", "pcm_s16le", "-ar", "44100", "-ac", "2", out)
	if o, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg to wav: %w: %s", err, truncate(string(o), 1500))
	}
	return nil
}
