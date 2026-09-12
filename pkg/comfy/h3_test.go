package comfy

import "testing"

func TestFramesForSeconds(t *testing.T) {
	// The counts published for this model: 3s=73, 5s=124, 10s=243, 15s=362.
	// 2s exercises the case where the raw count sits above the grid point and
	// must snap up to the next one (48 -> 56), not down to the previous.
	cases := map[float64]int{2: 56, 3: 73, 5: 124, 10: 243, 15: 362}
	for sec, want := range cases {
		if got := FramesForSeconds(sec); got != want {
			t.Errorf("FramesForSeconds(%v) = %d, want %d", sec, got, want)
		}
	}
	// Every duration must land on the model's grid, never below its floor.
	for sec := 0.0; sec <= 20; sec += 0.25 {
		n := FramesForSeconds(sec)
		if n%17 != 5 {
			t.Errorf("FramesForSeconds(%v) = %d, not on the 17k+5 grid", sec, n)
		}
		if n < 5 {
			t.Errorf("FramesForSeconds(%v) = %d, below the 5-frame floor", sec, n)
		}
		// Never hand back a clip shorter than was asked for.
		if raw := int(sec * H3FPS); n < raw {
			t.Errorf("FramesForSeconds(%v) = %d, rounded down below %d frames", sec, n, raw)
		}
	}
}

func TestH3Dimensions(t *testing.T) {
	cases := []struct {
		ar, res string
		w, h    int
	}{
		// The two configurations benchmarked on this box.
		{"16:9", "480p", 864, 480},
		{"16:9", "720p", 1344, 768},
		{"9:16", "480p", 480, 864},
		{"1:1", "480p", 640, 640},
		// 1080p does not fit on two 3090s; it degrades to the 720p tier.
		{"16:9", "1080p", 1344, 768},
		// Unknown inputs fall back to 16:9 at 480p rather than failing.
		{"", "", 864, 480},
		{"garbage", "nonsense", 864, 480},
	}
	for _, c := range cases {
		w, h := H3Dimensions(c.ar, c.res)
		if w != c.w || h != c.h {
			t.Errorf("H3Dimensions(%q,%q) = %dx%d, want %dx%d", c.ar, c.res, w, h, c.w, c.h)
		}
		if w%32 != 0 || h%32 != 0 {
			t.Errorf("H3Dimensions(%q,%q) = %dx%d, not on the 32px grid", c.ar, c.res, w, h)
		}
	}
}

func TestBuildH3Wiring(t *testing.T) {
	g := BuildH3(H3Options{Prompt: "a test shot", Length: FramesForSeconds(5)})

	for _, id := range []string{"1", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15"} {
		if _, ok := g[id]; !ok {
			t.Fatalf("node %s missing from the graph", id)
		}
	}
	if ct := g["7"].(map[string]any)["class_type"]; ct != "MiniMaxH3ImageToVideo" {
		t.Errorf("node 7 class_type = %v, want MiniMaxH3ImageToVideo", ct)
	}

	cond := g["7"].(map[string]any)["inputs"].(map[string]any)
	if _, ok := cond["first_frame"]; ok {
		t.Error("first_frame wired with no image supplied")
	}
	if cond["length"] != 124 {
		t.Errorf("length = %v, want 124", cond["length"])
	}

	// FSDP is what makes the 33B model fit across two 24GB cards.
	if fsdp := g["1"].(map[string]any)["inputs"].(map[string]any)["FSDP"]; fsdp != true {
		t.Errorf("FSDP = %v, want true by default", fsdp)
	}

	// The sampler must consume the conditioning node's latent output (slot 1).
	latent := g["11"].(map[string]any)["inputs"].(map[string]any)["latent_image"].([]any)
	if latent[0] != "7" || latent[1] != 1 {
		t.Errorf("latent_image = %v, want [7 1]", latent)
	}
}

func TestBuildH3Frames(t *testing.T) {
	first := FileRef{Filename: "a.png", Subfolder: "auteur/p1", Type: "input"}
	last := FileRef{Filename: "b.png", Type: "input"}
	g := BuildH3(H3Options{Prompt: "x", FirstFrame: &first, LastFrame: &last})

	cond := g["7"].(map[string]any)["inputs"].(map[string]any)
	if got := cond["first_frame"].([]any)[0]; got != "16" {
		t.Errorf("first_frame from node %v, want 16", got)
	}
	if got := cond["last_frame"].([]any)[0]; got != "17" {
		t.Errorf("last_frame from node %v, want 17", got)
	}
	if name := g["16"].(map[string]any)["inputs"].(map[string]any)["image"]; name != "auteur/p1/a.png" {
		t.Errorf("LoadImage name = %v, want auteur/p1/a.png", name)
	}
	if name := g["17"].(map[string]any)["inputs"].(map[string]any)["image"]; name != "b.png" {
		t.Errorf("LoadImage name = %v, want b.png", name)
	}
}

func TestBuildH3ReferenceGraph(t *testing.T) {
	refs := []FileRef{
		{Filename: "shot.png", Subfolder: "auteur/p1"},
		{Filename: "logo.png"},
	}
	g := BuildH3(H3Options{Prompt: "the monitor displays <Picture 1>", RefImages: refs})

	cond := g["7"].(map[string]any)
	if ct := cond["class_type"]; ct != "MiniMaxH3ReferenceToVideo" {
		t.Fatalf("node 7 class_type = %v, want MiniMaxH3ReferenceToVideo", ct)
	}
	in := cond["inputs"].(map[string]any)
	// References arrive through the autogrow input, one LoadImage each.
	if got := in["ref_images.ref_image_0"].([]any)[0]; got != "20" {
		t.Errorf("ref_image_0 from node %v, want 20", got)
	}
	if got := in["ref_images.ref_image_1"].([]any)[0]; got != "21" {
		t.Errorf("ref_image_1 from node %v, want 21", got)
	}
	if name := g["20"].(map[string]any)["inputs"].(map[string]any)["image"]; name != "auteur/p1/shot.png" {
		t.Errorf("ref LoadImage name = %v", name)
	}
	// REF2VA needs the audio VAE wired into conditioning, not just decode.
	if av := in["audio_vae"].([]any)[0]; av != "6" {
		t.Errorf("audio_vae from node %v, want 6", av)
	}
	if in["ref_image_size"] != "match" {
		t.Errorf("ref_image_size = %v, want match", in["ref_image_size"])
	}
	// The REF2VA DiT loads, without any turbo LoRA, at 20 steps.
	if dit := g["3"].(map[string]any)["inputs"].(map[string]any)["unet_name"]; dit != H3RefDiT {
		t.Errorf("unet = %v, want %v", dit, H3RefDiT)
	}
	if _, ok := g["2"]; ok {
		t.Error("LoRA node present in reference graph; turbo LoRAs are FL2V-only")
	}
	if steps := g["9"].(map[string]any)["inputs"].(map[string]any)["steps"]; steps != H3RefSteps {
		t.Errorf("steps = %v, want %v", steps, H3RefSteps)
	}
	// No first/last frame in reference mode.
	if _, ok := in["first_frame"]; ok {
		t.Error("first_frame wired in reference mode")
	}
}

func TestBuildH3SyncAudioGraph(t *testing.T) {
	g := BuildH3(H3Options{Prompt: "he raps to camera",
		RefImages: []FileRef{{Filename: "face.png"}},
		RefAudios: []FileRef{{Filename: "bars.wav", Subfolder: "auteur_sync"}}})
	cond := g["7"].(map[string]any)
	if ct := cond["class_type"]; ct != "MiniMaxH3ReferenceToVideo" {
		t.Fatalf("class_type = %v", ct)
	}
	in := cond["inputs"].(map[string]any)
	if got := in["ref_audios.ref_audio_0"].([]any)[0]; got != "30" {
		t.Errorf("ref_audio_0 from node %v, want 30", got)
	}
	if name := g["30"].(map[string]any)["inputs"].(map[string]any)["audio"]; name != "auteur_sync/bars.wav" {
		t.Errorf("LoadAudio name = %v", name)
	}
	// Audio-only reference (no images) must still select the REF2VA path.
	g2 := BuildH3(H3Options{Prompt: "x", RefAudios: []FileRef{{Filename: "a.wav"}}})
	if dit := g2["3"].(map[string]any)["inputs"].(map[string]any)["unet_name"]; dit != H3RefDiT {
		t.Errorf("audio-only ref used unet %v, want REF2VA", dit)
	}
}
