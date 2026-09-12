package agents

import (
	"encoding/json"
	"strings"
	"testing"

	"auteur/pkg/comfy"
	"auteur/pkg/harness"
)

// shotSchema digs out the per-shot property map from the registered tool.
func shotSchema(t *testing.T, backend VideoBackend) (map[string]any, []string, string) {
	t.Helper()
	reg := harness.NewRegistry()
	tb := &Toolbox{Video: backend}
	tb.RegisterVideoGenTools(reg)

	var def *harness.ToolDefinition
	for _, d := range reg.List() {
		if d.Name == "generate_video_clips" {
			cp := d
			def = &cp
		}
	}
	if def == nil {
		t.Fatal("generate_video_clips was not registered")
	}
	raw, err := json.Marshal(def.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Properties struct {
			Shots struct {
				Items struct {
					Properties map[string]any `json:"properties"`
				} `json:"items"`
			} `json:"shots"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	props := s.Properties.Shots.Items.Properties
	mode, _ := props["mode"].(map[string]any)
	var modes []string
	if mode != nil {
		for _, v := range mode["enum"].([]any) {
			modes = append(modes, v.(string))
		}
	}
	return props, modes, def.Description
}

// H3 offers all three modes since REF2VA landed, and the description must
// teach the <Picture N> binding syntax.
func TestH3SchemaModes(t *testing.T) {
	props, modes, desc := shotSchema(t, &H3Backend{Pool: comfy.NewPool([]string{"http://127.0.0.1:8189"})})

	if len(modes) != 3 {
		t.Errorf("mode enum = %v, want text, image, reference", modes)
	}
	if _, ok := props["reference_paths"]; !ok {
		t.Error("reference_paths missing: REF2VA content injection is unavailable")
	}
	if _, ok := props["end_image_path"]; !ok {
		t.Error("end_image_path missing: shot chaining is unavailable")
	}
	if _, ok := props["seed"]; !ok {
		t.Error("seed missing: refining a take is unavailable")
	}
	if _, ok := props["sync_audio_path"]; !ok {
		t.Error("sync_audio_path missing: lip sync is unavailable")
	}
	if !strings.Contains(desc, "MiniMax H3") {
		t.Errorf("description does not name the engine: %.80s", desc)
	}
	if !strings.Contains(desc, "<Picture 1>") {
		t.Error("description does not teach the <Picture N> reference syntax")
	}
	dur, _ := props["duration_seconds"].(map[string]any)
	if d, _ := dur["description"].(string); !strings.Contains(d, "2-15") {
		t.Errorf("duration description = %q, want H3's 2-15s range", d)
	}
}

// The hosted engine keeps every mode it always had.
func TestGrokSchemaKeepsReferenceMode(t *testing.T) {
	props, modes, desc := shotSchema(t, &GrokBackend{})

	found := false
	for _, m := range modes {
		if m == "reference" {
			found = true
		}
	}
	if !found {
		t.Errorf("mode enum = %v, want reference included", modes)
	}
	if _, ok := props["reference_paths"]; !ok {
		t.Error("reference_paths missing on the Grok backend")
	}
	if _, ok := props["end_image_path"]; ok {
		t.Error("end_image_path offered on Grok, which has no last-frame control")
	}
	if !strings.Contains(desc, "Grok Imagine") {
		t.Errorf("description does not name the engine: %.80s", desc)
	}
}
