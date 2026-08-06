package agent

// This file holds the option table the git tool allows through, and the
// classification of what each option's value is.
//
// It is an allowlist, and a deliberately small one. git 2.43 exposes well over
// nine hundred options across the ten permitted subcommands; reproducing that
// surface would be unreviewable and would go stale with every release. What is
// listed here is the subset a coding assistant actually uses. Everything else
// is refused with a pointer to run_command, which is itself behind the approval
// gate — so a refusal redirects rather than blocks.
//
// The previous design was a denylist of dangerous flags, and five review passes
// found five ways around it (--no-index, --file=, a glued -F<file>, a bare
// trailing --, and --pathspec-from-file). An empirical survey of git 2.43 then
// turned up more that the denylist never named: --stdin, --mailmap,
// --remerge-diff, --alternate-refs, --resolve-git-dir, --git-path, --prefix and
// -O<order-file>. Enumerating what is safe fails closed; enumerating what is
// dangerous only ever fails open.
//
// Deliberately excluded beyond the obvious file readers and writers:
//   - interactive and editor-launching forms (-p/--patch, -i/--interactive,
//     -e/--edit): the tool has no terminal, so these can only fail.
//   - --no-verify: skipping the user's hooks is not the agent's call.
//   - --help: hands off to the pager.

// gitValue says what an option's value is, which decides how it is checked.
type gitValue int

const (
	valNone   gitValue = iota // boolean flag; must not carry a value
	valText                   // free text — a message, pattern or format string
	valRef                    // a commit, branch or tag name
	valPath                   // a pathspec or filename; must stay inside base
	valOpaque                 // a number, mode or enum: not a path, not free text
)

// gitOpt describes one allowed option.
type gitOpt struct {
	val gitValue
	// glue marks a short option whose value git accepts with no separator
	// (-n5). Long options always accept --opt=value, so this only matters for
	// the single-letter forms.
	glue bool
	// optional marks a value git takes only when it is attached (--decorate
	// vs --decorate=short, -M vs -M50). Such an option must never consume the
	// following argument, or `git log --decorate ../secret` would hand the
	// path to the flag instead of path-checking it as an operand.
	optional bool
}

// gitCommonOpts are accepted for every allowlisted subcommand, so only
// spellings that mean the same thing everywhere belong here.
//
// The short forms deliberately do not: `-v` is --verbose for status, add,
// commit and branch, but --verify for tag, where it shells out to gpg. Putting
// it here admitted `git tag -v` past the per-subcommand table that excludes
// --verify. Short letters are listed per subcommand instead.
var gitCommonOpts = map[string]gitOpt{
	"--color":    {val: valOpaque, optional: true},
	"--no-color": {},
}

// verboseQuiet is the -v/-q pair, for the subcommands where those letters
// really do mean verbose and quiet.
var verboseQuiet = map[string]gitOpt{
	"--verbose": {},
	"-v":        {},
	"--quiet":   {},
	"-q":        {},
}

// gitNumericShorthand lists subcommands where a bare -<n> means --max-count=<n>.
var gitNumericShorthand = map[string]bool{"log": true, "show": true}

// diffDisplayOpts are the presentation flags shared by diff, log and show.
var diffDisplayOpts = map[string]gitOpt{
	"--patch":               {},
	"-p":                    {},
	"--no-patch":            {},
	"-s":                    {},
	"--stat":                {val: valOpaque, optional: true},
	"--numstat":             {},
	"--shortstat":           {},
	"--summary":             {},
	"--name-only":           {},
	"--name-status":         {},
	"--check":               {},
	"--unified":             {val: valOpaque, optional: true},
	"-U":                    {val: valOpaque, glue: true, optional: true},
	"--word-diff":           {val: valOpaque, optional: true},
	"--color-words":         {val: valText, optional: true},
	"--find-renames":        {val: valOpaque, optional: true},
	"-M":                    {val: valOpaque, glue: true, optional: true},
	"--find-copies":         {val: valOpaque, optional: true},
	"-C":                    {val: valOpaque, glue: true, optional: true},
	"--diff-filter":         {val: valOpaque},
	"--ignore-all-space":    {},
	"-w":                    {},
	"--ignore-space-change": {},
	"-b":                    {},
	"--ignore-blank-lines":  {},
	"--minimal":             {},
	"--histogram":           {},
	"--patience":            {},
	"--irreversible-delete": {},
	"--full-index":          {},
	"--binary":              {},
	"--abbrev":              {val: valOpaque, optional: true},
	"-R":                    {},
	"--src-prefix":          {val: valText},
	"--dst-prefix":          {val: valText},
	"--no-prefix":           {},
	"--relative":            {val: valPath, optional: true},
}

// revDisplayOpts are the commit-formatting flags shared by log and show.
var revDisplayOpts = map[string]gitOpt{
	"--oneline":          {},
	"--format":           {val: valText, optional: true},
	"--pretty":           {val: valText, optional: true},
	"--date":             {val: valText},
	"--abbrev-commit":    {},
	"--no-abbrev-commit": {},
	"--decorate":         {val: valOpaque, optional: true},
	"--no-decorate":      {},
	"--parents":          {},
	"--children":         {},
	"--show-signature":   {},
}

// gitOptions is the per-subcommand allowlist.
var gitOptions = map[string]map[string]gitOpt{
	"status": mergeGitOpts(verboseQuiet, map[string]gitOpt{
		"--short":           {},
		"-s":                {},
		"--long":            {},
		"--branch":          {},
		"-b":                {},
		"--porcelain":       {val: valOpaque, optional: true},
		"--show-stash":      {},
		"--untracked-files": {val: valOpaque, optional: true},
		"-u":                {val: valOpaque, glue: true, optional: true},
		"--ignored":         {val: valOpaque, optional: true},
		"--find-renames":    {val: valOpaque, optional: true},
		"--no-renames":      {},
		"--column":          {val: valOpaque, optional: true},
	}),

	"diff": mergeGitOpts(diffDisplayOpts, map[string]gitOpt{
		"--cached":     {},
		"--staged":     {},
		"--merge-base": {},
	}),

	"log": mergeGitOpts(diffDisplayOpts, revDisplayOpts, map[string]gitOpt{
		"--quiet":              {},
		"-q":                   {},
		"--graph":              {},
		"--max-count":          {val: valOpaque},
		"-n":                   {val: valOpaque, glue: true},
		"--skip":               {val: valOpaque},
		"--since":              {val: valText},
		"--after":              {val: valText},
		"--until":              {val: valText},
		"--before":             {val: valText},
		"--author":             {val: valText},
		"--committer":          {val: valText},
		"--grep":               {val: valText},
		"--all-match":          {},
		"--invert-grep":        {},
		"--regexp-ignore-case": {},
		"-i":                   {},
		"--extended-regexp":    {},
		"-E":                   {},
		"--fixed-strings":      {}, // log's -F spelling is blocked by gitDeniedFlags, where -F means --file for commit/tag
		"--perl-regexp":        {},
		"-S":                   {val: valText, glue: true},
		"-G":                   {val: valText, glue: true},
		"--pickaxe-regex":      {},
		"--pickaxe-all":        {},
		"--all":                {},
		"--branches":           {val: valText, optional: true},
		"--tags":               {val: valText, optional: true},
		"--remotes":            {val: valText, optional: true},
		"--merges":             {},
		"--no-merges":          {},
		"--min-parents":        {val: valOpaque, optional: true},
		"--max-parents":        {val: valOpaque, optional: true},
		"--first-parent":       {},
		"--follow":             {},
		"--reverse":            {},
		"--topo-order":         {},
		"--date-order":         {},
		"--author-date-order":  {},
		"--simplify-merges":    {},
		"--full-history":       {},
		"--cherry-pick":        {},
		"--left-right":         {},
		"--merge":              {},
		"--walk-reflogs":       {},
		"-g":                   {},
	}),

	"show": mergeGitOpts(diffDisplayOpts, revDisplayOpts, map[string]gitOpt{
		"--quiet":    {},
		"-q":         {},
		"--no-notes": {},
	}),

	"branch": mergeGitOpts(verboseQuiet, map[string]gitOpt{
		"--list":            {},
		"-l":                {},
		"--all":             {},
		"-a":                {},
		"--remotes":         {},
		"-r":                {},
		"--show-current":    {},
		"--contains":        {val: valRef, optional: true},
		"--no-contains":     {val: valRef, optional: true},
		"--merged":          {val: valRef, optional: true},
		"--no-merged":       {val: valRef, optional: true},
		"--points-at":       {val: valRef},
		"--sort":            {val: valText},
		"--format":          {val: valText},
		"--delete":          {},
		"-d":                {},
		"-D":                {},
		"--move":            {},
		"-m":                {},
		"-M":                {},
		"--copy":            {},
		"-c":                {},
		"--force":           {},
		"-f":                {},
		"--track":           {val: valOpaque, optional: true},
		"--no-track":        {},
		"--set-upstream-to": {val: valRef},
		"-u":                {val: valRef}, // for branch -u is --set-upstream-to
		"--unset-upstream":  {},
		"-t":                {val: valOpaque, optional: true}, // --track
	}),

	"add": mergeGitOpts(verboseQuiet, map[string]gitOpt{
		"--all":            {},
		"-A":               {},
		"--no-all":         {},
		"--update":         {},
		"-u":               {},
		"--dry-run":        {},
		"-n":               {},
		"--force":          {},
		"-f":               {},
		"--intent-to-add":  {},
		"-N":               {},
		"--refresh":        {},
		"--renormalize":    {},
		"--ignore-removal": {},
		"--ignore-errors":  {},
		"--ignore-missing": {},
		"--sparse":         {},
		"--chmod":          {val: valOpaque},
	}),

	"commit": mergeGitOpts(verboseQuiet, map[string]gitOpt{
		"--message":             {val: valText, glue: true},
		"-m":                    {val: valText, glue: true},
		"--all":                 {},
		"-a":                    {},
		"--amend":               {},
		"--no-edit":             {},
		"--allow-empty":         {},
		"--allow-empty-message": {},
		"--author":              {val: valText},
		"--date":                {val: valText},
		"--signoff":             {},
		"-s":                    {}, // for commit -s is --signoff, not --no-patch
		"--no-signoff":          {},
		"--gpg-sign":            {val: valOpaque, optional: true},
		"-S":                    {val: valOpaque, glue: true, optional: true},
		"--no-gpg-sign":         {},
		"--dry-run":             {},
		"--short":               {},
		"--porcelain":           {},
		"--long":                {},
		"--null":                {},
		"--untracked-files":     {val: valOpaque, optional: true},
		"--reset-author":        {},
		"--only":                {},
		"--include":             {},
		"-i":                    {},
	}),

	"tag": {
		"--list":        {},
		"-l":            {},
		"--annotate":    {},
		"-a":            {},
		"--message":     {val: valText, glue: true},
		"-m":            {val: valText, glue: true},
		"--delete":      {},
		"-d":            {},
		"--force":       {},
		"-f":            {},
		"-n":            {val: valOpaque, glue: true, optional: true},
		"--sort":        {val: valText},
		"--format":      {val: valText},
		"--contains":    {val: valRef, optional: true},
		"--merged":      {val: valRef, optional: true},
		"--no-merged":   {val: valRef, optional: true},
		"--points-at":   {val: valRef, optional: true},
		"--ignore-case": {},
	},

	"describe": {
		"--tags":         {},
		"--all":          {},
		"--long":         {},
		"--always":       {},
		"--abbrev":       {val: valOpaque, optional: true},
		"--dirty":        {val: valText, optional: true},
		"--broken":       {val: valText, optional: true},
		"--contains":     {},
		"--first-parent": {},
		"--match":        {val: valText},
		"--exclude":      {val: valText},
		"--candidates":   {val: valOpaque},
		"--exact-match":  {},
		"--debug":        {},
	},

	"rev-parse": {
		"--abbrev-ref":            {val: valOpaque, optional: true},
		"--short":                 {val: valOpaque, optional: true},
		"--symbolic":              {},
		"--symbolic-full-name":    {},
		"--verify":                {},
		"--revs-only":             {},
		"--no-revs":               {},
		"--flags":                 {},
		"--no-flags":              {},
		"--show-toplevel":         {},
		"--show-prefix":           {},
		"--show-cdup":             {},
		"--absolute-git-dir":      {},
		"--is-inside-work-tree":   {},
		"--is-inside-git-dir":     {},
		"--is-bare-repository":    {},
		"--is-shallow-repository": {},
		"--all":                   {},
		"--branches":              {},
		"--tags":                  {},
		"--remotes":               {},
	},
}

// mergeGitOpts combines option sets; later entries win.
func mergeGitOpts(sets ...map[string]gitOpt) map[string]gitOpt {
	out := map[string]gitOpt{}
	for _, s := range sets {
		for k, v := range s {
			out[k] = v
		}
	}
	return out
}

// lookupGitOpt resolves a flag for a subcommand, honouring --no- negations.
// A negated form never carries a value, whatever the positive form does.
func lookupGitOpt(subcmd, flag string) (gitOpt, bool) {
	if o, ok := gitOptions[subcmd][flag]; ok {
		return o, true
	}
	if o, ok := gitCommonOpts[flag]; ok {
		return o, true
	}
	if positive, found := negatedGitFlag(flag); found {
		if _, ok := gitOptions[subcmd][positive]; ok {
			return gitOpt{}, true
		}
		if _, ok := gitCommonOpts[positive]; ok {
			return gitOpt{}, true
		}
	}
	return gitOpt{}, false
}

// negatedGitFlag turns "--no-foo" into "--foo".
func negatedGitFlag(flag string) (string, bool) {
	const prefix = "--no-"
	if len(flag) > len(prefix) && flag[:len(prefix)] == prefix {
		return "--" + flag[len(prefix):], true
	}
	return "", false
}
