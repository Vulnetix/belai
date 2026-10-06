package shellsafe

import (
	"fmt"
	"sort"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// RewriteRule replaces the words that open a simple command: when a command's
// argv starts with Match, those words are replaced by Replace. It is the unit
// of the user's bash_rewrite table (docs/bash-rewrite.md). Both sides are
// plain words, so a replacement can never carry shell syntax.
type RewriteRule struct {
	Match   []string
	Replace []string
}

// String renders the rule as `match -> replace` for harness notes.
func (r RewriteRule) String() string {
	return strings.Join(r.Match, " ") + " -> " + strings.Join(r.Replace, " ")
}

// MaxRewriteWords bounds each side of a rule.
const MaxRewriteWords = 8

// plainWord reports whether a word is made only of characters the shell treats
// literally and that cannot begin an expansion, a quote, a redirection or a
// separator.
func plainWord(w string) bool {
	if w == "" || len(w) > 128 {
		return false
	}
	for i := 0; i < len(w); i++ {
		c := w[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("._-/@+:=,", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// ValidRewriteRule reports why a rule is unusable: an empty or oversized side,
// a word that is not plain, a first word that is an option or an assignment, or
// a rule that replaces a command with itself.
func ValidRewriteRule(r RewriteRule) error {
	for _, side := range []struct {
		name  string
		words []string
	}{{"match", r.Match}, {"replace", r.Replace}} {
		if len(side.words) == 0 {
			return fmt.Errorf("%s is empty", side.name)
		}
		if len(side.words) > MaxRewriteWords {
			return fmt.Errorf("%s has more than %d words", side.name, MaxRewriteWords)
		}
		for _, w := range side.words {
			if !plainWord(w) {
				return fmt.Errorf("%s word %q must be plain: letters, digits and . _ - / @ + : = ,", side.name, w)
			}
		}
		if first := side.words[0]; strings.HasPrefix(first, "-") || strings.Contains(first, "=") {
			return fmt.Errorf("%s must start with a command name, not %q", side.name, first)
		}
	}
	if strings.Join(r.Match, " ") == strings.Join(r.Replace, " ") {
		return fmt.Errorf("match and replace are the same")
	}
	return nil
}

// matches reports whether argv opens with the rule's match words. The program
// word matches by exact text, or by base name when the rule names a bare
// command, so `npm` also matches `/usr/bin/npm`. Every matched word must be a
// fixed literal.
func (r RewriteRule) matches(argv []string, fixed []bool) bool {
	if len(argv) < len(r.Match) {
		return false
	}
	for i, m := range r.Match {
		if !fixed[i] {
			return false
		}
		if i == 0 && !strings.Contains(m, "/") {
			if baseName(argv[0]) != m {
				return false
			}
			continue
		}
		if argv[i] != m {
			return false
		}
	}
	return true
}

type splice struct {
	start, end int
	text       string
	rule       RewriteRule
}

// Rewrite applies the first matching rule to each simple command written in
// the command position of src, found by parsing it, never by searching its
// text. A command held by a wrapper (env, sudo, sh -c, ...) is not rewritten.
//
// It fails closed: the result is parsed again and must have the same shape
// (same commands written, same shell properties) as the line it came from, or
// src is returned untouched with a reason. A line that does not parse is
// returned untouched with no reason: there is nothing to rewrite. applied lists
// the rule used for each command changed, in source order. The caller still
// owes the rewritten line every permission check.
func Rewrite(src string, rules []RewriteRule) (out string, applied []RewriteRule, why string) {
	if len(rules) == 0 {
		return src, nil, ""
	}
	a := Analyze(src)
	if !a.Parseable {
		return src, nil, ""
	}
	// Analyze trims the source; offsets are into that trimmed text.
	text := a.Source
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(text), "")
	if err != nil {
		return src, nil, ""
	}
	w := &walker{a: &Analysis{}, src: text}
	var edits []splice
	syntax.Walk(file, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		argv := make([]string, len(call.Args))
		fixed := make([]bool, len(call.Args))
		for i, word := range call.Args {
			s, dyn, glob := w.word(word)
			argv[i], fixed[i] = s, !dyn && !glob
		}
		for _, r := range rules {
			if !r.matches(argv, fixed) {
				continue
			}
			start := int(call.Args[0].Pos().Offset())
			end := int(call.Args[len(r.Match)-1].End().Offset())
			if start < 0 || end > len(text) || start >= end {
				return true
			}
			edits = append(edits, splice{start: start, end: end, text: strings.Join(r.Replace, " "), rule: r})
			break
		}
		return true
	})
	if len(edits) == 0 {
		return src, nil, ""
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var b strings.Builder
	pos := 0
	for _, e := range edits {
		if e.start < pos {
			return src, nil, "overlapping rewrites"
		}
		b.WriteString(text[pos:e.start])
		b.WriteString(e.text)
		pos = e.end
		applied = append(applied, e.rule)
	}
	b.WriteString(text[pos:])
	out = b.String()

	// Parse the result again and require the same shape.
	after := Analyze(out)
	switch {
	case !after.Parseable:
		return src, nil, "the rewritten line does not parse"
	case after.Flags != a.Flags:
		return src, nil, "the rewrite changed the line's shell properties"
	case after.Written != a.Written:
		return src, nil, "the rewrite changed the number of commands"
	}
	return out, applied, ""
}
