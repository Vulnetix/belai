package shellsafe

import (
	"strings"
)

// Subjects lists every command in the line as a single space-separated string,
// wrappers and the commands they run each on their own. It is what an allow
// rule is matched against: each must be allowed for the line to be.
func (a *Analysis) Subjects() []string {
	out := make([]string, 0, len(a.Commands))
	for _, c := range a.Commands {
		out = append(out, c.Joined())
	}
	return out
}

// DenySubjects is [Analysis.Subjects] plus normalised variants, so a deny rule
// written for the plain form still matches: the program named by its base
// ("/usr/bin/git" as "git"), and git without its global options
// ("git -c x=y push" as "git push"). A line that did not parse contributes its
// separator-split segments instead, so a deny rule still sees each part.
func (a *Analysis) DenySubjects() []string {
	if !a.Parseable {
		return fallbackSegments(a.Source)
	}
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, c := range a.Commands {
		add(c.Joined())
		if len(c.Argv) == 0 {
			continue
		}
		base := append([]string{baseName(c.Argv[0])}, c.Argv[1:]...)
		add(strings.Join(base, " "))
		if opts, ok := globalValueOptions[baseName(c.Argv[0])]; ok {
			add(strings.Join(withoutGlobals(base, opts), " "))
		}
	}
	return out
}

// fallbackSegments splits an unparseable line on the characters that separate
// commands, so deny matching still sees each segment.
func fallbackSegments(src string) []string {
	segs := strings.FieldsFunc(src, func(r rune) bool {
		switch r {
		case ';', '&', '|', '\n', '\r', '(', ')', '`', '{', '}':
			return true
		}
		return false
	})
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		if f := strings.Join(strings.Fields(s), " "); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// globalValueOptions lists, for tools that take global options before a
// subcommand, the ones that consume a value. Stripping them gives the plain
// form ("git push") a deny rule was written for.
var globalValueOptions = map[string]map[string]bool{
	"git": {
		"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true,
		"--exec-path": true, "--config-env": true, "--super-prefix": true, "--attr-source": true,
	},
	"gh":   {"-R": true, "--repo": true, "--hostname": true},
	"glab": {"-R": true, "--repo": true, "-g": true, "--group": true, "--hostname": true},
}

// withoutGlobals drops global options so the subcommand is second.
func withoutGlobals(argv []string, valueOpts map[string]bool) []string {
	out := []string{argv[0]}
	i := 1
	for i < len(argv) {
		a := argv[i]
		switch {
		case valueOpts[a]:
			i += 2
		case strings.HasPrefix(a, "-"):
			i++
		default:
			return append(out, argv[i:]...)
		}
	}
	return out
}

// Simple reports that the line is exactly one plain command: parsed, one
// written command, and nothing a shell would expand, chain, redirect or run
// in the background. Only such a line may run without a shell.
func (a *Analysis) Simple() bool {
	if !a.Parseable || a.Written != 1 {
		return false
	}
	const bad = FlagChain | FlagBackground | FlagSubshell | FlagBlock | FlagCmdSubst | FlagProcSubst |
		FlagRedirect | FlagHeredoc | FlagExpansion | FlagAssign | FlagControl | FlagNewline | FlagDynamic | FlagOpaque
	return !a.Has(bad) && !a.Commands[0].Dynamic
}
