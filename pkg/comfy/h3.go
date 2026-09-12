package comfy

import (
	"fmt"
	"math"
	"strings"
)

// MiniMax H3 is a 33B omni DiT: one denoise pass produces both the video and a
// native 32 kHz stereo track. On this box it runs across two RTX 3090s via
// Raylight (Ulysses sequence parallel + FSDP), which is what makes the longer
// sequences fit at all.
//
// Weight file names as they appear to the ComfyUI-H3 instance.
const (
	H3DiT      = "minimax_h3_fl2va_pruned_int8_convrot.safetensors"
	H3RefDiT   = "minimax_h3_ref2va_pruned_int8_convrot.safetensors"
	H3Turbo8   = "minimax_h3_fl2v_turbo_8step_v1.0_comfyui_bf16.safetensors"
	H3Turbo4   = "minimax_h3_fl2v_turbo_4step_v1.0_768p_comfyui_bf16.safetensors"
	H3TextEnc  = "qwen3vl_32b_minimax_h3_nvfp4_awq.safetensors"
	H3VideoVAE = "minimax_h3_video_vae_fp16.safetensors"
	H3AudioVAE = "minimax_h3_audio_vae_fp32.safetensors"
)

// H3FPS is the model's native frame rate; every duration is expressed in
// frames at this rate.
const H3FPS = 24

// H3MinSeconds and H3MaxSeconds bound one clip. The model's trained sequence
// range is roughly 124-362 frames; outside it quality degrades and, at the top
// end, the sequence stops fitting even split across both cards.
const (
	H3MinSeconds = 2
	H3MaxSeconds = 15
)

// FramesForSeconds converts a duration to a frame count on the grid H3
// requires: length % 17 == 5 (3s=73, 5s=124, 10s=243, 15s=362).
func FramesForSeconds(seconds float64) int {
	n := int(math.Round(seconds * H3FPS))
	if n < 5 {
		n = 5
	}
	// Snap up to the next value with n%17 == 5. Go's % keeps the sign of the
	// dividend, so the offset is normalised to stay positive: rounding down
	// would silently hand the editor a shorter clip than the cut needs.
	delta := (5 - n%17) % 17
	if delta < 0 {
		delta += 17
	}
	return n + delta
}

// SecondsForFrames is the inverse, giving the clip's true duration.
func SecondsForFrames(frames int) float64 { return float64(frames) / H3FPS }

// pixel budgets per resolution tier, chosen so that 16:9 lands exactly on the
// two configurations benchmarked on this box: 864x480 and 1344x768.
var h3TierArea = map[string]float64{
	"480p": 864 * 480,
	"720p": 1344 * 768,
}

// H3Dimensions maps an aspect ratio and resolution tier to a width and height
// on the model's 32-pixel grid. Unknown ratios fall back to 16:9 and unknown
// tiers to 480p, which is the practical everyday configuration here.
func H3Dimensions(aspectRatio, resolution string) (int, int) {
	ar := parseAspect(aspectRatio)
	tier := strings.ToLower(strings.TrimSpace(resolution))
	// 1080p does not fit on two 3090s at any usable length; treat it as 720p.
	if tier == "1080p" {
		tier = "720p"
	}
	area, ok := h3TierArea[tier]
	if !ok {
		area = h3TierArea["480p"]
	}
	w := snap32(math.Sqrt(area * ar))
	h := snap32(math.Sqrt(area / ar))
	return w, h
}

func parseAspect(s string) float64 {
	num, den, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 16.0 / 9.0
	}
	var w, h float64
	if _, err := fmt.Sscanf(num, "%f", &w); err != nil || w <= 0 {
		return 16.0 / 9.0
	}
	if _, err := fmt.Sscanf(den, "%f", &h); err != nil || h <= 0 {
		return 16.0 / 9.0
	}
	return w / h
}

func snap32(v float64) int {
	n := int(math.Round(v/32)) * 32
	if n < 32 {
		n = 32
	}
	return n
}

// H3Options describes one clip render.
type H3Options struct {
	Prompt string
	Width  int
	Height int
	Length int // frames; must satisfy length%17==5
	Steps  int
	Seed   int64

	// FirstFrame and LastFrame are ComfyUI input-tree references (from
	// UploadImage). H3 is a first/last-frame-to-video model: giving only the
	// first frame animates it forward; giving both interpolates between them.
	FirstFrame *FileRef
	LastFrame  *FileRef

	// RefAudios (REF2VA only, max 3) anchor the joint audio+video generation
	// to supplied audio: the rendered performance lip-syncs to it. Slice the
	// audio to exactly the clip's length before uploading.
	RefAudios []FileRef

	// RefImages switches the graph to the REF2VA variant
	// (MiniMaxH3ReferenceToVideo): up to 9 reference images ride through the
	// Qwen3-VL encoder and the prompt binds them as <Picture 1>..<Picture N>.
	// Mutually exclusive with FirstFrame/LastFrame; needs the REF2VA DiT and
	// runs without the turbo LoRA (20 steps by default).
	RefImages []FileRef
	// RefImageSize is "match" (scale refs to the generation's pixel area;
	// default) or "max" (2048px short edge, best identity, several times
	// slower — reference tokens ride through every sampling step).
	RefImageSize string

	// FilenamePrefix is passed to SaveVideo, e.g. "video/auteur_shot01".
	FilenamePrefix string

	// Parallelism. GPUs=2 with Ulysses=2 splits the sequence across both
	// cards; FSDP shards the weights so the 33B model fits.
	GPUs    int
	Ulysses int
	Ring    int
	// DisableFSDP turns off weight sharding. FSDP is on by default because
	// the 33B model does not fit on a single 24GB card without it.
	DisableFSDP bool

	// Model overrides; zero values select the defaults above.
	DiT         string
	LoRA        string
	LoRAWeight  float64
	TextEncoder string
	Sampler     string
	Scheduler   string
	Attention   string
}

// H3RefSteps is the sampling depth for reference renders: the turbo LoRAs
// are FL2V-trained, so REF2VA runs the reference workflow's 20 plain steps.
const H3RefSteps = 20

// WithDefaults fills in the configuration proven on this box: the 8-step turbo
// LoRA, res_multistep/simple sampling, and a dual-GPU Raylight split. With
// RefImages set it selects the REF2VA variant instead.
func (o H3Options) WithDefaults() H3Options {
	ref := len(o.RefImages) > 0 || len(o.RefAudios) > 0
	if ref {
		if o.Steps == 0 {
			o.Steps = H3RefSteps
		}
		if o.DiT == "" {
			o.DiT = H3RefDiT
		}
		// The turbo LoRAs are FL2V-specific; never default one in.
		o.LoRA = ""
		if o.RefImageSize == "" {
			o.RefImageSize = "match"
		}
		o.FirstFrame, o.LastFrame = nil, nil
	}
	if o.Steps == 0 {
		o.Steps = 8
	}
	if o.Width == 0 || o.Height == 0 {
		o.Width, o.Height = H3Dimensions("16:9", "480p")
	}
	if o.Length == 0 {
		o.Length = FramesForSeconds(5)
	}
	if o.GPUs == 0 {
		o.GPUs = 2
	}
	if o.Ulysses == 0 {
		o.Ulysses = o.GPUs
	}
	if o.Ring == 0 {
		o.Ring = 1
	}
	if o.DiT == "" {
		o.DiT = H3DiT
	}
	if o.LoRA == "" && !ref {
		o.LoRA = H3Turbo8
	}
	if o.LoRAWeight == 0 {
		o.LoRAWeight = 1.0
	}
	if o.TextEncoder == "" {
		o.TextEncoder = H3TextEnc
	}
	if o.Sampler == "" {
		o.Sampler = "res_multistep"
	}
	if o.Scheduler == "" {
		o.Scheduler = "simple"
	}
	if o.Attention == "" {
		o.Attention = "TORCH_FLASH"
	}
	if o.FilenamePrefix == "" {
		o.FilenamePrefix = "video/auteur"
	}
	return o
}

// Describe summarises the render for progress messages and logs.
func (o H3Options) Describe() string {
	return fmt.Sprintf("%dx%d, %d frames (%.2fs @ %dfps), %d steps, %d GPU",
		o.Width, o.Height, o.Length, SecondsForFrames(o.Length), H3FPS, o.Steps, o.GPUs)
}

// BuildH3 returns the API-format workflow graph for one H3 clip.
//
//	RayInitializer ──┐
//	                 ├─> RayUNETLoader ──┬─> RayBasicGuider ────┐
//	RayLoraLoader ───┘                   └─> RayBasicScheduler ─┤
//	                                                            ├─> XFuserSamplerCustomAdvanced
//	CLIPLoader ─┬─> MiniMaxH3ImageToVideo ─(cond, latent)───────┘
//	VAELoader ──┘                                                │
//	                                    VAEDecode / VAEDecodeAudio ─> CreateVideo -> SaveVideo
func BuildH3(o H3Options) map[string]any {
	o = o.WithDefaults()
	g := map[string]any{}

	g["1"] = node("RayInitializer", map[string]any{
		"ray_cluster_address":       "local",
		"ray_cluster_namespace":     "default",
		"GPU":                       o.GPUs,
		"ulysses_degree":            o.Ulysses,
		"ring_degree":               o.Ring,
		"cfg_degree":                1,
		"dp_degree":                 1,
		"sync_ulysses":              false,
		"clear_vram_after_sampling": true,
		"FSDP":                      !o.DisableFSDP,
		"FSDP_CPU_OFFLOAD":          false,
		"XFuser_attention":          o.Attention,
		"skip_comm_test":            false,
		"use_mmap":                  false,
	})

	unet := map[string]any{
		"unet_name":       o.DiT,
		"weight_dtype":    "default",
		"ray_actors_init": link("1", 0),
	}
	if o.LoRA != "" {
		g["2"] = node("RayLoraLoader", map[string]any{
			"lora_name":      o.LoRA,
			"strength_model": o.LoRAWeight,
		})
		unet["lora"] = link("2", 0)
	}
	g["3"] = node("RayUNETLoader", unet)

	g["4"] = node("CLIPLoader", map[string]any{
		"clip_name": o.TextEncoder, "type": "minimax", "device": "default",
	})
	g["5"] = node("VAELoader", map[string]any{"vae_name": H3VideoVAE})
	g["6"] = node("VAELoader", map[string]any{"vae_name": H3AudioVAE})

	cond := map[string]any{
		"clip":   link("4", 0),
		"vae":    link("5", 0),
		"prompt": o.Prompt,
		"width":  o.Width,
		"height": o.Height,
		"length": o.Length,
	}
	if len(o.RefImages) > 0 || len(o.RefAudios) > 0 {
		// REF2VA: references enter as an autogrow input, one LoadImage each.
		cond["audio_vae"] = link("6", 0)
		cond["ref_image_size"] = o.RefImageSize
		for i, ref := range o.RefImages {
			id := fmt.Sprintf("%d", 20+i)
			g[id] = node("LoadImage", map[string]any{"image": ref.Name()})
			cond[fmt.Sprintf("ref_images.ref_image_%d", i)] = link(id, 0)
		}
		for i, ref := range o.RefAudios {
			id := fmt.Sprintf("%d", 30+i)
			g[id] = node("LoadAudio", map[string]any{"audio": ref.Name()})
			cond[fmt.Sprintf("ref_audios.ref_audio_%d", i)] = link(id, 0)
		}
		g["7"] = node("MiniMaxH3ReferenceToVideo", cond)
	} else {
		if o.FirstFrame != nil {
			g["16"] = node("LoadImage", map[string]any{"image": o.FirstFrame.Name()})
			cond["first_frame"] = link("16", 0)
		}
		if o.LastFrame != nil {
			g["17"] = node("LoadImage", map[string]any{"image": o.LastFrame.Name()})
			cond["last_frame"] = link("17", 0)
		}
		g["7"] = node("MiniMaxH3ImageToVideo", cond)
	}

	g["8"] = node("RayBasicGuider", map[string]any{
		"ray_actors": link("3", 0), "conditioning": link("7", 0),
	})
	g["9"] = node("RayBasicScheduler", map[string]any{
		"ray_actors": link("3", 0), "scheduler": o.Scheduler,
		"steps": o.Steps, "denoise": 1.0,
	})
	g["10"] = node("KSamplerSelect", map[string]any{"sampler_name": o.Sampler})

	g["11"] = node("XFuserSamplerCustomAdvanced", map[string]any{
		"add_noise": true, "noise_seed": o.Seed,
		"guider": link("8", 0), "sampler": link("10", 0),
		"sigmas": link("9", 0), "latent_image": link("7", 1),
	})

	g["12"] = node("VAEDecode", map[string]any{"samples": link("11", 0), "vae": link("5", 0)})
	g["13"] = node("VAEDecodeAudio", map[string]any{"samples": link("11", 0), "vae": link("6", 0)})
	g["14"] = node("CreateVideo", map[string]any{
		"images": link("12", 0), "fps": float64(H3FPS), "audio": link("13", 0),
	})
	g["15"] = node("SaveVideo", map[string]any{
		"video": link("14", 0), "filename_prefix": o.FilenamePrefix,
		"format": "auto", "codec": "auto",
	})
	return g
}

func node(classType string, inputs map[string]any) map[string]any {
	return map[string]any{"class_type": classType, "inputs": inputs}
}

// link references another node's output slot.
func link(nodeID string, slot int) []any { return []any{nodeID, slot} }
