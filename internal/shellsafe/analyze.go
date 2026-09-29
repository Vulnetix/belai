// Package shellsafe decides what a shell command line is, by parsing it.
//
// The harness used to judge a command by splitting it on whitespace and
// rejecting a few characters. That cannot see quoting, wrappers or nesting: a
// deny rule for "git push" missed `"git" push`, `git -c x=y push`,
// `env git push` and `sh -c 'git push'`, and an allow rule written for
// `git status*` also matched `git status; rm -rf x`. This package parses the
// line with a real shell grammar (mvdan.cc/sh) and answers three questions:
//
//   - What simple commands does it contain, once quotes are removed and
//     wrappers such as env, sudo, xargs, sh -c and find -exec are unwrapped?
//     ([Analyze], [Analysis.Subjects], [Analysis.DenySubjects]).
//   - Is it one plain command with nothing the shell would expand or chain, so
//     that it can run without a shell? ([ReadOnly]).
//   - If so, is that command, with these flags, read-only?
//
// Anything it cannot parse or cannot see through is reported as such and the
// callers fail closed: it never matches an allow rule and never runs as
// read-only. The package holds no state and asks no model anything.
package shellsafe

import (
	"errors"
	"strings"
	"unicode/utf8"

	"mvdan.cc/sh/v3/syntax"
)

// Flag is a property of a command line that the shell would act on.
type Flag uint32

const (
	// FlagChain is more than one statement or a ; && || | |& connector.
	FlagChain Flag = 1 << iota
	// FlagBackground is a trailing & or a coprocess.
	FlagBackground
	// FlagSubshell is a ( ... ) group.
	FlagSubshell
	// FlagBlock is a { ... } group.
	FlagBlock
	// FlagCmdSubst is $( ... ) or a backtick substitution.
	FlagCmdSubst
	// FlagProcSubst is <( ... ) or >( ... ).
	FlagProcSubst
	// FlagRedirect is any redirection.
	FlagRedirect
	// FlagWriteRedirect is a redirection that can create or truncate a file.
	FlagWriteRedirect
	// FlagHeredoc is a here-document or here-string.
	FlagHeredoc
	// FlagExpansion is a parameter, arithmetic, extended-glob or ANSI-C
	// expansion.
	FlagExpansion
	// FlagAssign is a leading NAME=value assignment.
	FlagAssign
	// FlagControl is a compound construct: if, while, for, case, function,
	// test, declaration, let, time, negation.
	FlagControl
	// FlagNewline is a raw line break or Unicode line separator in the source.
	FlagNewline
	// FlagDynamic is a word whose value is not fixed by the source.
	FlagDynamic
	// FlagOpaque is a wrapper payload this package could not see into.
	FlagOpaque
	// FlagGlob is an unquoted glob character (informational).
	FlagGlob
)

// lineBreaks are the characters that end a line: LF, CR, NEL, and the Unicode
// line and paragraph separators. Built from code points so the source holds no
// invisible characters.
var lineBreaks = string([]rune{'\n', '\r', 0x85, 0x2028, 0x2029})

// maxDepth bounds nested shell payloads (sh -c 'sh -c ...').
const maxDepth = 3

// maxSource bounds the command line that is analysed.
const maxSource = 64 << 10

// Command is one simple command.
type Command struct {
	// Argv is the words after quote removal. A dynamic word keeps its source
	// text.
	Argv []string
	// Unwrapped marks a command found inside a wrapper (env, sh -c, ...) rather
	// than written directly.
	Unwrapped bool
	// Dynamic reports that some word is not a fixed literal.
	Dynamic bool
}

// Joined is the command as a single space-separated string.
func (c Command) Joined() string { return strings.Join(c.Argv, " ") }

// Analysis is the result of parsing one command line.
type Analysis struct {
	// Parseable is false when the source is not valid bash or holds control
	// bytes; nothing else is meaningful then.
	Parseable bool
	// Flags collects every property found, including those of nested payloads.
	Flags Flag
	// Commands lists every simple command: written ones first, in source order,
	// then unwrapped ones.
	Commands []Command
	// Written counts the commands written directly in the source.
	Written int
	// Source is the trimmed command line that was analysed.
	Source string
}

// Has reports whether any of the flags is set.
func (a *Analysis) Has(f Flag) bool { return a.Flags&f != 0 }

// Clean reports why src cannot be a command line at all: empty, oversized,
// not valid UTF-8, or holding control bytes other than tab and line breaks. It
// does not judge the shell syntax; a full-shell command is free to be any
// valid script, and [Analyze] reports what it contains.
func Clean(src string) error {
	switch {
	case strings.TrimSpace(src) == "":
		return errors.New("empty command")
	case len(src) > maxSource:
		return errors.New("command too long")
	case !utf8.ValidString(src):
		return errors.New("command is not valid UTF-8")
	}
	for i := 0; i < len(src); i++ {
		if c := src[i]; c == 0 || c < ' ' && c != '\t' && c != '\n' && c != '\r' || c == 0x7f {
			return errors.New("command contains a control character")
		}
	}
	return nil
}

// Analyze parses src as bash and reports what it contains.
func Analyze(src string) *Analysis {
	return analyze(src, 0)
}

func analyze(src string, depth int) *Analysis {
	a := &Analysis{Source: strings.TrimSpace(src)}
	if a.Source == "" || len(a.Source) > maxSource || !utf8.ValidString(a.Source) {
		return a
	}
	for i := 0; i < len(a.Source); i++ {
		if c := a.Source[i]; c == 0 || c < ' ' && c != '\t' && c != '\n' && c != '\r' || c == 0x7f {
			return a
		}
	}
	if strings.ContainsAny(a.Source, lineBreaks) {
		a.Flags |= FlagNewline
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(a.Source), "")
	if err != nil {
		return a
	}
	a.Parseable = true
	if len(file.Stmts) > 1 {
		a.Flags |= FlagChain
	}
	w := &walker{a: a, src: a.Source}
	syntax.Walk(file, w.visit)
	a.Written = len(a.Commands)
	for _, c := range a.Commands[:a.Written:a.Written] {
		unwrap(a, c.Argv, depth)
	}
	return a
}

type walker struct {
	a   *Analysis
	src string
}

func (w *walker) visit(n syntax.Node) bool {
	a := w.a
	switch n := n.(type) {
	case *syntax.Stmt:
		if n.Background {
			a.Flags |= FlagBackground
		}
		if n.Coprocess {
			a.Flags |= FlagBackground | FlagControl
		}
		if n.Negated {
			a.Flags |= FlagControl
		}
		for _, r := range n.Redirs {
			a.Flags |= FlagRedirect
			switch r.Op {
			case syntax.RdrOut, syntax.AppOut, syntax.RdrInOut, syntax.ClbOut, syntax.RdrAll, syntax.AppAll:
				a.Flags |= FlagWriteRedirect
			case syntax.DplOut, syntax.DplIn:
				if r.Word != nil && !fdTarget(r.Word) {
					a.Flags |= FlagWriteRedirect
				}
			case syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
				a.Flags |= FlagHeredoc
			}
		}
	case *syntax.CallExpr:
		if len(n.Assigns) > 0 {
			a.Flags |= FlagAssign
		}
		if len(n.Args) > 0 {
			c := Command{}
			for _, word := range n.Args {
				s, dyn, glob := w.word(word)
				c.Argv = append(c.Argv, s)
				c.Dynamic = c.Dynamic || dyn
				if glob {
					a.Flags |= FlagGlob
				}
			}
			if c.Dynamic {
				a.Flags |= FlagDynamic
			}
			a.Commands = append(a.Commands, c)
		}
	case *syntax.BinaryCmd:
		a.Flags |= FlagChain
	case *syntax.Subshell:
		a.Flags |= FlagSubshell
	case *syntax.Block:
		a.Flags |= FlagBlock
	case *syntax.IfClause, *syntax.WhileClause, *syntax.ForClause, *syntax.CaseClause, *syntax.FuncDecl,
		*syntax.ArithmCmd, *syntax.TestClause, *syntax.DeclClause, *syntax.LetClause, *syntax.TimeClause,
		*syntax.CoprocClause, *syntax.TestDecl:
		a.Flags |= FlagControl
	case *syntax.CmdSubst:
		a.Flags |= FlagCmdSubst
	case *syntax.ProcSubst:
		a.Flags |= FlagProcSubst
	case *syntax.ParamExp, *syntax.ArithmExp, *syntax.ExtGlob:
		a.Flags |= FlagExpansion
	case *syntax.SglQuoted:
		if n.Dollar {
			a.Flags |= FlagExpansion
		}
	}
	return true
}

// fdTarget reports a duplication target that is a file descriptor number or
// "-", as in 2>&1 or 0<&-.
func fdTarget(w *syntax.Word) bool {
	if len(w.Parts) != 1 {
		return false
	}
	l, ok := w.Parts[0].(*syntax.Lit)
	if !ok || l.Value == "" {
		return false
	}
	if l.Value == "-" {
		return true
	}
	for i := 0; i < len(l.Value); i++ {
		if l.Value[i] < '0' || l.Value[i] > '9' {
			return false
		}
	}
	return true
}

// word returns the literal value of a word with quotes removed. A word with
// any expansion is dynamic and is returned as its source text.
func (w *walker) word(word *syntax.Word) (value string, dynamic, glob bool) {
	var b strings.Builder
	for _, p := range word.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			s := unescapeBare(p.Value)
			if strings.ContainsAny(p.Value, "*?[") {
				glob = true
			}
			b.WriteString(s)
		case *syntax.SglQuoted:
			if p.Dollar {
				dynamic = true
			}
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				l, ok := q.(*syntax.Lit)
				if !ok {
					dynamic = true
					continue
				}
				b.WriteString(unescapeDouble(l.Value))
			}
		default:
			dynamic = true
		}
	}
	if dynamic {
		return w.text(word), true, glob
	}
	return b.String(), false, glob
}

func (w *walker) text(n syntax.Node) string {
	s, e := int(n.Pos().Offset()), int(n.End().Offset())
	if s < 0 || e > len(w.src) || s > e {
		return ""
	}
	return w.src[s:e]
}

// unescapeBare removes the backslash escapes of an unquoted literal.
func unescapeBare(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			if s[i] == '\n' {
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// unescapeDouble removes the escapes that are special inside double quotes.
func unescapeDouble(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("$`\"\\\n", s[i+1]) >= 0 {
			i++
			if s[i] == '\n' {
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
