package sanitize

import (
	"regexp"
	"strings"
)

// DecisionText is text cleaned for a decision backend. It can only be built by
// [ForDecision], so a function that takes one has a compile-time guarantee the
// right sanitiser ran. The zero value is empty and valid.
type DecisionText struct{ s string }

// String returns the cleaned text.
func (d DecisionText) String() string { return d.s }

// Len is the byte length of the cleaned text.
func (d DecisionText) Len() int { return len(d.s) }

var (
	// specialOpenRe finds a chat special-token opener: an angle bracket in any
	// width followed (after optional space) by a letter, slash, bang, question
	// mark or vertical bar in either width, e.g. "<|im_start|>", "<s>", "</s>".
	specialOpenRe = regexp.MustCompile(`[<\x{FF1C}\x{FE64}]\s*([A-Za-z/!?|\x{FF5C}])`)
	// layoutLineRe finds a line that looks like part of a decision prompt's own
	// layout: "Question", "Options:", "Answer", "Context:" or an "(A)" option.
	layoutLineRe = regexp.MustCompile(`(?im)^[ \t]*(?:question|options\s*:|answer|context\s*:|\([A-P]\))`)
	// bracketTokenRe finds instruction-format markers such as [INST].
	bracketTokenRe = regexp.MustCompile(`\[\s*(/?)\s*(?i:inst|sys)\s*\]`)
)

// ForDecision cleans text that becomes part of a decision request's state or
// question. On top of [Text] it splits chat special-token openers so they
// cannot tokenise as control tokens, prefixes any line that imitates the
// prompt's own layout, and caps the result at max runes (zero or less means no
// cap). Decision backends answer only with numbers, so the goal is that
// content cannot forge prompt structure, not that it be readable.
func ForDecision(s string, max int) DecisionText {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = Text(s)
	s = specialOpenRe.ReplaceAllString(s, "< $1")
	s = bracketTokenRe.ReplaceAllString(s, "[ ${1}inst ]")
	s = layoutLineRe.ReplaceAllStringFunc(s, func(m string) string { return "| " + m })
	return DecisionText{s: capRunes(s, max, "")}
}
