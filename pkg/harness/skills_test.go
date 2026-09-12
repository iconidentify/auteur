package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSkillsAndProgressiveDisclosure(t *testing.T) {
	dir := t.TempDir()
	content := `---
name: test-skill
description: A skill for testing
agents: editor, director
---
# The body

Full playbook content.
`
	if err := os.WriteFile(filepath.Join(dir, "test-skill.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "open-skill.md"), []byte("no frontmatter here"), 0o644); err != nil {
		t.Fatal(err)
	}

	lib, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}

	sk, ok := lib.Get("test-skill")
	if !ok {
		t.Fatal("test-skill not found")
	}
	if sk.Description != "A skill for testing" {
		t.Errorf("description = %q", sk.Description)
	}
	if !strings.Contains(sk.Body, "Full playbook content") || strings.Contains(sk.Body, "name: test-skill") {
		t.Errorf("body not stripped of frontmatter: %q", sk.Body)
	}

	// Role scoping: editor sees both, screenwriter only the unscoped one.
	if got := len(lib.ForAgent("editor")); got != 2 {
		t.Errorf("editor sees %d skills, want 2", got)
	}
	if got := len(lib.ForAgent("screenwriter")); got != 1 {
		t.Errorf("screenwriter sees %d skills, want 1", got)
	}

	// Prompt block carries the summary but never the body.
	block := lib.PromptBlock("editor")
	if !strings.Contains(block, "test-skill: A skill for testing") {
		t.Errorf("prompt block missing summary: %q", block)
	}
	if strings.Contains(block, "Full playbook content") {
		t.Error("prompt block leaked full body; progressive disclosure broken")
	}

	// read_skill tool returns the body.
	reg := NewRegistry()
	lib.RegisterSkillTool(reg, "editor")
	out, err := reg.Execute(context.Background(), ToolCall{Name: "read_skill", Input: map[string]any{"name": "test-skill"}})
	if err != nil {
		t.Fatalf("read_skill: %v", err)
	}
	if !strings.Contains(out, "Full playbook content") {
		t.Errorf("read_skill returned %q", out)
	}
	if _, err := reg.Execute(context.Background(), ToolCall{Name: "read_skill", Input: map[string]any{"name": "nope"}}); err == nil {
		t.Error("unknown skill not rejected")
	}
}

func TestRegistryOrderAndUnregister(t *testing.T) {
	reg := NewRegistry()
	h := func(ctx context.Context, in map[string]any) (string, error) { return "", nil }
	reg.Register(ToolDefinition{Name: "b"}, h)
	reg.Register(ToolDefinition{Name: "a"}, h)
	reg.Register(ToolDefinition{Name: "b", Description: "replaced"}, h)

	list := reg.List()
	if len(list) != 2 || list[0].Name != "b" || list[1].Name != "a" {
		t.Errorf("registration order not preserved: %+v", list)
	}
	if list[0].Description != "replaced" {
		t.Error("re-register did not replace definition")
	}
	reg.Unregister("b")
	if len(reg.List()) != 1 {
		t.Error("unregister failed")
	}
}
