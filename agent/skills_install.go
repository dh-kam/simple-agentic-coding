package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// InstallFromGitHub clones a GitHub repo and installs any skills found into
// skillDir. Supports multiple repo layouts:
//
//  1. Root SKILL.md (single-skill repo)
//  2. skills/*/SKILL.md (collection)
//  3. .claude/skills/*/SKILL.md (Claude Code layout)
//  4. .agents/skills/*/SKILL.md (cross-platform)
//  5. CLAUDE.md fallback (treated as a skill named after the repo)
//
// Returns the names of installed skills.
func InstallFromGitHub(input, skillDir string) ([]string, error) {
	cloneURL, repoName, err := parseGitHubURL(input)
	if err != nil {
		return nil, err
	}

	tmpDir, err := os.MkdirTemp("", "agentic-skill-*")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	cmd := exec.Command("git", "clone", "--depth", "1", cloneURL, tmpDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git clone %s: %w\n%s", cloneURL, err, out)
	}

	found := scanForSkills(tmpDir, repoName)
	if len(found) == 0 {
		return nil, fmt.Errorf("no SKILL.md or CLAUDE.md found in %s", input)
	}

	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return nil, err
	}

	var installed []string
	for _, s := range found {
		name := s.skillName
		if name == "" {
			name = repoName
		}
		dest := filepath.Join(skillDir, name)
		_ = os.RemoveAll(dest) // overwrite if exists
		if err := os.MkdirAll(dest, 0o755); err != nil {
			continue
		}
		content := s.content
		// If the file has no frontmatter, add a minimal one.
		if !strings.HasPrefix(strings.TrimSpace(content), "---") {
			content = fmt.Sprintf("---\nname: %s\ndescription: (installed from %s)\n---\n\n%s", name, input, content)
		}
		if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte(content), 0o644); err != nil {
			continue
		}
		// Copy references/ if exists.
		refSrc := filepath.Join(s.baseDir, "references")
		if _, err := os.Stat(refSrc); err == nil {
			copyDir(refSrc, filepath.Join(dest, "references"))
		}
		installed = append(installed, name)
	}
	return installed, nil
}

// ListInstalledSkills prints installed skills to stdout.
func ListInstalledSkills(skillDir string) {
	skills, err := LoadSkills(skillDir)
	if err != nil || len(skills) == 0 {
		fmt.Println("  (no skills installed)")
		fmt.Printf("\n  Install with: agentic skills install <github-url>\n")
		return
	}
	fmt.Printf("  %d skill(s) installed in %s:\n\n", len(skills), skillDir)
	for _, s := range skills {
		desc := s.Description
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Printf("  • %s — %s\n", s.Name, truncStrLocal(desc, 70))
	}
	fmt.Println("\n  Remove with: agentic skills remove <name>")
}

// RemoveSkill deletes a skill directory by name.
func RemoveSkill(skillDir, name string) error {
	path := filepath.Join(skillDir, name)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("skill %q not found in %s", name, skillDir)
	}
	return os.RemoveAll(path)
}

// --- internals ---

type skillFound struct {
	content   string
	skillName string // from frontmatter or dir name
	baseDir   string // parent dir (for references/)
}

func parseGitHubURL(input string) (cloneURL, repoName string, err error) {
	input = strings.TrimSuffix(input, ".git")
	input = strings.TrimSpace(input)

	switch {
	case strings.HasPrefix(input, "https://github.com/"):
		parts := strings.Split(strings.TrimPrefix(input, "https://github.com/"), "/")
		if len(parts) >= 2 {
			return input, parts[1], nil
		}
	case strings.HasPrefix(input, "git@github.com:"):
		rest := strings.TrimPrefix(input, "git@github.com:")
		parts := strings.Split(rest, "/")
		if len(parts) >= 2 {
			return input, parts[1], nil
		}
	default:
		parts := strings.Split(input, "/")
		if len(parts) == 2 {
			return "https://github.com/" + input, parts[1], nil
		}
	}
	return "", "", fmt.Errorf("unrecognized GitHub URL: %s\n  supported: https://github.com/owner/repo  or  owner/repo", input)
}

func scanForSkills(root, repoName string) []skillFound {
	var found []skillFound

	// Pattern 1: root/SKILL.md
	if data, err := os.ReadFile(filepath.Join(root, "SKILL.md")); err == nil {
		found = append(found, makeSkillFound(string(data), root))
	}

	// Pattern 2-4: */skills/*/SKILL.md (scan common layouts)
	for _, sub := range []string{"skills", ".claude/skills", ".agents/skills"} {
		skillsDir := filepath.Join(root, sub)
		entries, err := os.ReadDir(skillsDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(skillsDir, e.Name(), "SKILL.md")
			if data, err := os.ReadFile(p); err == nil {
				found = append(found, makeSkillFound(string(data), filepath.Join(skillsDir, e.Name())))
			}
		}
	}

	// Pattern 5: CLAUDE.md fallback (only if nothing else found)
	if len(found) == 0 {
		if data, err := os.ReadFile(filepath.Join(root, "CLAUDE.md")); err == nil {
			found = append(found, skillFound{
				content:   string(data),
				skillName: repoName,
				baseDir:   root,
			})
		}
	}

	return found
}

func makeSkillFound(content, baseDir string) skillFound {
	name, _, _ := parseSkillFile(content)
	return skillFound{content: content, skillName: name, baseDir: baseDir}
}

func copyDir(src, dst string) {
	_ = filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			_ = os.MkdirAll(target, 0o755)
		} else {
			if data, err := os.ReadFile(path); err == nil {
				_ = os.WriteFile(target, data, 0o644)
			}
		}
		return nil
	})
}

func truncStrLocal(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
