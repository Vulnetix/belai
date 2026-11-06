package patchfmt

import (
	"strings"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/fuzzy"
)

// foldRune maps typographic punctuation to the ASCII a writer would have typed.
// The table is closed. It maps single characters only and never touches a
// letter, a digit, a line terminator or an invisible rune, so line structure
// survives folding and a hidden character in a file is never matched away.
func foldRune(r rune) (string, bool) {
	switch {
	case r >= 0x2018 && r <= 0x201B:
		return "'", true
	case r >= 0x201C && r <= 0x201F:
		return `"`, true
	case r >= 0x2010 && r <= 0x2015, r == 0x2212:
		return "-", true
	case r == 0x00A0, r >= 0x2002 && r <= 0x200A, r == 0x202F, r == 0x205F, r == 0x3000:
		return " ", true
	case r == 0x2026:
		return "...", true
	}
	return "", false
}

// fold returns s with foldRune applied and, for each byte offset of the result,
// the byte offset in s of the rune it came from.
func fold(s string) (string, []int32) {
	var b strings.Builder
	b.Grow(len(s))
	m := make([]int32, 0, len(s)+1)
	for off := 0; off < len(s); {
		r, w := utf8.DecodeRuneInString(s[off:])
		rep, ok := foldRune(r)
		if !ok {
			rep = s[off : off+w]
		}
		b.WriteString(rep)
		for range len(rep) {
			m = append(m, int32(off))
		}
		off += w
	}
	m = append(m, int32(len(s)))
	return b.String(), m
}

// locateFolded is fuzzy.Locate on the folded forms of text and old, with the
// span of a unique match mapped back onto text. ok is false when folding changed
// nothing (so there is nothing new to find) or nothing matched.
func locateFolded(text, old string) (fuzzy.Result, bool) {
	ft, tmap := fold(text)
	fo, _ := fold(old)
	if ft == text && fo == old {
		return fuzzy.Result{}, false
	}
	r := fuzzy.Locate(ft, fo, fuzzy.Options{Relaxed: true, Lines: true})
	if r.Matches == 0 {
		return r, false
	}
	if r.Matches == 1 {
		r.Start, r.End = int(tmap[r.Start]), int(tmap[r.End])
	}
	return r, true
}
