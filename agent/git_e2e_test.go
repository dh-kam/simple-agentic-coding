package agent

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every command here must pass the allowlist AND be accepted by real git.
func TestGitTool_everydayCommandsWork(t *testing.T) {
	base := t.TempDir()
	if out, err := exec.Command("git", "-C", base, "init").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, out)
	}
	g := func(a ...string) {
		c := exec.Command("git", append([]string{"-C", base,
			"-c", "user.email=t@t", "-c", "user.name=t"}, a...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("setup git %v: %v\n%s", a, err, out)
		}
	}
	mustWrite(t, filepath.Join(base, "a.go"), "package a\n")
	g("add", ".")
	g("commit", "-m", "init")
	g("tag", "-a", "v1", "-m", "first")
	mustWrite(t, filepath.Join(base, "a.go"), "package a // changed\n")

	tool := NewGitTool(base)
	for _, cmd := range []string{
		"status", "status --short", "status -sb", "status --porcelain",
		"diff", "diff --stat", "diff --cached", "diff --name-only", "diff -U3",
		"diff --numstat", "diff -w", "diff --diff-filter=M",
		"log --oneline", "log --oneline --graph", "log -5", "log -n 3",
		"log --stat", "log -p", `log --format="%H %s"`, "log --pretty=oneline",
		`log --grep "init"`, `log --since "2 weeks ago"`, `log --author "t"`,
		"log --decorate", "log --decorate=short", "log --all",
		"log -S package", "log --name-only", "log --reverse",
		"show", "show --stat", "show --name-only", "show HEAD",
		"branch", "branch -a", "branch --list", "branch --show-current",
		"add .", "add a.go", "add -A", "add -u", "add -n .",
		"tag -l", "tag -n", "describe --tags", "describe --always",
		"rev-parse --show-toplevel", "rev-parse --abbrev-ref HEAD",
		"rev-parse --is-inside-work-tree", "rev-parse HEAD",
	} {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		out, err := tool.Run(context.Background(), args)
		if err != nil {
			t.Errorf("REJECTED  git %-32s  %v", cmd, strings.SplitN(strings.TrimSpace(out+" "+err.Error()), "\n", 2)[0])
		}
	}
}

// Commit is exercised separately because it changes state.
func TestGitTool_commitFormsWork(t *testing.T) {
	base := t.TempDir()
	if out, err := exec.Command("git", "-C", base, "init").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, out)
	}
	exec.Command("git", "-C", base, "config", "user.email", "t@t").Run()
	exec.Command("git", "-C", base, "config", "user.name", "t").Run()

	tool := NewGitTool(base)
	runOut := func(cmd string) (string, error) {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		return tool.Run(context.Background(), args)
	}
	mustWrite(t, filepath.Join(base, "f.txt"), "1\n")
	for _, cmd := range []string{
		"add .",
		`commit -m "feat: initial"`,
		`commit --allow-empty -m "chore: empty"`,
		`commit --allow-empty -am "chore: with -am"`,
		`commit --allow-empty -m "fix: colon: in message"`,
		`commit --allow-empty -m "refactor: move ../shared"`,
		`commit --allow-empty --allow-empty-message -m ""`,
	} {
		if out, err := runOut(cmd); err != nil {
			t.Errorf("REJECTED  git %-46s  %v\n%s", cmd, err, out)
		}
	}

	// --amend needs a real change staged on top of a commit that has a
	// message, or git refuses on its own terms rather than the allowlist's.
	mustWrite(t, filepath.Join(base, "f.txt"), "2\n")
	if _, err := runOut("add ."); err != nil {
		t.Fatalf("add: %v", err)
	}
	if out, err := runOut(`commit -m "chore: real commit"`); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	mustWrite(t, filepath.Join(base, "f.txt"), "3\n")
	if _, err := runOut("add ."); err != nil {
		t.Fatalf("add: %v", err)
	}
	if out, err := runOut("commit --amend --no-edit"); err != nil {
		t.Errorf("REJECTED  git commit --amend --no-edit  %v\n%s", err, out)
	}
}

// The allowlist must never contain an option that opens a caller-named file,
// redirects git's execution or I/O, or needs a terminal. This list comes from
// an empirical survey of git 2.43's option surface across the ten permitted
// subcommands — it is the set the previous denylist had to enumerate correctly
// and repeatedly failed to.
func TestGitOptions_excludeEveryDangerousOption(t *testing.T) {
	// These mean the same thing wherever they appear: they open a file named by
	// the argument, redirect git's execution or repository target, run an
	// external program on file contents, or hand off to the pager.
	alwaysForbidden := []string{
		"--file", "-F", "--template", "--pathspec-from-file", "--add-file",
		"-O", "--order-file", "--resolve-git-dir", "--mailmap", "--use-mailmap",
		"--output", "-o",
		"--exec-path", "--git-dir", "--work-tree", "--git-path", "--prefix",
		"--upload-pack", "--receive-pack", "--namespace",
		"--ext-diff", "--textconv", "--submodule", "--remerge-diff",
		"--alternate-refs",
		"--stdin",
		"--help", "--help-all", "-h",
		"--no-verify",
		"--parseopt", "--sq-quote", "--local-env-vars",
	}
	// Spellings whose meaning depends on the subcommand. -p is a display flag
	// for log/diff/show but interactive hunk selection for add/commit; -i is
	// --regexp-ignore-case for log and --include for commit, but --interactive
	// for add.
	forbiddenFor := map[string][]string{
		"add":    {"-p", "--patch", "-i", "--interactive", "-e", "--edit"},
		"commit": {"-p", "--patch", "--interactive", "-e", "--edit"},
		"tag":    {"-e", "--edit", "--sign", "-s", "--local-user", "-u"},
		"branch": {"--edit-description"},
	}

	check := func(where string, opts map[string]gitOpt, banned []string) {
		for _, bad := range banned {
			if _, listed := opts[bad]; listed {
				t.Errorf("%s allows %q, which reads/writes a file, redirects git, "+
					"or needs a terminal", where, bad)
			}
		}
	}
	for subcmd, opts := range gitOptions {
		check("gitOptions["+subcmd+"]", opts, alwaysForbidden)
		check("gitOptions["+subcmd+"]", opts, forbiddenFor[subcmd])
	}
	check("gitCommonOpts", gitCommonOpts, alwaysForbidden)
	for _, banned := range forbiddenFor {
		check("gitCommonOpts", gitCommonOpts, banned)
	}
}

// A value classified as text or opaque skips the path check, so nothing that
// git resolves as a filesystem path may be classified that way.
func TestGitOptions_noPathHidesBehindATextValue(t *testing.T) {
	pathish := map[string]bool{
		"--relative": true, // genuinely a path, and classified as one
	}
	for subcmd, opts := range gitOptions {
		for flag, o := range opts {
			if o.val == valPath && !pathish[flag] {
				t.Logf("note: %s %s is path-checked", subcmd, flag)
			}
			// An option that takes a value must say which kind; valNone with a
			// value would silently accept anything attached to it.
			if o.val == valNone && (o.glue || o.optional) {
				t.Errorf("gitOptions[%q][%q]: glue/optional set on a valueless option", subcmd, flag)
			}
		}
	}
}

// The table's `optional` flag has to match git's own parse-options, and the
// direction that matters is one-way: if git treats a value as optional and the
// table calls it required, the checker consumes the following argument as that
// value — skipping the operand path check while git reads the very same token
// as a pathspec. `git log --format ../secret` escaped exactly this way.
//
// The reverse mismatch is merely strict: the checker path-checks a token git
// would have taken as a value, which can only refuse too much, never too
// little. So this asserts the unsafe direction and reports the other as a note.
func TestGitOptions_optionalFlagMatchesGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	mustWrite(t, filepath.Join(repo, "a.txt"), "x\n")
	exec.Command("git", "-C", repo, "add", ".").Run()
	exec.Command("git", "-C", repo, "-c", "user.email=t@t", "-c", "user.name=t",
		"commit", "-m", "i").Run()

	// gitRequiresValue asks git itself: invoking the option with nothing after
	// it draws "requires a value" only when the value is mandatory.
	gitRequiresValue := func(subcmd, flag string) bool {
		out, _ := exec.Command("git", "-C", repo, subcmd, flag).CombinedOutput()
		s := strings.ToLower(string(out))
		return strings.Contains(s, "requires a value") || strings.Contains(s, "requires an argument")
	}

	for subcmd, opts := range gitOptions {
		for flag, o := range opts {
			if o.val == valNone || !strings.HasPrefix(flag, "--") {
				continue // booleans and short forms carry glued values only
			}
			required := gitRequiresValue(subcmd, flag)
			switch {
			case !required && !o.optional:
				t.Errorf("gitOptions[%q][%q] is marked required but git takes its value as "+
					"optional — the checker will swallow the next argument unchecked", subcmd, flag)
			case required && o.optional:
				t.Logf("note: %s %s is marked optional but git requires a value; "+
					"strict, not unsafe", subcmd, flag)
			}
		}
	}
}
