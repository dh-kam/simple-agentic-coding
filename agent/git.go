package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// gitAllowed lists the subcommands the git tool will run. Deliberately absent:
//
//   - config: writes persist in .git/config after the session and are an
//     execution/exfiltration primitive (core.hooksPath, core.sshCommand,
//     http.proxy, url.*.insteadOf).
//   - remote: `remote add`/`set-url` writes .git/config just as directly,
//     redirecting the next push to an attacker.
//   - stash: silently hides uncommitted work with no diff shown to the user.
//
// add/commit stay, but they are gated: IsMutating puts them behind the TUI's
// approval prompt and behind .agentic/settings.json in one-shot mode.
var gitAllowed = map[string]bool{
	"status": true, "diff": true, "log": true, "branch": true,
	"add": true, "commit": true, "show": true,
	"tag": true, "describe": true, "rev-parse": true,
}

// gitDeniedFlags reach outside the worktree regardless of which subcommand
// carries them: `--no-index` diffs any two files on disk, `--output=` writes
// one anywhere, and the `--file` family reads one into a message or pathspec
// list — all straight through the safePath sandbox.
var gitDeniedFlags = []string{
	"--output", "--no-index", "--exec-path", "--git-dir", "--work-tree",
	"--upload-pack", "--receive-pack", "--ext-diff", "--textconv",
	"--file", "-F", "--pathspec-from-file", "--template", "--add-file",
}

// gitValueFlags are keyed by subcommand because the same letter means
// different things: `-m` is a message for commit and tag, but a boolean
// ("diff for merges") for log, show and diff. Scoping them is what stops
// `log -p -m ../secret.txt` — where -m consumes nothing — from carrying its
// neighbour past the path check.
//
// Only flags whose operand is genuinely free text belong here: a commit
// message or a --grep pattern may legitimately be absolute or contain "..".
var gitValueFlags = map[string]map[string]bool{
	"commit": {"-m": true, "--message": true, "--author": true, "--date": true},
	"tag":    {"-m": true, "--message": true},
	"branch": {"-m": true, "-M": true, "--edit-description": true},
	"log":    {"--grep": true, "--author": true, "--since": true, "--until": true, "--format": true, "--pretty": true},
	"show":   {"--format": true, "--pretty": true},
	"diff":   {"--diff-filter": true},
}

// gitReadsWholeRepo lists subcommands that ignore cmd.Dir and report on the
// entire repository. When base is a subdirectory they would show files the
// rest of the tool surface keeps out of reach, so a "." pathspec is appended
// unless the caller supplied one.
var gitReadsWholeRepo = map[string]bool{"diff": true, "log": true, "show": true}

// gitShortValueFlags are single-letter options that take a value, which git
// also accepts glued to the letter with no separator: `-F/etc/passwd` is one
// token. Splitting only these avoids mangling short option clusters like -am.
//
//	-F file  -L <range>:<file>  -O order-file  -t template
const gitShortValueFlags = "FLOt"

// splitGitFlag separates a token into its option and any value attached to it,
// covering both `--opt=value` and `-Xvalue`.
func splitGitFlag(a string) (flag, attached string) {
	if i := strings.IndexByte(a, '='); i > 0 {
		return a[:i], a[i+1:]
	}
	if len(a) > 2 && a[0] == '-' && a[1] != '-' &&
		strings.IndexByte(gitShortValueFlags, a[1]) >= 0 {
		return a[:2], a[2:]
	}
	return a, ""
}

// isMessageCluster reports whether a short option cluster ends in -m, so the
// next token is a commit or tag message rather than a path. `commit -am "fix:
// crash"` is the everyday form, and its message must not be path-checked.
func isMessageCluster(a string) bool {
	if len(a) < 2 || a[0] != '-' || a[1] == '-' || a[len(a)-1] != 'm' {
		return false
	}
	for _, c := range a[1:] {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z') {
			return false
		}
	}
	return true
}

// checkGitArgs rejects flags and operands that would let an allowlisted
// subcommand read or write outside base. subcmd selects the value-flag set.
func checkGitArgs(subcmd string, parts []string) error {
	valueFlags := gitValueFlags[subcmd]
	skipPathCheck := false
	for _, a := range parts {
		flag, attached := splitGitFlag(a)

		// Denied flags are checked on *every* argument, including one that
		// follows a value flag. Skipping the whole token instead let a boolean
		// use of a value flag smuggle the next one through.
		for _, denied := range gitDeniedFlags {
			if flag == denied {
				return fmt.Errorf("git flag %q not allowed", flag)
			}
		}
		// An attached value hides inside a token that starts with "-", so it
		// never reached the operand checks below: --file=/etc/passwd read any
		// file on the host.
		if attached != "" && !valueFlags[flag] {
			if err := checkGitOperand(attached); err != nil {
				return fmt.Errorf("%s: %w", flag, err)
			}
		}

		if skipPathCheck {
			skipPathCheck = false
			continue
		}
		if valueFlags[flag] || (valueFlags["-m"] && isMessageCluster(a)) {
			skipPathCheck = attached == ""
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		if err := checkGitOperand(a); err != nil {
			return err
		}
	}
	return nil
}

// checkGitOperand rejects an argument that would resolve outside base.
func checkGitOperand(a string) error {
	// Pathspec magic re-anchors a path at the repository root regardless of
	// cmd.Dir, so ":/" and ":(top)" reach above base without containing "..".
	if strings.HasPrefix(a, ":") {
		return fmt.Errorf("pathspec magic %q not allowed", a)
	}
	// <rev>:<path> is resolved from the repository root too, so `show
	// HEAD:secret.txt` reads a tracked file above base.
	if i := strings.IndexByte(a, ':'); i > 0 {
		return fmt.Errorf("rev:path form %q not allowed — use read_file", a)
	}
	if filepath.IsAbs(a) {
		return fmt.Errorf("absolute path %q not allowed", a)
	}
	for _, seg := range strings.Split(filepath.ToSlash(a), "/") {
		if seg == ".." {
			return fmt.Errorf("path %q escapes base", a)
		}
	}
	return nil
}

// confineToBase appends a "." pathspec to a whole-repository subcommand that
// did not name one, so its output covers base and nothing above it.
//
// Only when base is below the repository root. A pathspec also switches on
// git's history simplification, which silently drops merge and empty commits —
// so at the root, where confinement buys nothing, adding one would corrupt the
// history the model reads.
func confineToBase(parts []string, atRepoRoot bool) []string {
	if atRepoRoot || !gitReadsWholeRepo[parts[0]] {
		return parts
	}
	for i, a := range parts[1:] {
		// A bare trailing "--" is an *empty* pathspec, which means the whole
		// repository — treating it as "the caller supplied one" reopened the
		// hole it was meant to close.
		if a == "--" && i+2 < len(parts) {
			return parts
		}
	}
	return append(append([]string{}, parts...), "--", ".")
}

// repoRoot reports whether base is the top level of its git worktree. The
// answer is cached because it costs a subprocess and cannot change during a
// session — but only once it has actually been determined.
type repoRoot struct {
	mu       sync.Mutex
	resolved bool
	is       bool
}

func (r *repoRoot) atRoot(ctx context.Context, base string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resolved {
		return r.is
	}
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = base
	out, err := cmd.Output()
	if err != nil {
		// Not a repository, or this particular call was cancelled. Answer
		// "not at the root" so confinement stays on, but do NOT cache it:
		// caching one cancelled call would keep appending a pathspec at the
		// real root for the rest of the session, and that silently drops merge
		// and empty commits from every log the model reads.
		return false
	}
	top, err1 := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	here, err2 := filepath.EvalSymlinks(base)
	r.is = err1 == nil && err2 == nil && top == here
	r.resolved = true
	return r.is
}

func NewGitTool(base string) Tool {
	var root repoRoot
	return Tool{
		Name: "git",
		Description: "Git 명령을 실행한다. 허용된 subcommand만 실행: status, diff, log, add, commit, branch 등. " +
			"셸을 거치지 않고 git 을 직접 실행하며, 인자는 따옴표(' \")와 백슬래시 이스케이프를 인식해 분리한다. " +
			"절대 경로·상위 경로(..)와 base 밖을 읽고 쓰는 플래그는 거부된다.",
		InputSchema: map[string]any{
			"command": map[string]any{"type": "string", "description": `git subcommand (status, diff, log, add ., commit -m "msg")`},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			if strings.TrimSpace(in.Command) == "" {
				return "", fmt.Errorf("command required")
			}
			parts, err := splitArgs(in.Command)
			if err != nil {
				return "", err
			}
			if len(parts) > 0 && parts[0] == "git" {
				parts = parts[1:]
			}
			if len(parts) == 0 {
				return "", fmt.Errorf("no git subcommand")
			}
			if !gitAllowed[parts[0]] {
				return "", fmt.Errorf("git subcommand %q not allowed", parts[0])
			}
			if err := checkGitArgs(parts[0], parts[1:]); err != nil {
				return "", err
			}
			parts = confineToBase(parts, root.atRoot(ctx, base))
			cctx, cancel := context.WithTimeout(ctx, commandTimeout())
			defer cancel()
			c := exec.CommandContext(cctx, "git", parts...)
			c.Dir = base
			out, err := c.CombinedOutput()
			if err != nil {
				return string(out) + "\n" + err.Error(), err
			}
			return string(out), nil
		},
	}
}

func NewGitCommitTool(base string) Tool {
	return Tool{
		Name:        "git_commit",
		Description: "변경사항을 커밋한다. 자동으로 git add -A 후 커밋.",
		InputSchema: map[string]any{
			"message": map[string]any{"type": "string", "description": "커밋 메시지"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			if in.Message == "" {
				return "", fmt.Errorf("message required")
			}
			cctx, cancel := context.WithTimeout(ctx, commandTimeout())
			defer cancel()
			add := exec.CommandContext(cctx, "git", "add", "-A")
			add.Dir = base
			if out, err := add.CombinedOutput(); err != nil {
				return string(out), fmt.Errorf("git add: %w", err)
			}
			commit := exec.CommandContext(cctx, "git", "commit", "-m", in.Message)
			commit.Dir = base
			out, err := commit.CombinedOutput()
			if err != nil {
				return string(out), err
			}
			return "committed: " + in.Message, nil
		},
	}
}

// splitArgs splits a command line into arguments the way a shell would, so a
// quoted commit message survives as one argument. strings.Fields would turn
// `commit -m "fix: a thing"` into five arguments, two of them carrying literal
// quote characters — which is exactly the form this tool documents.
func splitArgs(s string) ([]string, error) {
	var (
		args    []string
		cur     strings.Builder
		started bool
		quote   rune // 0, '\'' or '"'
	)
	flush := func() {
		if started {
			args = append(args, cur.String())
			cur.Reset()
			started = false
		}
	}
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case quote == 0 && (c == '\'' || c == '"'):
			quote, started = c, true
		case quote != 0 && c == quote:
			quote = 0
		// A backslash escapes outside quotes, but inside double quotes the
		// shell only treats it as an escape before these four characters —
		// otherwise it stays literal. Escaping everything turned `--grep "\d+"`
		// into `d+` and mangled Windows paths in commit messages.
		case c == '\\' && i+1 < len(runes) &&
			(quote == 0 || (quote == '"' && strings.ContainsRune("$`\"\\\n", runes[i+1]))):
			i++
			cur.WriteRune(runes[i])
			started = true
		case quote == 0 && (c == ' ' || c == '\t' || c == '\n'):
			flush()
		default:
			cur.WriteRune(c)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unbalanced %c quote in command", quote)
	}
	flush()
	return args, nil
}
