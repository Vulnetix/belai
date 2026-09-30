package sanitize

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Text cleans free text bound for a model or the transcript. It repairs
// invalid UTF-8 (the bytes are dropped), removes terminal escape sequences,
// control characters other than newline and tab, bidirectional overrides,
// zero-width and tag-block runes and the Unicode line separators (which become
// newlines), then removes delimiter markup. Joiners that spell real text
// (ZWJ, ZWNJ) are kept. It is idempotent.
func Text(s string) string {
	if s == "" {
		return s
	}
	if plainText(s) {
		return Sanitize(s)
	}
	s = strings.ToValidUTF8(s, "")
	if strings.ContainsRune(s, 0x1b) || strings.ContainsRune(s, 0x9b) {
		s = ansi.Strip(s)
	}
	s = strings.Map(textRune, s)
	return Sanitize(s)
}

// plainText reports whether s is printable ASCII plus newline and tab: text
// that every rule of [Text] except the delimiter scrub leaves as it is.
func plainText(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x80 || c == 0x7f || c < ' ' && c != '\n' && c != '\t' {
			return false
		}
	}
	return true
}

// textRune is the per-rune rule of Text: -1 drops the rune.
func textRune(r rune) rune {
	switch {
	case r == '\n' || r == '\t':
		return r
	case r == '\r':
		return -1
	case r == 0x2028 || r == 0x2029 || r == 0x85:
		return '\n'
	case r == 0x200C || r == 0x200D:
		return r
	case r < ' ' || r == 0x7f || (r >= 0x80 && r <= 0x9f):
		return -1
	case unicode.Is(unicode.Cf, r), r >= 0xE0000 && r <= 0xE007F,
		r >= 0xFE00 && r <= 0xFE0F, r == utf8.RuneError:
		return -1
	}
	return r
}

// TextN is [Text] capped at max runes, cut on a rune boundary. A max of zero
// or less means no cap.
func TextN(s string, max int) string {
	return capRunes(Text(s), max, "")
}

// Line cleans text for a single line of UI, log or notification output: [Text]
// followed by folding all whitespace runs to one space and capping at max
// runes with an ellipsis.
func Line(s string, max int) string {
	return capRunes(strings.Join(strings.Fields(Text(s)), " "), max, "…")
}

// Ident keeps only ASCII letters, digits, dot, underscore and hyphen, capped at
// max runes. It is for names that must be safe anywhere: tool names, backend
// ids, subjects in a record.
func Ident(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		}
		if max > 0 && b.Len() >= max {
			break
		}
	}
	return b.String()
}

// ErrPath is returned by [PathText] for a path that fails the lexical checks.
var ErrPath = errors.New("path has invalid characters")

// MaxPathBytes bounds a path argument.
const MaxPathBytes = 4096

// PathText applies the lexical checks a path must pass before the tools
// package resolves and confines it: valid UTF-8, no NUL, control, newline,
// bidirectional or invisible runes, and a bounded length. It never rewrites
// the path; confinement stays the last word.
func PathText(s string) error {
	if s == "" || len(s) > MaxPathBytes || !utf8.ValidString(s) {
		return ErrPath
	}
	for _, r := range s {
		if r < ' ' || r == 0x7f || (r >= 0x80 && r <= 0x9f) || invisible(r) {
			return ErrPath
		}
	}
	return nil
}

// ErrHeader is returned by [Header] for a value that could split or smuggle a
// header.
var ErrHeader = errors.New("header value has invalid characters")

// Header validates an HTTP header value: visible ASCII and space or tab only,
// no CR, LF, NUL or other control. Header injection is refused, not repaired.
func Header(s string) error {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\t' || (c >= ' ' && c < 0x7f) {
			continue
		}
		return ErrHeader
	}
	return nil
}

func capRunes(s string, max int, suffix string) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	keep := max - utf8.RuneCountInString(suffix)
	if keep < 0 {
		keep = 0
	}
	rs := []rune(s)
	return string(rs[:keep]) + suffix
}

// Clip is [Line] with the cap applied the way display code wants it: when the
// cleaned line is longer than max runes it is cut to max runes and an
// ellipsis follows, so the result is at most max plus one rune. A max of zero
// or less means no cap.
func Clip(s string, max int) string {
	s = Line(s, 0)
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:max])) + "…"
}
