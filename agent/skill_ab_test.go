package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSkillAB_SystemPromptDiff — 스킬 유무에 따른 시스템 프롬프트/도구 차이 검증
func TestSkillAB_SystemPromptDiff(t *testing.T) {
	// 1. 스킬 없는 경우 (빈 디렉토리)
	noSkillDir := t.TempDir()
	noSkills, _ := LoadSkills(noSkillDir)
	noSkillSummary := SkillSummary(noSkills)

	// 2. 스킬 있는 경우
	withSkillDir := t.TempDir()
	skillPath := filepath.Join(withSkillDir, "test-skill")
	os.MkdirAll(skillPath, 0755)
	os.WriteFile(filepath.Join(skillPath, "SKILL.md"), []byte(`---
name: test-skill
description: A test skill for A/B comparison
---
# Test Skill
Always write tests for every function.`), 0644)
	withSkills, _ := LoadSkills(withSkillDir)
	withSkillSummary := SkillSummary(withSkills)

	// 검증 1: 스킬 없으면 summary 빈 문자열
	if noSkillSummary != "" {
		t.Errorf("noSkillSummary should be empty, got: %q", noSkillSummary)
	}

	// 검증 2: 스킬 있으면 summary에 이름 포함
	if !strings.Contains(withSkillSummary, "test-skill") {
		t.Errorf("withSkillSummary should contain 'test-skill': %q", withSkillSummary)
	}

	// 검증 3: load_skill 도구로 스킬 내용 로드
	withTool := NewLoadSkillTool(withSkills)
	out, err := withTool.Run(context.Background(), json.RawMessage(`{"name":"test-skill"}`))
	if err != nil {
		t.Fatalf("load_skill failed: %v", err)
	}
	if !strings.Contains(out, "Always write tests") {
		t.Errorf("loaded content missing: %q", out)
	}

	// 검증 4: 스킬 없으면 load_skill 에러
	noTool := NewLoadSkillTool(noSkills)
	_, err = noTool.Run(context.Background(), json.RawMessage(`{"name":"anything"}`))
	if err == nil {
		t.Error("load_skill with no skills should error")
	}
}

// TestSkillAB_DescriptionLength — 10개 스킬 summary가 너무 길지 않은지 확인
func TestSkillAB_DescriptionLength(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("skill-%02d", i)
		os.MkdirAll(filepath.Join(dir, name), 0755)
		os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(
			fmt.Sprintf("---\nname: %s\ndescription: Skill number %d for testing\n---\n# %s\nBody.", name, i, name)), 0644)
	}
	skills, _ := LoadSkills(dir)
	summary := SkillSummary(skills)
	if len(summary) > 1000 {
		t.Errorf("summary too long for 10 skills: %d chars (want < 1000)", len(summary))
	}
	if !strings.Contains(summary, "skill-00") || !strings.Contains(summary, "skill-09") {
		t.Errorf("summary should contain all skill names")
	}
}

// TestSkillAB_LoadedSkillAffectsContext — 스킬 로드 후 내용이 컨텍스트에 반영되는지 확인
func TestSkillAB_LoadedSkillAffectsContext(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "code-review"), 0755)
	os.WriteFile(filepath.Join(dir, "code-review", "SKILL.md"), []byte(`---
name: code-review
description: Code review guidelines
---
# Code Review Rules
1. Check for nil pointer dereference
2. Verify error handling
3. Look for race conditions`), 0644)

	skills, _ := LoadSkills(dir)
	tool := NewLoadSkillTool(skills)

	// load_skill 호출 → 스킬 내용 반환
	out, err := tool.Run(context.Background(), json.RawMessage(`{"name":"code-review"}`))
	if err != nil {
		t.Fatalf("load_skill: %v", err)
	}

	// 스킬 내용이 3개 규칙을 모두 포함하는지 확인
	rules := []string{"nil pointer", "error handling", "race condition"}
	for _, r := range rules {
		if !strings.Contains(out, r) {
			t.Errorf("skill content missing rule %q", r)
		}
	}

	// summary에 code-review가 있어야 함
	summary := SkillSummary(skills)
	if !strings.Contains(summary, "code-review") {
		t.Errorf("summary should mention code-review")
	}
}
