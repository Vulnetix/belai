package shellsafe

import (
	"strings"
)

// trustedDirs are the directories an absolute command path may name. A path in
// any other place ("/tmp/x/cat", "./cat") could be an attacker's program with a
// familiar name, so it never counts as the allowlisted command.
var trustedDirs = map[string]bool{
	"/bin": true, "/usr/bin": true, "/usr/local/bin": true, "/sbin": true, "/usr/sbin": true,
	"/opt/homebrew/bin": true,
}

// readOnlyBins are the commands that may run read-only. Flags that write or
// execute are refused per command below.
var readOnlyBins = set(
	"cat", "false", "grep", "egrep", "fgrep", "rg", "ls", "uname", "pwd", "head", "tail",
	"wc", "sort", "uniq", "file", "which", "diff", "stat", "du", "basename",
	"dirname", "realpath", "readlink", "jq", "cut", "tr", "nl", "fold",
	"paste", "printenv", "join", "comm", "rev", "shuf", "seq", "sleep", "od", "xxd",
	"base64", "date", "printf", "echo", "tree", "fd", "zcat", "gunzip", "md5sum",
	"sha256sum", "column", "expand", "unexpand",
)

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// deniedFlags are options that write a file or run a program, per command.
// short holds single letters (tested inside a cluster such as -uo), long holds
// full names (tested by exact match, "=value" form and any unambiguous
// abbreviation the option parser would accept).
var deniedFlags = map[string]struct{ short, long string }{
	"sort": {"o", "output compress-program"},
	"shuf": {"o", "output"},
	"tree": {"o", "output"},
	"rg":   {"", "pre pre-glob hostname-bin"},
	"fd":   {"xX", "exec exec-batch"},
	"date": {"s", "set"},
	"file": {"C", "compile"},
}

// maxOperands bounds positional operands for commands whose second operand is
// an output file (uniq IN OUT, xxd IN OUT).
var maxOperands = map[string]int{"uniq": 1, "xxd": 1}

// gitReadSubcommands may run; each gets the flag checks in gitPolicy.
var gitReadSubcommands = set("status", "log", "diff", "show", "rev-parse", "ls-files", "grep", "describe")

// gitValueOptions are the global options that take a value and are allowed.
var gitAllowedGlobalValue = set("-C", "--git-dir", "--work-tree", "--namespace")

// gitDeniedGlobal are global options that run programs or change configuration.
var gitDeniedGlobal = []string{"-c", "--config-env", "--exec-path", "-p", "--paginate", "--html-path", "--man-path", "--info-path", "--super-prefix", "--attr-source"}

// findUnsafe are find primaries that write, delete or run a program.
var findUnsafe = set("-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fls", "-fprintf")

// ReadOnly reports whether src is a single plain command that is on the
// read-only allowlist with none of the flags that write or execute, and
// returns the argv to execute. The returned argv is what was analysed: the
// caller runs exactly it (no shell), so the command that was judged is the
// command that runs. The reason is empty on success.
func ReadOnly(src string) (argv []string, reason string) {
	a := Analyze(src)
	if !a.Parseable {
		return nil, "not a valid single command"
	}
	if !a.Simple() {
		return nil, "not one plain command (no chaining, pipes, redirects, expansions or newlines)"
	}
	argv = append([]string(nil), a.Commands[0].Argv...)
	name := argv[0]
	if strings.Contains(name, "/") {
		dir, base := name[:strings.LastIndex(name, "/")], name[strings.LastIndex(name, "/")+1:]
		if dir == "" || !trustedDirs[dir] || base == "" {
			return nil, "command path is not in a trusted directory"
		}
		name = base
	}
	args := argv[1:]
	switch name {
	case "git":
		out, why := gitPolicy(argv[0], args)
		if why != "" {
			return nil, why
		}
		return out, ""
	case "find":
		for _, f := range args {
			if findUnsafe[f] {
				return nil, "find option " + f + " can write or run a program"
			}
		}
		return argv, ""
	case "env":
		for _, f := range args {
			if strings.HasPrefix(f, "-") || strings.Contains(f, "=") {
				continue
			}
			return nil, "env would run a command"
		}
		for _, f := range args {
			if f == "-u" || f == "--unset" || f == "-S" || f == "-C" || f == "--chdir" {
				return nil, "env option " + f + " takes a command or value"
			}
		}
		return argv, ""
	case "gunzip":
		if !hasAny(args, "-c", "--stdout", "--to-stdout", "-t", "--test", "-l", "--list") {
			return nil, "gunzip decompresses in place unless given -c"
		}
		return argv, ""
	}
	if !readOnlyBins[name] {
		return nil, "command is not on the read-only allowlist"
	}
	if why := checkFlags(name, args); why != "" {
		return nil, why
	}
	if name == "date" {
		for _, a := range args {
			if !strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "+") {
				return nil, "date with a positional argument sets the clock"
			}
		}
	}
	if max, ok := maxOperands[name]; ok {
		n := 0
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				n++
			}
		}
		if n > max {
			return nil, name + " with an output file operand writes"
		}
	}
	return argv, ""
}

func hasAny(args []string, want ...string) bool {
	for _, a := range args {
		for _, w := range want {
			if a == w {
				return true
			}
		}
	}
	return false
}

// checkFlags applies deniedFlags for a command.
func checkFlags(name string, args []string) string {
	d, ok := deniedFlags[name]
	if !ok {
		return ""
	}
	longs := strings.Fields(d.long)
	for _, a := range args {
		if a == "--" {
			return ""
		}
		switch {
		case strings.HasPrefix(a, "--"):
			opt := strings.TrimPrefix(a, "--")
			if i := strings.IndexByte(opt, '='); i >= 0 {
				opt = opt[:i]
			}
			for _, l := range longs {
				if opt != "" && strings.HasPrefix(l, opt) {
					return name + " option --" + l + " can write or run a program"
				}
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			for _, r := range a[1:] {
				if strings.ContainsRune(d.short, r) {
					return name + " option -" + string(r) + " can write or run a program"
				}
			}
		}
	}
	return ""
}

// gitPolicy checks a git invocation and returns the argv to run: global
// options that run programs are refused, the subcommand must be read-only,
// its flags that write a file or open a pager are refused, and hardening
// options are added so repository configuration cannot run a program.
func gitPolicy(program string, args []string) ([]string, string) {
	i := 0
	var globals []string
	for i < len(args) {
		a := args[i]
		name, _, _ := strings.Cut(a, "=")
		for _, d := range gitDeniedGlobal {
			if name == d {
				return nil, "git option " + d + " can run a program or change configuration"
			}
		}
		if gitAllowedGlobalValue[a] {
			if i+1 >= len(args) {
				return nil, "git option " + a + " needs a value"
			}
			globals = append(globals, a, args[i+1])
			i += 2
			continue
		}
		if strings.HasPrefix(a, "-") {
			globals = append(globals, a)
			i++
			continue
		}
		break
	}
	if i >= len(args) {
		return nil, "git needs a subcommand"
	}
	sub, rest := args[i], args[i+1:]
	if !gitReadSubcommands[sub] {
		return nil, "git " + sub + " is not read-only"
	}
	for _, f := range rest {
		if f == "--" {
			break
		}
		name, _, _ := strings.Cut(f, "=")
		switch {
		case name == "--output", name == "--ext-diff", name == "--open-files-in-pager", name == "--textconv":
			return nil, "git " + sub + " option " + name + " can write or run a program"
		case sub == "grep" && strings.HasPrefix(f, "-O") && !strings.HasPrefix(f, "--"):
			return nil, "git grep -O opens a pager"
		case strings.HasPrefix(f, "--output"):
			return nil, "git " + sub + " option --output writes a file"
		}
	}
	// Repository configuration can name programs (core.fsmonitor, diff
	// external drivers, textconv). Turn them off; the model cannot.
	out := []string{program, "-c", "core.fsmonitor=false", "-c", "core.pager=cat"}
	out = append(out, globals...)
	out = append(out, sub)
	if sub == "diff" || sub == "log" || sub == "show" {
		out = append(out, "--no-ext-diff", "--no-textconv")
	}
	return append(out, rest...), ""
}
