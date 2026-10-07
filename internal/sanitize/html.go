package sanitize

import (
	"html"
	"strings"
)

// PlainText reduces a fragment of HTML to the text a person would read from
// it, folded onto one line and capped at max runes (see [Line]). It is for
// error bodies a server sent back instead of the answer it was asked for, such
// as a 403 page from a gateway, that would otherwise show markup on screen.
//
// Tags and comments are dropped, the content of script and style elements is
// dropped with them, entities are decoded, and a tag that was cut off by a
// byte limit (no closing angle bracket) is dropped to the end. Text with no
// markup passes through [Line] unchanged. The function is deterministic and
// asks no model; the result is display text and is as untrusted as its input.
func PlainText(s string, max int) string {
	if !strings.Contains(s, "<") {
		return Line(html.UnescapeString(s), max)
	}
	var b strings.Builder
	for len(s) > 0 {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		s = s[i:]
		switch {
		case strings.HasPrefix(s, "<!--"):
			end := strings.Index(s, "-->")
			if end < 0 {
				s = ""
			} else {
				s = s[end+3:]
			}
			b.WriteByte(' ')
		case isTagStart(s):
			name := tagName(s)
			end := strings.IndexByte(s, '>')
			if end < 0 {
				s = ""
				break
			}
			s = s[end+1:]
			if name == "script" || name == "style" {
				s = skipElement(s, name)
			}
			b.WriteByte(' ')
		default:
			// A bare "<" that opens no tag is text.
			b.WriteByte('<')
			s = s[1:]
		}
	}
	return Line(html.UnescapeString(b.String()), max)
}

// isTagStart reports whether s, which begins with "<", opens a tag, a closing
// tag, a declaration or a processing instruction rather than a lone "<".
func isTagStart(s string) bool {
	if len(s) < 2 {
		return false
	}
	c := s[1]
	return c == '/' || c == '!' || c == '?' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// tagName returns the lower-case element name of the opening tag at the start
// of s, or "" for a closing tag or declaration.
func tagName(s string) string {
	s = s[1:]
	n := 0
	for n < len(s) {
		c := s[n]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			n++
			continue
		}
		break
	}
	return strings.ToLower(s[:n])
}

// skipElement drops s up to and including the closing tag of name, or to the
// end when the element is never closed.
func skipElement(s, name string) string {
	closer := "</" + name
	lower := asciiLower(s)
	i := strings.Index(lower, closer)
	if i < 0 {
		return ""
	}
	rest := s[i:]
	if end := strings.IndexByte(rest, '>'); end >= 0 {
		return rest[end+1:]
	}
	return ""
}

// asciiLower lower-cases ASCII letters only, so byte offsets in the result
// match the input (strings.ToLower can change a multi-byte rune's length).
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
