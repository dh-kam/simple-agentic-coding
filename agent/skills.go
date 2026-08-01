package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Skill is a loaded skill definition (from SKILL.md).
type Skill struct {
	Name        string // skill identifier
	Description string // one-line description for the model
	Content     string // full instructions (body of SKILL.md)
}

// LoadSkills scans a directory for skill subdirectories containing SKILL.md.
// Each skill is a subdirectory with a SKILL.md file with YAML-like frontmatter:
//
//	---
//	name: my-skill
//	description: what this skill does
//	---
//	# Full instructions...
//
// Returns skills sorted by name. Missing directory → empty list (no error).
func LoadSkills(dir string) ([]Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no skills dir → no skills
		}
		return nil, fmt.Errorf("skills: read %q: %w", dir, err)
	}
	var skills []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name(), "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			continue // no SKILL.md → skip
		}
		name, desc, body := parseSkillFile(string(data))
		if name == "" {
			name = e.Name()
		}
		skills = append(skills, Skill{Name: name, Description: desc, Content: body})
	}
	return skills, nil
}

// parseSkillFile extracts frontmatter (name, description) and body from SKILL.md.
func parseSkillFile(content string) (name, description, body string) {
	content = strings.TrimSpace(content)
	body = content // default: entire content is body
	if !strings.HasPrefix(content, "---") {
		return "", "", content
	}
	rest := strings.TrimPrefix(content, "---")
	// find closing ---
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", "", content
	}
	front := rest[:idx]
	body = strings.TrimSpace(rest[idx+4:])
	for _, line := range strings.Split(front, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "name:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		case strings.HasPrefix(line, "description:"):
			description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		}
	}
	return name, description, body
}

// SkillSummary builds a system-prompt section listing available skills.
// Returns "" when there are no skills.
func SkillSummary(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## 사용 가능한 스킬 (load_skill 도구로 상세 지침을 로드할 수 있다)")
	for _, s := range skills {
		line := "- " + s.Name
		if s.Description != "" {
			line += ": " + s.Description
		}
		b.WriteString("\n" + line)
	}
	return b.String()
}

// NewLoadSkillTool creates a tool that loads a skill's full content by name.
// The model calls this when it needs the detailed instructions.
func NewLoadSkillTool(skills []Skill) Tool {
	byName := make(map[string]Skill, len(skills))
	names := make([]string, 0, len(skills))
	for _, s := range skills {
		byName[s.Name] = s
		names = append(names, s.Name)
	}
	return Tool{
		Name:        "load_skill",
		Description: "스킬의 상세 지침을 로드한다. 사용 가능: " + strings.Join(names, ", "),
		InputSchema: map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "로드할 스킬 이름",
			},
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			s, ok := byName[in.Name]
			if !ok {
				return "", fmt.Errorf("unknown skill %q (available: %s)", in.Name, strings.Join(names, ", "))
			}
			return s.Content, nil
		},
	}
}
