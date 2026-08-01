package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func InstallFromGitHub(input, skillDir string) ([]string, error) {
	cloneURL, repoName, err := parseGitHubURL(input)
	if err != nil {
		return nil, err
	}
	tmpDir, err := os.MkdirTemp("", "agentic-skill-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	cmd := exec.Command("git", "clone", "--depth", "1", cloneURL, tmpDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git clone: %w\n%s", err, out)
	}
	found := scanForSkills(tmpDir, repoName)
	if len(found) == 0 {
		return nil, fmt.Errorf("no SKILL.md found in %s", input)
	}
	os.MkdirAll(skillDir, 0755)
	var installed []string
	for _, s := range found {
		name := s.skillName
		if name == "" {
			name = repoName
		}
		dest := filepath.Join(skillDir, name)
		os.RemoveAll(dest)
		os.MkdirAll(dest, 0755)
		content := s.content
		if !strings.HasPrefix(strings.TrimSpace(content), "---") {
			content = fmt.Sprintf("---\nname: %s\ndescription: (from %s)\n---\n%s", name, input, content)
		}
		os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte(content), 0644)
		installed = append(installed, name)
	}
	return installed, nil
}

func ListInstalledSkills(skillDir string) {
	skills, err := LoadSkills(skillDir)
	if err != nil || len(skills) == 0 {
		fmt.Println("  (no skills installed)")
		return
	}
	fmt.Printf("  %d skill(s) in %s:\n\n", len(skills), skillDir)
	for _, s := range skills {
		d := s.Description
		if d == "" {
			d = "(no desc)"
		}
		fmt.Printf("  • %s — %s\n", s.Name, truncStrSI(d, 70))
	}
}

func RemoveSkill(skillDir, name string) error {
	p := filepath.Join(skillDir, name)
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return fmt.Errorf("skill %q not found", name)
	}
	return os.RemoveAll(p)
}

type skillFound struct{ content, skillName, baseDir string }

func parseGitHubURL(input string) (cloneURL, repoName string, err error) {
	input = strings.TrimSuffix(strings.TrimSpace(input), ".git")
	switch {
	case strings.HasPrefix(input, "https://github.com/"):
		parts := strings.Split(strings.TrimPrefix(input, "https://github.com/"), "/")
		if len(parts) >= 2 {
			return input, parts[1], nil
		}
	case strings.HasPrefix(input, "git@github.com:"):
		parts := strings.Split(strings.TrimPrefix(input, "git@github.com:"), "/")
		if len(parts) >= 2 {
			return input, parts[1], nil
		}
	default:
		parts := strings.Split(input, "/")
		if len(parts) == 2 {
			return "https://github.com/" + input, parts[1], nil
		}
	}
	return "", "", fmt.Errorf("unrecognized: %s", input)
}

func scanForSkills(root, repoName string) []skillFound {
	var found []skillFound
	if data, err := os.ReadFile(filepath.Join(root, "SKILL.md")); err == nil {
		n, _, _ := parseSkillFile(string(data))
		found = append(found, skillFound{content: string(data), skillName: n, baseDir: root})
	}
	for _, sub := range []string{"skills", ".claude/skills", ".agents/skills"} {
		entries, err := os.ReadDir(filepath.Join(root, sub))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(root, sub, e.Name(), "SKILL.md")
			if data, err := os.ReadFile(p); err == nil {
				n, _, _ := parseSkillFile(string(data))
				found = append(found, skillFound{content: string(data), skillName: n, baseDir: filepath.Join(root, sub, e.Name())})
			}
		}
	}
	if len(found) == 0 {
		if data, err := os.ReadFile(filepath.Join(root, "CLAUDE.md")); err == nil {
			found = append(found, skillFound{content: string(data), skillName: repoName, baseDir: root})
		}
	}
	return found
}

func truncStrSI(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
