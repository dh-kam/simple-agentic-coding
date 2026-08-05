package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	return installFromClone(tmpDir, repoName, skillDir, input)
}

// installFromClone copies the skills found in an already-cloned tree into
// skillDir. Split out from InstallFromGitHub so the confinement it enforces can
// be tested without a network fetch.
func installFromClone(tmpDir, repoName, skillDir, input string) ([]string, error) {
	found := scanForSkills(tmpDir, repoName)
	if len(found) == 0 {
		return nil, fmt.Errorf("no SKILL.md found in %s", input)
	}
	os.MkdirAll(skillDir, 0755)
	var installed []string
	for _, s := range found {
		// The name comes out of the cloned repository's own SKILL.md, so it is
		// attacker-controlled. Unchecked it flowed into filepath.Join and then
		// into os.RemoveAll: `name: ../../../x` deleted a directory anywhere on
		// disk and installed the skill there.
		name, err := safeSkillName(s.skillName, repoName)
		if err != nil {
			return installed, err
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
		fmt.Printf("  • %s — %s\n", s.Name, TruncRunes(d, 70))
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

// safeSkillName reduces a name declared by a downloaded repository to a single
// harmless path segment, falling back to the repository name.
func safeSkillName(declared, fallback string) (string, error) {
	for _, candidate := range []string{declared, fallback} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || candidate == "." || candidate == ".." {
			continue
		}
		// Reject rather than sanitize: a name that needs rewriting is not one
		// the author chose, and silently installing under a different name
		// hides the attempt.
		if candidate != filepath.Base(candidate) ||
			strings.ContainsAny(candidate, `/\`) ||
			strings.HasPrefix(candidate, ".") {
			continue
		}
		return candidate, nil
	}
	return "", fmt.Errorf("unusable skill name %q", declared)
}

// repoSegment matches a GitHub owner or repository name. Anchoring the whole
// segment is what stops "owner/.." from becoming a repoName that escapes
// skillDir when a repo declares no skill name of its own.
var repoSegment = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func parseGitHubURL(input string) (cloneURL, repoName string, err error) {
	input = strings.TrimSuffix(strings.TrimSpace(input), ".git")
	var parts []string
	switch {
	case strings.HasPrefix(input, "https://github.com/"):
		parts = strings.Split(strings.TrimPrefix(input, "https://github.com/"), "/")
	case strings.HasPrefix(input, "git@github.com:"):
		parts = strings.Split(strings.TrimPrefix(input, "git@github.com:"), "/")
	default:
		if p := strings.Split(input, "/"); len(p) == 2 {
			parts = p
			input = "https://github.com/" + input
		}
	}
	if len(parts) < 2 {
		return "", "", fmt.Errorf("unrecognized: %s", input)
	}
	for _, seg := range parts[:2] {
		if !repoSegment.MatchString(seg) || seg == "." || seg == ".." {
			return "", "", fmt.Errorf("invalid owner/repo segment %q", seg)
		}
	}
	return input, parts[1], nil
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
