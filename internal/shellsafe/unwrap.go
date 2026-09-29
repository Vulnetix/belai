package shellsafe

import (
	"path"
	"strings"
)

// shells run a payload given with -c.
var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "ash": true, "fish": true}

// valueOptions lists, per wrapper, the options that consume the next word, so
// the word is not mistaken for the wrapped command.
var valueOptions = map[string]map[string]bool{
	"env":     {"-u": true, "--unset": true, "-C": true, "--chdir": true},
	"nice":    {"-n": true, "--adjustment": true},
	"ionice":  {"-c": true, "-n": true, "-p": true, "-P": true, "-u": true},
	"stdbuf":  {"-i": true, "-o": true, "-e": true},
	"timeout": {"-s": true, "--signal": true, "-k": true, "--kill-after": true},
	"sudo":    {"-u": true, "-g": true, "-h": true, "-p": true, "-C": true, "-D": true, "-R": true, "-T": true, "-U": true, "--user": true, "--group": true},
	"doas":    {"-u": true, "-C": true},
	"xargs":   {"-I": true, "-i": true, "-n": true, "-P": true, "-d": true, "-a": true, "-E": true, "-L": true, "-s": true, "--max-args": true, "--max-procs": true, "--delimiter": true, "--arg-file": true, "--replace": true},
	"flock":   {"-w": true, "-E": true, "--timeout": true},
	"chroot":  {"--userspec": true},
}

// simpleWrappers run the rest of their arguments as a command.
var simpleWrappers = map[string]bool{
	"env": true, "command": true, "builtin": true, "exec": true, "nohup": true, "nice": true, "ionice": true,
	"setsid": true, "stdbuf": true, "timeout": true, "sudo": true, "doas": true, "xargs": true, "flock": true,
	"time": true, "watch": true, "busybox": true, "unbuffer": true,
}

// baseName is the final path element of a command word.
func baseName(word string) string { return path.Base(word) }

// unwrap looks inside a command for commands it runs: wrappers (env, sudo,
// xargs, ...), shells given a payload (sh -c), eval, and find -exec. Each
// inner command is appended to the analysis. A payload that cannot be seen
// into sets [FlagOpaque].
func unwrap(a *Analysis, argv []string, depth int) {
	if len(argv) == 0 {
		return
	}
	name := baseName(argv[0])
	switch {
	case shells[name]:
		payload, ok := shellPayload(argv[1:])
		if ok {
			nested(a, payload, depth)
		}
	case name == "eval":
		nested(a, strings.Join(argv[1:], " "), depth)
	case name == "find":
		findExec(a, argv[1:], depth)
	case simpleWrappers[name]:
		inner, ok := wrapped(name, argv[1:])
		if !ok {
			a.Flags |= FlagOpaque
			return
		}
		if len(inner) == 0 {
			return
		}
		a.Commands = append(a.Commands, Command{Argv: inner, Unwrapped: true, Dynamic: dynamicWords(inner)})
		unwrap(a, inner, depth)
	}
}

func dynamicWords(argv []string) bool {
	for _, w := range argv {
		if strings.ContainsAny(w, "$`") {
			return true
		}
	}
	return false
}

// nested analyses a shell payload one level deeper and folds its commands and
// flags into a.
func nested(a *Analysis, payload string, depth int) {
	if depth+1 > maxDepth {
		a.Flags |= FlagOpaque
		return
	}
	sub := analyze(payload, depth+1)
	if !sub.Parseable {
		a.Flags |= FlagOpaque
		return
	}
	a.Flags |= sub.Flags
	for _, c := range sub.Commands {
		c.Unwrapped = true
		a.Commands = append(a.Commands, c)
	}
}

// shellPayload returns the script given to a shell with -c, including the
// clustered forms -lc and -ec.
func shellPayload(args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" || !strings.HasPrefix(a, "-") {
			return "", false
		}
		if strings.HasPrefix(a, "--") {
			continue
		}
		if strings.Contains(a[1:], "c") && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// wrapped skips a wrapper's own options and assignments and returns the
// command it runs. ok is false when the wrapper takes an option this package
// does not understand well enough to find the command (env -S, a timeout with
// no duration).
func wrapped(name string, args []string) ([]string, bool) {
	vals := valueOptions[name]
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--":
			i++
			return finish(name, args[i:])
		case name == "env" && (a == "-S" || strings.HasPrefix(a, "--split-string") || strings.HasPrefix(a, "-S")):
			return nil, false
		case strings.HasPrefix(a, "-") && len(a) > 1:
			if vals[a] {
				i += 2
				continue
			}
			i++
		case name == "env" && strings.Contains(a, "=") && !strings.HasPrefix(a, "="):
			i++
		default:
			return finish(name, args[i:])
		}
	}
	return nil, true
}

// finish handles wrappers with a leading positional before the command.
func finish(name string, rest []string) ([]string, bool) {
	switch name {
	case "timeout":
		if len(rest) == 0 {
			return nil, false
		}
		rest = rest[1:]
	case "flock":
		if len(rest) == 0 {
			return nil, false
		}
		rest = rest[1:]
	case "busybox":
		// busybox NAME args: NAME is the command.
	case "watch":
		// watch takes a command string; treat the words as the command.
	}
	return rest, true
}

// findExec extracts the commands run by find -exec, -execdir, -ok and -okdir,
// which end at ";" or "+".
func findExec(a *Analysis, args []string, depth int) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-exec", "-execdir", "-ok", "-okdir":
			j := i + 1
			for j < len(args) && args[j] != ";" && args[j] != `\;` && args[j] != "+" {
				j++
			}
			if j > i+1 {
				inner := args[i+1 : j]
				a.Commands = append(a.Commands, Command{Argv: inner, Unwrapped: true, Dynamic: dynamicWords(inner)})
				unwrap(a, inner, depth)
			}
			i = j
		}
	}
}
