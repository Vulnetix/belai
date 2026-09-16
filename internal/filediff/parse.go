package filediff

import (
	"path/filepath"
	"strings"
)

// Command parsing is the fallback for workspaces that are not git
// repositories. It is deliberately conservative: guessing wrongly about which
// files a command touched produces a confident, incorrect diff, which is worse
// than showing none. Anything it cannot account for returns confident=false,
// and the caller shows no diff rather than a misleading one.
//
// Inside a git repository this code is not used at all — `git status` observes
// what actually changed, whatever the command was.

// maxGlobMatches bounds glob expansion. A pattern matching more than this is
// a sweep, not an edit.
const maxGlobMatches = 64

// formatters rewrite the files named in their arguments in place.
var formatters = map[string]bool{
	"gofmt": true, "goimports": true, "gofumpt": true,
	"prettier": true, "black": true, "isort": true, "yapf": true,
	"rustfmt": true, "clang-format": true, "shfmt": true,
	"terraform": true, "tofu": true, "sqlfluff": true, "ruff": true,
	"eslint": true, "stylelint": true, "dart": true, "swiftformat": true,
}

// inPlaceEditors edit the files named in their trailing arguments.
var inPlaceEditors = map[string]bool{"sed": true, "perl": true, "ruby": true}

// ParseTargets infers which paths a shell command writes to.
//
// confident is false when the command does anything the rules cannot account
// for — an unrecognised program, a variable or substitution in a path, a patch
// whose targets live inside the patch, a formatter pointed at a directory. The
// caller must treat a false here as "no diff available", never as "no files
// changed".
func ParseTargets(command string) (targets []string, confident bool) {
	if strings.TrimSpace(command) == "" {
		return nil, false
	}
	seen := map[string]bool{}
	add := func(p string) bool {
		p = strings.TrimSpace(p)
		if p == "" || hasExpansion(p) {
			return false
		}
		matches, ok := expandGlob(p)
		if !ok {
			return false
		}
		for _, m := range matches {
			if !seen[m] {
				seen[m] = true
				targets = append(targets, m)
			}
		}
		return true
	}

	for _, simple := range splitCommands(command) {
		if len(simple) == 0 {
			continue
		}
		if !parseSimple(simple, add) {
			return nil, false
		}
	}
	return targets, true
}

// parseSimple handles one simple command, returning false when it cannot
// account for what the command writes.
func parseSimple(tokens []string, add func(string) bool) bool {
	// Redirections write regardless of the program, and are stripped before
	// the program itself is considered.
	args, ok := takeRedirections(tokens, add)
	if !ok {
		return false
	}
	if len(args) == 0 {
		return true
	}

	name := filepath.Base(args[0])
	rest := args[1:]

	switch {
	case name == "tee":
		return addNonFlags(rest, add)

	case inPlaceEditors[name]:
		if !hasInPlaceFlag(rest) {
			return true // reading only
		}
		return addTrailingPaths(rest, add)

	case name == "touch", name == "rm", name == "truncate", name == "unlink":
		return addNonFlags(rest, add)

	case name == "mv":
		// Both ends change: the source disappears, the destination appears.
		return addNonFlags(rest, add)

	case name == "cp", name == "install", name == "ln":
		paths := nonFlags(rest)
		if len(paths) == 0 {
			return true
		}
		return add(paths[len(paths)-1])

	case name == "dd":
		for _, a := range rest {
			if v, found := strings.CutPrefix(a, "of="); found {
				return add(v)
			}
		}
		return true

	case name == "mkdir", name == "chmod", name == "chown", name == "ls",
		name == "cat", name == "echo", name == "printf", name == "grep",
		name == "find", name == "head", name == "tail", name == "wc",
		name == "diff", name == "which", name == "pwd", name == "true":
		// Read-only, or metadata-only: nothing to diff.
		return true

	case formatters[name]:
		return addFormatterTargets(rest, add)

	case name == "patch":
		// The targets are named inside the patch, not on the command line.
		return false
	}

	// git apply / git checkout / git stash and friends rewrite files whose
	// names are not on the command line; anything else is simply unknown.
	return false
}

// addFormatterTargets adds a formatter's path arguments. A directory argument
// means an unbounded rewrite, which cannot be enumerated cheaply.
func addFormatterTargets(rest []string, add func(string) bool) bool {
	paths := nonFlags(rest)
	if len(paths) == 0 {
		return false
	}
	for _, p := range paths {
		if isDir(p) {
			return false
		}
		if !add(p) {
			return false
		}
	}
	return true
}

func addNonFlags(rest []string, add func(string) bool) bool {
	for _, p := range nonFlags(rest) {
		if !add(p) {
			return false
		}
	}
	return true
}

// addTrailingPaths adds the path operands of an in-place editor, which follow
// its flags and its script.
func addTrailingPaths(rest []string, add func(string) bool) bool {
	paths := nonFlags(rest)
	if len(paths) < 2 {
		// Only a script, no file: the edit goes to stdout.
		return true
	}
	for _, p := range paths[1:] {
		if !add(p) {
			return false
		}
	}
	return true
}

func hasInPlaceFlag(args []string) bool {
	for _, a := range args {
		if a == "--in-place" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") &&
			strings.Contains(a, "i")) {
			return true
		}
	}
	return false
}

func nonFlags(args []string) []string {
	var out []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") && a != "-" {
			continue
		}
		out = append(out, a)
	}
	return out
}

// takeRedirections records redirect targets and returns the remaining tokens.
func takeRedirections(tokens []string, add func(string) bool) ([]string, bool) {
	var rest []string
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if t == "<" {
			i++ // input redirection reads; skip its operand
			continue
		}
		if isWriteRedirect(t) {
			// The target is either glued to the operator or the next token.
			if target := writeTarget(t); target != "" {
				if !add(target) {
					return nil, false
				}
				continue
			}
			if i+1 >= len(tokens) {
				return nil, false
			}
			i++
			if !add(tokens[i]) {
				return nil, false
			}
			continue
		}
		rest = append(rest, t)
	}
	return rest, true
}

// isWriteRedirect reports whether a token opens an output redirection: >, >>,
// &>, >|, or a numbered form such as 2>.
func isWriteRedirect(t string) bool {
	s := strings.TrimPrefix(t, "&")
	s = strings.TrimLeft(s, "0123456789")
	return strings.HasPrefix(s, ">")
}

func writeTarget(t string) string {
	s := strings.TrimPrefix(t, "&")
	s = strings.TrimLeft(s, "0123456789")
	s = strings.TrimPrefix(s, ">")
	s = strings.TrimPrefix(s, ">")
	s = strings.TrimPrefix(s, "|")
	return s
}

// hasExpansion reports whether a path depends on something only the shell
// knows, which makes it unusable as a target.
func hasExpansion(p string) bool {
	return strings.ContainsAny(p, "$`") || strings.Contains(p, "~")
}

// splitCommands tokenises a command line and splits it at separators, skipping
// heredoc bodies so their contents are never parsed as shell.
func splitCommands(s string) [][]string {
	var out [][]string
	var cur []string
	var tok strings.Builder
	var heredocs []string

	flushTok := func() {
		if tok.Len() > 0 {
			cur = append(cur, tok.String())
			tok.Reset()
		}
	}
	flushCmd := func() {
		flushTok()
		if len(cur) > 0 {
			out = append(out, cur)
			cur = nil
		}
	}

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch c {
		case '\'', '"':
			// Quoted runs are taken literally, including separators.
			quote := c
			i++
			for i < len(runes) && runes[i] != quote {
				if runes[i] == '\\' && quote == '"' && i+1 < len(runes) {
					i++
				}
				tok.WriteRune(runes[i])
				i++
			}
		case '\\':
			if i+1 < len(runes) {
				i++
				tok.WriteRune(runes[i])
			}
		case ' ', '\t':
			flushTok()
		case '\n', ';':
			flushCmd()
			// A heredoc body starts after the newline that ends its command.
			if c == '\n' && len(heredocs) > 0 {
				i = skipHeredoc(runes, i+1, heredocs[0]) - 1
				heredocs = heredocs[1:]
			}
		case '&', '|':
			// && and || separate; a single | pipes. Both end the command.
			// But &> opens a redirection, and >| is the clobbering form of >,
			// so neither of those is a separator.
			if c == '|' && strings.HasSuffix(tok.String(), ">") {
				tok.WriteRune(c)
				continue
			}
			if c == '&' && i+1 < len(runes) && runes[i+1] == '>' {
				tok.WriteRune(c)
				continue
			}
			if i+1 < len(runes) && runes[i+1] == c {
				i++
			}
			flushCmd()
		case '<':
			if i+1 < len(runes) && runes[i+1] == '<' {
				i += 2
				if i < len(runes) && runes[i] == '-' {
					i++
				}
				delim, next := readHeredocDelim(runes, i)
				heredocs = append(heredocs, delim)
				i = next - 1
				continue
			}
			flushTok()
			cur = append(cur, "<")
		default:
			tok.WriteRune(c)
		}
	}
	flushCmd()
	return out
}

// readHeredocDelim reads the delimiter word after <<, which may be quoted.
func readHeredocDelim(runes []rune, i int) (string, int) {
	for i < len(runes) && (runes[i] == ' ' || runes[i] == '\t') {
		i++
	}
	var b strings.Builder
	for i < len(runes) && runes[i] != ' ' && runes[i] != '\t' && runes[i] != '\n' && runes[i] != ';' {
		if runes[i] != '\'' && runes[i] != '"' {
			b.WriteRune(runes[i])
		}
		i++
	}
	return b.String(), i
}

// skipHeredoc returns the index just past the heredoc body's terminator.
func skipHeredoc(runes []rune, i int, delim string) int {
	for i < len(runes) {
		start := i
		for i < len(runes) && runes[i] != '\n' {
			i++
		}
		line := strings.TrimSpace(string(runes[start:i]))
		i++ // consume the newline
		if line == delim {
			return i
		}
	}
	return len(runes)
}
