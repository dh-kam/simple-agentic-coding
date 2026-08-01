package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Skill struct {
	Name, Description, Content string
}

func LoadSkills(dir string) ([]Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var skills []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		name, desc, body := parseSkillFile(string(data))
		if name == "" {
			name = e.Name()
		}
		skills = append(skills, Skill{Name: name, Description: desc, Content: body})
	}
	return skills, nil
}

func parseSkillFile(content string) (name, description, body string) {
	content = strings.TrimSpace(content)
	body = content
	if !strings.HasPrefix(content, "---") {
		return
	}
	rest := strings.TrimPrefix(content, "---")
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return
	}
	for _, line := range strings.Split(rest[:idx], "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			name = strings.TrimSpace(line[5:])
		}
		if strings.HasPrefix(line, "description:") {
			description = strings.TrimSpace(line[12:])
		}
	}
	body = strings.TrimSpace(rest[idx+4:])
	return
}

func SkillSummary(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## 사용 가능한 스킬 (load_skill 도구로 상세 지침 로드)")
	for _, s := range skills {
		line := "- " + s.Name
		if s.Description != "" {
			line += ": " + s.Description
		}
		b.WriteString("\n" + line)
	}
	return b.String()
}

func NewLoadSkillTool(skills []Skill) Tool {
	byName := map[string]Skill{}
	for _, s := range skills {
		byName[s.Name] = s
	}
	return Tool{
		Name:        "load_skill",
		Description: "스킬 상세 지침을 로드한다.",
		InputSchema: map[string]any{"name": map[string]any{"type": "string"}},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Name string `json:"name"`
			}
			json.Unmarshal(args, &in)
			s, ok := byName[in.Name]
			if !ok {
				return "", fmt.Errorf("unknown skill %q", in.Name)
			}
			return s.Content, nil
		},
	}
}
