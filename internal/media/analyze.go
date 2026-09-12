// Package media wraps ffmpeg/ffprobe for audio analysis and video assembly.
package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/cmplx"
	"os/exec"
)

const (
	analysisRate = 22050 // Hz, mono
	frameSize    = 2048
	hopSize      = 512
)

// AudioAnalysis is the structured result of analyzing a music track. It is
// designed to be handed to an LLM: compact, quantized, and self-describing.
type AudioAnalysis struct {
	Path         string    `json:"path"`
	Duration     float64   `json:"duration_seconds"`
	Tempo        float64   `json:"tempo_bpm"`
	BeatInterval float64   `json:"beat_interval_seconds"`
	FirstBeat    float64   `json:"first_beat_offset_seconds"`
	Sections     []Section `json:"sections"`
	// EnergyCurve is the normalized (0-100) loudness sampled once per second.
	EnergyCurve []int `json:"energy_curve_per_second"`
}

// Section is a contiguous span of the track with a coherent energy character.
type Section struct {
	Start   float64 `json:"start_seconds"`
	End     float64 `json:"end_seconds"`
	Label   string  `json:"label"`  // intro | build | peak | breakdown | outro | steady
	Energy  int     `json:"energy"` // 0-100 mean energy
	Beats   int     `json:"approx_beats"`
	Comment string  `json:"comment,omitempty"`
}

// AnalyzeAudio decodes the file with ffmpeg and computes duration, tempo, a
// beat grid, an energy curve, and coarse structural sections.
func AnalyzeAudio(ctx context.Context, path string) (*AudioAnalysis, error) {
	samples, err := decodePCM(ctx, path)
	if err != nil {
		return nil, err
	}
	if len(samples) < analysisRate {
		return nil, fmt.Errorf("audio too short to analyze (%d samples)", len(samples))
	}
	dur := float64(len(samples)) / analysisRate

	onset := onsetEnvelope(samples)
	hopDur := float64(hopSize) / analysisRate

	tempo, beatInterval := estimateTempo(onset, hopDur)
	firstBeat := estimateFirstBeat(onset, beatInterval, hopDur)

	energy := energyPerSecond(samples)
	sections := segment(energy, beatInterval)

	return &AudioAnalysis{
		Path:         path,
		Duration:     round2(dur),
		Tempo:        round2(tempo),
		BeatInterval: round4(beatInterval),
		FirstBeat:    round4(firstBeat),
		Sections:     sections,
		EnergyCurve:  energy,
	}, nil
}

// decodePCM extracts mono float samples at analysisRate via ffmpeg.
func decodePCM(ctx context.Context, path string) ([]float64, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-i", path,
		"-ac", "1", "-ar", fmt.Sprint(analysisRate), "-f", "s16le", "-")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg decode: %w: %s", err, errb.String())
	}
	raw := out.Bytes()
	n := len(raw) / 2
	samples := make([]float64, n)
	for i := 0; i < n; i++ {
		v := int16(binary.LittleEndian.Uint16(raw[i*2:]))
		samples[i] = float64(v) / 32768.0
	}
	return samples, nil
}

// onsetEnvelope computes a spectral-flux onset strength curve, one value per hop.
func onsetEnvelope(samples []float64) []float64 {
	nFrames := (len(samples) - frameSize) / hopSize
	if nFrames < 1 {
		return nil
	}
	prevMag := make([]float64, frameSize/2)
	env := make([]float64, nFrames)
	window := hannWindow(frameSize)
	buf := make([]complex128, frameSize)
	for f := 0; f < nFrames; f++ {
		off := f * hopSize
		for i := 0; i < frameSize; i++ {
			buf[i] = complex(samples[off+i]*window[i], 0)
		}
		fft(buf)
		flux := 0.0
		for i := 0; i < frameSize/2; i++ {
			m := cmplx.Abs(buf[i])
			d := m - prevMag[i]
			if d > 0 {
				flux += d
			}
			prevMag[i] = m
		}
		env[f] = flux
	}
	// Normalize and lightly smooth.
	max := 0.0
	for _, v := range env {
		if v > max {
			max = v
		}
	}
	if max > 0 {
		for i := range env {
			env[i] /= max
		}
	}
	return env
}

func hannWindow(n int) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(n-1)))
	}
	return w
}

// fft is an in-place iterative radix-2 Cooley-Tukey transform.
func fft(a []complex128) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for length := 2; length <= n; length <<= 1 {
		ang := -2 * math.Pi / float64(length)
		wl := cmplx.Rect(1, ang)
		for i := 0; i < n; i += length {
			w := complex(1, 0)
			for j := 0; j < length/2; j++ {
				u := a[i+j]
				v := a[i+j+length/2] * w
				a[i+j] = u + v
				a[i+j+length/2] = u - v
				w *= wl
			}
		}
	}
}

// estimateTempo autocorrelates the onset envelope over 60-180 BPM.
func estimateTempo(onset []float64, hopDur float64) (bpm, interval float64) {
	if len(onset) == 0 {
		return 120, 0.5
	}
	minLag := int(60.0 / 180.0 / hopDur) // 180 BPM
	maxLag := int(60.0 / 60.0 / hopDur)  // 60 BPM
	if maxLag >= len(onset) {
		maxLag = len(onset) - 1
	}
	score := func(lag int) float64 {
		if lag < 1 || lag >= len(onset) {
			return 0
		}
		s := 0.0
		for i := 0; i+lag < len(onset); i++ {
			s += onset[i] * onset[i+lag]
		}
		return s / float64(len(onset)-lag)
	}
	bestLag, bestScore := minLag, -1.0
	for lag := minLag; lag <= maxLag; lag++ {
		s := score(lag)
		// Slight preference for the 90-150 BPM range typical of most music.
		center := 60.0 / (float64(lag) * hopDur)
		weight := 1.0 - 0.15*math.Abs(math.Log2(center/120.0))
		s *= weight
		if s > bestScore {
			bestScore, bestLag = s, lag
		}
	}
	// Octave folding: a perfectly periodic beat scores equally at 2x the lag,
	// so prefer the doubled tempo whenever it holds up, until we reach 90 BPM.
	for 60.0/(float64(bestLag)*hopDur) < 90 {
		half := bestLag / 2
		if half < minLag || score(half) < 0.7*score(bestLag) {
			break
		}
		bestLag = half
	}
	// Parabolic interpolation around the peak for sub-lag precision; lag
	// quantization alone is a ~2% tempo error that drifts over minutes.
	refined := float64(bestLag)
	s0, s1, s2 := score(bestLag-1), score(bestLag), score(bestLag+1)
	if denom := s0 - 2*s1 + s2; denom != 0 {
		delta := 0.5 * (s0 - s2) / denom
		if delta > -1 && delta < 1 {
			refined += delta
		}
	}
	interval = refined * hopDur
	bpm = 60.0 / interval
	return bpm, interval
}

// estimateFirstBeat finds the phase offset that best aligns a beat comb with
// the onset envelope.
func estimateFirstBeat(onset []float64, beatInterval, hopDur float64) float64 {
	if len(onset) == 0 || beatInterval <= 0 {
		return 0
	}
	lag := int(beatInterval / hopDur)
	if lag < 1 {
		return 0
	}
	bestPhase, bestScore := 0, -1.0
	for phase := 0; phase < lag; phase++ {
		s := 0.0
		for i := phase; i < len(onset); i += lag {
			s += onset[i]
		}
		if s > bestScore {
			bestScore, bestPhase = s, phase
		}
	}
	return float64(bestPhase) * hopDur
}

// energyPerSecond returns RMS loudness per second scaled 0-100.
func energyPerSecond(samples []float64) []int {
	secs := len(samples) / analysisRate
	if secs < 1 {
		secs = 1
	}
	out := make([]float64, 0, secs)
	for s := 0; s < secs; s++ {
		start := s * analysisRate
		end := start + analysisRate
		if end > len(samples) {
			end = len(samples)
		}
		sum := 0.0
		for i := start; i < end; i++ {
			sum += samples[i] * samples[i]
		}
		out = append(out, math.Sqrt(sum/float64(end-start)))
	}
	max := 0.0
	for _, v := range out {
		if v > max {
			max = v
		}
	}
	res := make([]int, len(out))
	if max > 0 {
		for i, v := range out {
			res[i] = int(v / max * 100)
		}
	}
	return res
}

// segment slices the energy curve into coarse sections labeled by shape.
func segment(energy []int, beatInterval float64) []Section {
	n := len(energy)
	if n == 0 {
		return nil
	}
	// Smooth with a 5s moving average.
	smooth := make([]float64, n)
	for i := range energy {
		lo, hi := i-2, i+3
		if lo < 0 {
			lo = 0
		}
		if hi > n {
			hi = n
		}
		s := 0
		for j := lo; j < hi; j++ {
			s += energy[j]
		}
		smooth[i] = float64(s) / float64(hi-lo)
	}
	// Boundaries where the smoothed curve moves by a meaningful step.
	var bounds []int
	last := smooth[0]
	for i := 4; i < n; i++ {
		if math.Abs(smooth[i]-last) > 14 && (len(bounds) == 0 || i-bounds[len(bounds)-1] >= 8) {
			bounds = append(bounds, i)
			last = smooth[i]
		}
	}
	edges := append([]int{0}, bounds...)
	edges = append(edges, n)

	var sections []Section
	for i := 0; i+1 < len(edges); i++ {
		start, end := edges[i], edges[i+1]
		if end-start < 3 && len(sections) > 0 {
			sections[len(sections)-1].End = float64(end)
			continue
		}
		mean := 0.0
		for j := start; j < end; j++ {
			mean += smooth[j]
		}
		mean /= float64(end - start)
		sections = append(sections, Section{
			Start:  float64(start),
			End:    float64(end),
			Energy: int(mean),
		})
	}
	labelSections(sections)
	for i := range sections {
		if beatInterval > 0 {
			sections[i].Beats = int((sections[i].End - sections[i].Start) / beatInterval)
		}
	}
	return sections
}

func labelSections(secs []Section) {
	if len(secs) == 0 {
		return
	}
	maxE := 0
	for _, s := range secs {
		if s.Energy > maxE {
			maxE = s.Energy
		}
	}
	for i := range secs {
		s := &secs[i]
		rel := 0.0
		if maxE > 0 {
			rel = float64(s.Energy) / float64(maxE)
		}
		var prev, next *Section
		if i > 0 {
			prev = &secs[i-1]
		}
		if i+1 < len(secs) {
			next = &secs[i+1]
		}
		switch {
		case i == 0 && rel < 0.75:
			s.Label = "intro"
		case i == len(secs)-1 && rel < 0.75:
			s.Label = "outro"
		case rel >= 0.85:
			s.Label = "peak"
		case next != nil && next.Energy > s.Energy+10:
			s.Label = "build"
		case prev != nil && prev.Energy > s.Energy+10:
			s.Label = "breakdown"
		default:
			s.Label = "steady"
		}
	}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

// ProbeResult is a trimmed ffprobe summary of any media file.
type ProbeResult struct {
	Path     string  `json:"path"`
	Format   string  `json:"format"`
	Duration float64 `json:"duration_seconds"`
	Streams  []struct {
		CodecType  string `json:"codec_type"`
		CodecName  string `json:"codec_name"`
		Width      int    `json:"width,omitempty"`
		Height     int    `json:"height,omitempty"`
		SampleRate string `json:"sample_rate,omitempty"`
		Channels   int    `json:"channels,omitempty"`
	} `json:"streams"`
}

// Probe runs ffprobe and returns a compact summary.
func Probe(ctx context.Context, path string) (*ProbeResult, error) {
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-print_format", "json", "-show_format", "-show_streams", path)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe %s: %w", path, err)
	}
	var raw struct {
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType  string `json:"codec_type"`
			CodecName  string `json:"codec_name"`
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			SampleRate string `json:"sample_rate"`
			Channels   int    `json:"channels"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	res := &ProbeResult{Path: path, Format: raw.Format.FormatName}
	fmt.Sscanf(raw.Format.Duration, "%f", &res.Duration)
	res.Duration = round2(res.Duration)
	for _, s := range raw.Streams {
		res.Streams = append(res.Streams, struct {
			CodecType  string `json:"codec_type"`
			CodecName  string `json:"codec_name"`
			Width      int    `json:"width,omitempty"`
			Height     int    `json:"height,omitempty"`
			SampleRate string `json:"sample_rate,omitempty"`
			Channels   int    `json:"channels,omitempty"`
		}(s))
	}
	return res, nil
}
