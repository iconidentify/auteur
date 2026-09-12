package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Skill is a markdown playbook an agent can consult. Skills follow the
// chonkbase progressive-disclosure pattern: only name and description are
// injected into the system prompt; the full body is fetched on demand via
// the read_skill tool.
//
// Skill files are markdown with a simple frontmatter block:
//
//	---
//	name: reference-integration
//	description: How to weave reference imagery into generated shots
//	agents: director, cinematographer
//	---
//	...body...
//
// An empty agents list makes the skill available to every agent.
type Skill struct {
	Name        string
	Description string
	Agents      []string
	Body        string
	Path        string
}

// SkillLibrary holds skills loaded from a directory.
type SkillLibrary struct {
	skills map[string]Skill
}

// LoadSkills reads every *.md file in dir (non-recursive) into a library.
func LoadSkills(dir string) (*SkillLibrary, error) {
	lib := &SkillLibrary{skills: map[string]Skill{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read skills dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		sk := parseSkill(string(data), path)
		if sk.Name == "" {
			sk.Name = strings.TrimSuffix(e.Name(), ".md")
		}
		lib.skills[sk.Name] = sk
	}
	return lib, nil
}

func parseSkill(content, path string) Skill {
	sk := Skill{Path: path, Body: content}
	if !strings.HasPrefix(content, "---") {
		return sk
	}
	rest := content[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return sk
	}
	front := rest[:end]
	sk.Body = strings.TrimLeft(rest[end+4:], "\n")
	for _, line := range strings.Split(front, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "name":
			sk.Name = val
		case "description":
			sk.Description = val
		case "agents":
			val = strings.Trim(val, "[]")
			for _, a := range strings.Split(val, ",") {
				if a = strings.TrimSpace(a); a != "" {
					sk.Agents = append(sk.Agents, a)
				}
			}
		}
	}
	return sk
}

// ForAgent returns the skills visible to a given agent role, sorted by name.
func (l *SkillLibrary) ForAgent(role string) []Skill {
	var out []Skill
	for _, sk := range l.skills {
		if len(sk.Agents) == 0 {
			out = append(out, sk)
			continue
		}
		for _, a := range sk.Agents {
			if a == role {
				out = append(out, sk)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get returns a skill by name.
func (l *SkillLibrary) Get(name string) (Skill, bool) {
	sk, ok := l.skills[name]
	return sk, ok
}

// PromptBlock renders the progressive-disclosure summary for a system
// prompt: one line per skill, with instructions to read before relying.
func (l *SkillLibrary) PromptBlock(role string) string {
	skills := l.ForAgent(role)
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Skills\n")
	b.WriteString("You have a library of skills: expert playbooks for parts of your job. ")
	b.WriteString("Before doing work a skill covers, call read_skill to load it and follow it.\n\n")
	for _, sk := range skills {
		fmt.Fprintf(&b, "- %s: %s\n", sk.Name, sk.Description)
	}
	return b.String()
}

// RegisterSkillTool adds a read_skill tool bound to this library.
func (l *SkillLibrary) RegisterSkillTool(reg *Registry, role string) {
	names := []string{}
	for _, sk := range l.ForAgent(role) {
		names = append(names, sk.Name)
	}
	reg.Register(ToolDefinition{
		Name:        "read_skill",
		Description: "Load the full text of a skill playbook. Available: " + strings.Join(names, ", "),
		InputSchema: Obj(map[string]any{
			"name": Str("The skill name to load"),
		}, "name"),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		name, _ := input["name"].(string)
		sk, ok := l.Get(name)
		if !ok {
			return "", fmt.Errorf("no skill named %q (available: %s)", name, strings.Join(names, ", "))
		}
		return sk.Body, nil
	})
}
