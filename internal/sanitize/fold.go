package sanitize

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/vulnetix/belai/internal/delimiters"
)

// A unit is one source symbol of the text being scanned: a single rune, or a
// whole HTML character reference. Scanning happens on the folded form of the
// units (case, compatibility and entity folded, invisible runes dropped), and
// removal happens on the original bytes the units cover, so the text that
// survives is never rewritten, only cut.
type unit struct {
	start, end int
	fold       string
}

// markupRunes are the non-ASCII runes whose compatibility fold is a markup
// character. The set is fixed by Unicode; a test recomputes it.
var markupRunes = string([]rune{0xFF1C, 0xFE64})

var (
	foldTagRe  = regexp.MustCompile(`<\s*(/?)\s*([a-z][a-z0-9_-]*)([^<>]*)>`)
	foldAttrRe = regexp.MustCompile(`\s+(?:nonce|integrity)\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]*)`)
)

// maxFoldPasses bounds the fixed-point loop. Every pass removes at least one
// byte, so the loop always ends; the cap only bounds adversarial input that
// nests fragments deeply.
const maxFoldPasses = 32

// entityAt reports the length and folded text of an HTML character reference
// at the start of s, or 0 when s does not start with one. Only the forms a
// reader could take for markup or quoting are recognised.
func entityAt(s string) (int, string) {
	if len(s) < 4 || s[0] != '&' {
		return 0, ""
	}
	end := strings.IndexByte(s, ';')
	if end < 0 || end > 10 {
		return 0, ""
	}
	body := strings.ToLower(s[1:end])
	switch body {
	case "lt":
		return end + 1, "<"
	case "gt":
		return end + 1, ">"
	case "quot":
		return end + 1, `"`
	case "apos":
		return end + 1, "'"
	case "amp":
		return end + 1, "&"
	case "sol":
		return end + 1, "/"
	case "equals":
		return end + 1, "="
	}
	if len(body) >= 2 && body[0] == '#' {
		var n int64
		var err error
		if body[1] == 'x' {
			n, err = strconv.ParseInt(body[2:], 16, 32)
		} else {
			n, err = strconv.ParseInt(body[1:], 10, 32)
		}
		if err != nil || n <= 0 || n > unicode.MaxRune {
			return end + 1, ""
		}
		return end + 1, foldRune(rune(n))
	}
	return 0, ""
}

// foldRune returns the scan form of one rune: lower case, compatibility
// normalised (fullwidth and small-form markup become ASCII), with invisible
// and control runes dropped so they cannot split a tag name.
func foldRune(r rune) string {
	if r < utf8.RuneSelf {
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		if r < ' ' && r != '\n' && r != '\t' && r != '\r' || r == 0x7f {
			return ""
		}
		return string(r)
	}
	if r == utf8.RuneError || invisible(r) {
		return ""
	}
	return strings.ToLower(norm.NFKC.String(string(r)))
}

// invisible reports runes that render as nothing or reorder text, and so can
// hide inside a tag name: format characters, controls, the tag block,
// variation selectors and the line and paragraph separators.
func invisible(r rune) bool {
	switch {
	case unicode.Is(unicode.Cf, r), unicode.Is(unicode.Cc, r):
		return true
	case r >= 0xE0000 && r <= 0xE007F:
		return true
	case r >= 0xFE00 && r <= 0xFE0F, r >= 0xE0100 && r <= 0xE01EF:
		return true
	case r == 0x2028, r == 0x2029, r == 0x034F, r == 0x115F, r == 0x1160, r == 0x3164, r == 0xFFA0:
		return true
	}
	return false
}

func unitsOf(s string) []unit {
	us := make([]unit, 0, len(s))
	for i := 0; i < len(s); {
		if s[i] == '&' {
			if n, f := entityAt(s[i:]); n > 0 {
				us = append(us, unit{i, i + n, f})
				i += n
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		us = append(us, unit{i, i + size, foldRune(r)})
		i += size
	}
	return us
}

// scanMarkup reports whether s holds anything the delimiter scrubber could
// act on. It is the fast path in front of every tool result.
func scanMarkup(s string) bool {
	return strings.ContainsAny(s, "<&"+markupRunes)
}

// stripDelimiters removes harness delimiter tags from s. Tags are found on the
// folded form, so case, fullwidth angle brackets, character references and
// zero-width runes inside the tag do not hide it. A tag whose kind the engine
// manages is cut whole; any other tag only loses nonce and integrity
// attributes. The scan repeats until nothing changes, so removing one tag
// cannot assemble another from the fragments either side of it.
func stripDelimiters(s string) string {
	for pass := 0; pass < maxFoldPasses && scanMarkup(s); pass++ {
		next, changed := stripPass(s)
		if !changed {
			return s
		}
		s = next
	}
	if scanMarkup(s) {
		// Adversarial nesting: fail closed by dropping the bracket runes.
		return strings.Map(func(r rune) rune {
			switch r {
			case '<', '>', 0xFF1C, 0xFF1E, 0xFE64, 0xFE65:
				return -1
			}
			return r
		}, s)
	}
	return s
}

func stripPass(s string) (string, bool) {
	us := unitsOf(s)
	var fold strings.Builder
	owner := make([]int32, 0, len(s))
	for i, u := range us {
		fold.WriteString(u.fold)
		for range len(u.fold) {
			owner = append(owner, int32(i))
		}
	}
	f := fold.String()
	drop := make([]bool, len(us))
	changed := false
	cut := func(from, to int) {
		if from >= to {
			return
		}
		first, last := int(owner[from]), int(owner[to-1])
		for i := first; i <= last; i++ {
			drop[i] = true
		}
		changed = true
	}
	for _, m := range foldTagRe.FindAllStringSubmatchIndex(f, -1) {
		name := f[m[4]:m[5]]
		if delimiters.KnownKinds[name] {
			cut(m[0], m[1])
			continue
		}
		if f[m[2]:m[3]] == "" {
			for _, a := range foldAttrRe.FindAllStringIndex(f[m[6]:m[7]], -1) {
				cut(m[6]+a[0], m[6]+a[1])
			}
		}
	}
	if !changed {
		return s, false
	}
	var b strings.Builder
	b.Grow(len(s))
	for i, u := range us {
		if !drop[i] {
			b.WriteString(s[u.start:u.end])
		}
	}
	return b.String(), true
}
