package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSkills(t *testing.T) {
	dir := t.TempDir()
	// skill 1: with frontmatter
	os.MkdirAll(filepath.Join(dir, "go-test"), 0755)
	os.WriteFile(filepath.Join(dir, "go-test", "SKILL.md"), []byte(`---
name: go-test
description: Write Go tests following best practices
---
# Go Test Skill

Always use table-driven tests. Name test cases descriptively.
Use t.Run for subtests.`), 0644)

	// skill 2: no frontmatter (name from dir)
	os.MkdirAll(filepath.Join(dir, "lint"), 0755)
	os.WriteFile(filepath.Join(dir, "lint", "SKILL.md"), []byte("# Lint Skill\nRun golangci-lint."), 0644)

	// not a skill (no SKILL.md)
	os.MkdirAll(filepath.Join(dir, "empty"), 0755)

	skills, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("skills = %d, want 2", len(skills))
	}

	// find go-test skill
	var gt Skill
	for _, s := range skills {
		if s.Name == "go-test" {
			gt = s
		}
	}
	if gt.Name == "" {
		t.Fatal("go-test skill not found")
	}
	if gt.Description != "Write Go tests following best practices" {
		t.Errorf("description = %q", gt.Description)
	}
	if !strings.Contains(gt.Content, "table-driven") {
		t.Errorf("content missing instructions: %q", gt.Content)
	}

	// lint skill: name from directory
	var lt Skill
	for _, s := range skills {
		if s.Name == "lint" {
			lt = s
		}
	}
	if lt.Name == "" {
		t.Fatal("lint skill not found")
	}
	if !strings.Contains(lt.Content, "golangci-lint") {
		t.Errorf("lint content = %q", lt.Content)
	}
}

func TestSkillSummary(t *testing.T) {
	if s := SkillSummary(nil); s != "" {
		t.Error("nil should be empty")
	}
	s := SkillSummary([]Skill{
		{Name: "go-test", Description: "Write tests"},
		{Name: "lint", Description: "Run linter"},
	})
	if !strings.Contains(s, "go-test") || !strings.Contains(s, "lint") {
		t.Errorf("summary missing skills: %q", s)
	}
	if !strings.Contains(s, "load_skill") {
		t.Error("summary should mention load_skill tool")
	}
}

func TestLoadSkillTool(t *testing.T) {
	skills := []Skill{
		{Name: "go-test", Content: "# Go tests\nUse table-driven tests."},
		{Name: "lint", Content: "# Lint\nRun golangci-lint."},
	}
	tool := NewLoadSkillTool(skills)

	// load existing skill
	out, err := call(t, tool, map[string]any{"name": "go-test"})
	if err != nil {
		t.Fatalf("load go-test: %v", err)
	}
	if !strings.Contains(out, "table-driven") {
		t.Errorf("content = %q", out)
	}

	// load unknown skill → error
	_, err = call(t, tool, map[string]any{"name": "nope"})
	if err == nil {
		t.Error("expected error for unknown skill")
	}
}

func TestLoadSkills_missingDir(t *testing.T) {
	skills, err := LoadSkills("/nonexistent/path")
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if skills != nil {
		t.Errorf("expected nil skills, got %d", len(skills))
	}
}
