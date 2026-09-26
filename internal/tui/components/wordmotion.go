package components

import "unicode"

// Word-boundary motion for the prompt editor, matching the behaviour of the Pi
// coding agent's editor so the two feel the same under the same keys.
//
// Pi segments the line with Intl.Segmenter and then splits word-like segments
// again at internal punctuation. The observable result is a three-class model,
// which is what this implements: runs of whitespace, runs of word runes, and
// runs of everything else (punctuation and symbols). A motion crosses exactly
// one run, so `foo.bar` stops at the dot rather than stepping over the whole
// token, and `();` is one hop rather than three.
//
// The one deliberate divergence is scripts without spaces: Pi's segmenter
// finds word boundaries inside a run of CJK, and this does not — a run of
// letters is one word here. Belai has no segmenter in the standard library,
// and a wrong boundary would be worse than a coarse one.

type runeClass int

const (
	classSpace runeClass = iota
	classWord
	classOther
)

// classOf buckets a rune into the three classes a motion can cross. The
// underscore counts as a word rune because `foo_bar` is one identifier, which
// matches both Pi (its segmenter joins on underscore) and every editor a
// programmer is used to.
func classOf(r rune) runeClass {
	switch {
	case unicode.IsSpace(r):
		return classSpace
	case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r):
		return classWord
	default:
		return classOther
	}
}

// wordLeft returns the column a word-left motion moves to from col within one
// line. It skips any whitespace immediately behind the cursor, then crosses
// the single run that begins there, so it always lands on the *start* of a
// word rather than one character into it.
//
// col is a rune index and is clamped, so a caller that has drifted out of
// range gets a sane answer instead of a panic.
func wordLeft(line []rune, col int) int {
	if col > len(line) {
		col = len(line)
	}
	if col <= 0 {
		return 0
	}

	i := col
	for i > 0 && classOf(line[i-1]) == classSpace {
		i--
	}
	if i == 0 {
		return 0
	}

	c := classOf(line[i-1])
	for i > 0 && classOf(line[i-1]) == c {
		i--
	}
	return i
}

// wordRight returns the column a word-right motion moves to from col within
// one line. It skips whitespace ahead of the cursor, then crosses the single
// run that starts there, so it lands on the *end* of a word and not on the
// start of the next one — the same asymmetry Pi has, and the one every
// readline-style editor uses.
func wordRight(line []rune, col int) int {
	if col < 0 {
		col = 0
	}
	if col >= len(line) {
		return len(line)
	}

	i := col
	for i < len(line) && classOf(line[i]) == classSpace {
		i++
	}
	if i == len(line) {
		return i
	}

	c := classOf(line[i])
	for i < len(line) && classOf(line[i]) == c {
		i++
	}
	return i
}
