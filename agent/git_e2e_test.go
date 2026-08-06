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
		"--parseopt", "--sq-quote", "--local-env-vars",
	}
	// Spellings whose meaning depends on the subcommand. -p is a display flag
	// for log/diff/show but interactive hunk selection for add/commit; -i is
	// --regexp-ignore-case for log and --include for commit, but --interactive
	// for add.
	// rev-parse's --no-verify negates "check that the object name is valid" and
	// is harmless; only commit's skips the user's hooks.
	forbiddenFor := map[string][]string{
		"add":    {"-p", "--patch", "-i", "--interactive", "-e", "--edit"},
		"commit": {"-p", "--patch", "--interactive", "-e", "--edit", "--no-verify", "--verify"},
		"tag":    {"-e", "--edit", "--sign", "-s", "--local-user", "-u", "--verify"},
		"branch": {"--edit-description"},
	}

	// Reachability, not table membership: lookupGitOpt also admits --no-X
	// whenever --X is listed, so checking the map alone would miss an option
	// resurrected through its negation.
	check := func(subcmd string, banned []string) {
		for _, bad := range banned {
			if _, ok := lookupGitOpt(subcmd, bad); ok {
				t.Errorf("git %s accepts %q, which reads/writes a file, redirects git, "+
					"needs a terminal, or bypasses the user's hooks", subcmd, bad)
			}
		}
	}
	for subcmd := range gitOptions {
		check(subcmd, alwaysForbidden)
		check(subcmd, forbiddenFor[subcmd])
	}
}

// A value classified as text or opaque skips the path check entirely, so no
// option whose value git resolves as a filesystem path may be classified that
// way. Asserting the classification by inspection proved worthless — the
// earlier version of this test only logged — so this feeds each such option a
// path to a file above base and checks that its contents never come back.
func TestGitOptions_noPathHidesBehindATextValue(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	_, base := gitRepoWithSubdir(t)
	tool := NewGitTool(base)

	const secret = "ROOT-SECRET-ABOVE-BASE"
	for _, victim := range []string{"../secret.txt", "/etc/hostname"} {
		for subcmd, opts := range gitOptions {
			for flag, o := range opts {
				if o.val != valText && o.val != valOpaque {
					continue // valPath and valRef are path-checked already
				}
				var forms []string
				if strings.HasPrefix(flag, "--") {
					forms = append(forms, subcmd+" "+flag+"="+victim)
					if !o.optional {
						forms = append(forms, subcmd+" "+flag+" "+victim)
					}
				} else if o.glue {
					forms = append(forms, subcmd+" "+flag+victim)
				}
				for _, cmd := range forms {
					args, _ := json.Marshal(map[string]string{"command": cmd})
					out, _ := tool.Run(context.Background(), args)
					if strings.Contains(out, secret) {
						t.Errorf("git %s leaked a file above base — %q is classified as "+
							"free text but git reads it as a path:\n%s", cmd, flag, out)
					}
				}
			}
		}
	}
}

// Options whose value git resolves as a path must be classified valPath, so it
// goes through checkGitOperand. The leak probe above cannot see this one:
// --relative only rewrites the prefix diff prints, so misclassifying it leaks
// no content — but it would still let a path past the operand rules.
func TestGitOptions_knownPathValuesAreClassifiedAsPaths(t *testing.T) {
	pathValued := map[string][]string{
		"diff": {"--relative"},
		"log":  {"--relative"},
		"show": {"--relative"},
	}
	for subcmd, flags := range pathValued {
		for _, flag := range flags {
			o, ok := gitOptions[subcmd][flag]
			if !ok {
				continue // dropping it entirely is fine; misclassifying it is not
			}
			if o.val != valPath {
				t.Errorf("gitOptions[%q][%q] takes a path but is classified %v", subcmd, flag, o.val)
			}
		}
	}
}

// An option that carries no value must not be marked glue or optional; those
// only describe how a value arrives.
func TestGitOptions_valuelessOptionsCarryNoValueHints(t *testing.T) {
	for subcmd, opts := range gitOptions {
		for flag, o := range opts {
			if o.val == valNone && (o.glue || o.optional) {
				t.Errorf("gitOptions[%q][%q]: glue/optional set on a valueless option", subcmd, flag)
			}
		}
	}
}

// gitDeniedFlags is documented as a second line of defence behind the
// allowlist. Without this, deleting it entirely would break no test.
func TestGitDeniedFlags_areConsulted(t *testing.T) {
	if len(gitDeniedFlags) == 0 {
		t.Fatal("the denylist is empty; the second line of defence is gone")
	}
	for _, flag := range gitDeniedFlags {
		if err := gitFlagDenied(flag); err == nil {
			t.Errorf("gitFlagDenied(%q) returned nil", flag)
		}
	}
	// and it fires even when the allowlist would have admitted the option
	saved := gitOptions["log"]["--output"]
	gitOptions["log"]["--output"] = gitOpt{val: valText}
	defer func() {
		if saved == (gitOpt{}) {
			delete(gitOptions["log"], "--output")
		} else {
			gitOptions["log"]["--output"] = saved
		}
	}()
	if _, err := checkGitArgs("log", []string{"--output=/tmp/x"}, true); err == nil {
		t.Error("an option added to the allowlist by mistake was not caught by the denylist")
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
	// it draws "requires a value" only when the value is mandatory. This works
	// for short options too, which an earlier version of this test skipped on
	// the mistaken grounds that short forms only ever take glued values — the
	// distinction it exists to catch applies to them equally.
	gitRequiresValue := func(subcmd, flag string) bool {
		out, _ := exec.Command("git", "-C", repo, subcmd, flag).CombinedOutput()
		s := strings.ToLower(string(out))
		return strings.Contains(s, "requires a value") || strings.Contains(s, "requires an argument")
	}

	tables := map[string]map[string]gitOpt{"": gitCommonOpts}
	for subcmd, opts := range gitOptions {
		tables[subcmd] = opts
	}
	for subcmd, opts := range tables {
		probeWith := subcmd
		if probeWith == "" {
			probeWith = "log" // gitCommonOpts applies everywhere; log accepts them
		}
		for flag, o := range opts {
			if o.val == valNone {
				continue
			}
			_ = probeWith
			required := gitRequiresValue(probeWith, flag)
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

// Confinement rests on a "." pathspec, and a pathspec only constrains path
// resolution. An operand that names an object directly sidesteps it: `show
// HEAD^{tree}` lists the whole worktree and `show <blob-sha>` prints a file
// outright — and `status --porcelain=v2` hands out the object ids of files
// above base to feed it.
func TestGitTool_objectOperandsCannotEscapeBase(t *testing.T) {
	repo, base := gitRepoWithSubdir(t)
	tool := NewGitTool(base)
	run := func(cmd string) (string, error) {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		return tool.Run(context.Background(), args)
	}

	// the object id of a file above base, obtained outside the tool
	sha, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD:secret.txt").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	blob := strings.TrimSpace(string(sha))

	for _, cmd := range []string{
		"show HEAD^{tree}",
		"show HEAD^{tree}:secret.txt",
		"show " + blob,
		"show " + blob[:8],
		"log " + blob,
		"diff " + blob,
	} {
		out, err := run(cmd)
		if err == nil {
			t.Errorf("git %s was allowed:\n%s", cmd, out)
		}
		if strings.Contains(out, "ROOT-SECRET-ABOVE-BASE") {
			t.Errorf("git %s leaked a file above base:\n%s", cmd, out)
		}
	}

	// and the id must not be obtainable through the tool in the first place
	for _, cmd := range []string{"status --porcelain=v2", "status --porcelain", "status --short"} {
		out, err := run(cmd)
		if err != nil {
			continue
		}
		if strings.Contains(out, "secret.txt") {
			t.Errorf("git %s named a file above base:\n%s", cmd, out)
		}
	}
}

// add -A and add -u act on the whole worktree from any subdirectory, so
// without a pathspec they pull files above base into the object database.
func TestGitTool_addCannotStageAboveBase(t *testing.T) {
	repo, base := gitRepoWithSubdir(t)
	tool := NewGitTool(base)

	for _, cmd := range []string{"add -A", "add -u", "add ."} {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		if out, err := tool.Run(context.Background(), args); err != nil {
			t.Fatalf("git %s: %v\n%s", cmd, err, out)
		}
		staged, _ := exec.Command("git", "-C", repo, "diff", "--cached", "--name-only").Output()
		if strings.Contains(string(staged), "secret.txt") {
			t.Errorf("git %s staged a file above base:\n%s", cmd, staged)
		}
		exec.Command("git", "-C", repo, "reset", "-q").Run()
	}
}

// tag's -v is --verify, which shells out to gpg — not --verbose. Sharing the
// short letter through gitCommonOpts let it past the table that excludes
// --verify for tag.
func TestGitTool_shortOptionsAreSubcommandScoped(t *testing.T) {
	base := t.TempDir()
	if _, ok := lookupGitOpt("tag", "-v"); ok {
		t.Error("tag -v is --verify and must not be admitted as --verbose")
	}
	args, _ := json.Marshal(map[string]string{"command": "tag -v v1"})
	if _, err := NewGitTool(base).Run(context.Background(), args); err == nil {
		t.Error("git tag -v was allowed")
	}
	// the letters that really do mean verbose/quiet still work
	for _, sub := range []string{"status", "add", "commit", "branch"} {
		if _, ok := lookupGitOpt(sub, "-v"); !ok {
			t.Errorf("%s -v (verbose) was lost", sub)
		}
	}
}
