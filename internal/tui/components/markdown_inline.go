package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Inline markdown spans render to []Seg so the block renderer can wrap and
// re-slice them on the plain text. Emphasis is expressed with the palette and
// reverse video rather than a Bold/Italic attribute the Seg model does not
// carry: bold is cream, italic is teal-soft, and both together add reverse
// video because a colour alone can no longer tell them apart. Strikethrough
// carries its own SGR span.

const (
	styleBold = 1 << iota
	styleEm
	styleStrike
)

// parseInline renders inline markdown in one logical line of text into
// segments. Unmatched delimiters render literally rather than being dropped,
// so a half-typed reply stays readable while it streams.
func parseInline(src string) []Seg {
	rs := []rune(src)
	var out []Seg
	var lit []rune
	flush := func() {
		if len(lit) > 0 {
			out = append(out, NewSeg(string(lit), nil))
			lit = lit[:0]
		}
	}
	for i := 0; i < len(rs); {
		switch c := rs[i]; {
		case c == '\\' && i+1 < len(rs) && isEscapable(rs[i+1]):
			lit = append(lit, rs[i+1])
			i += 2
		case c == '`':
			flush()
			if s, ni, ok := parseCode(rs, i); ok {
				out = append(out, s)
				i = ni
			} else {
				lit = append(lit, c)
				i++
			}
		case c == '<':
			flush()
			if segs, ni, ok := parseAutolink(rs, i); ok {
				out = append(out, segs...)
				i = ni
			} else {
				lit = append(lit, c)
				i++
			}
		case c == '[':
			flush()
			if segs, ni, ok := parseLink(rs, i); ok {
				out = append(out, segs...)
				i = ni
			} else {
				lit = append(lit, c)
				i++
			}
		case c == '*' || c == '_' || c == '~':
			flush()
			if segs, ni, ok := parseEmph(rs, i); ok {
				out = append(out, segs...)
				i = ni
			} else {
				lit = append(lit, c)
				i++
			}
		default:
			lit = append(lit, c)
			i++
		}
	}
	flush()
	return out
}

// isEscapable reports whether a rune can follow a backslash escape in inline
// markdown (the ASCII punctuation CommonMark allows).
func isEscapable(r rune) bool {
	return strings.ContainsRune(`!"#$%&'()*+,-./:;<=>?@[\]^_`+"`"+`{|}~`, r)
}

func isAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// parseCode parses a backtick code span, returning one amber segment.
func parseCode(rs []rune, i int) (Seg, int, bool) {
	n := 1
	for i+n < len(rs) && rs[i+n] == '`' {
		n++
	}
	j := i + n
	for j < len(rs) {
		if rs[j] != '`' {
			j++
			continue
		}
		m := 1
		for j+m < len(rs) && rs[j+m] == '`' {
			m++
		}
		if m == n {
			content := strings.ReplaceAll(string(rs[i+n:j]), "\n", " ")
			if content != "" && content[0] == ' ' && content[len(content)-1] == ' ' &&
				strings.TrimSpace(content) != "" {
				content = content[1 : len(content)-1]
			}
			return NewSeg(content, ColorAmber), j + n, true
		}
		j += m
	}
	return Seg{}, i, false
}

// parseAutolink parses <https://…> and <user@example.com> into a teal link.
func parseAutolink(rs []rune, i int) ([]Seg, int, bool) {
	j := i + 1
	for j < len(rs) && rs[j] != '>' && rs[j] != '\n' && rs[j] != '<' {
		j++
	}
	if j >= len(rs) || rs[j] != '>' || j == i+1 {
		return nil, i, false
	}
	inner := string(rs[i+1 : j])
	if !looksLikeURL(inner) && !looksLikeEmail(inner) {
		return nil, i, false
	}
	return []Seg{NewSeg(inner, ColorTealSoft)}, j + 1, true
}

func looksLikeURL(s string) bool {
	if i := strings.Index(s, "://"); i > 0 {
		return i+3 < len(s)
	}
	return strings.HasPrefix(s, "www.")
}

func looksLikeEmail(s string) bool {
	at := strings.Index(s, "@")
	return at > 0 && at < len(s)-1 && !strings.ContainsAny(s, " \t")
}

// parseLink parses [text](url). The text is rendered teal, the URL muted and
// marked Link so the wrapper drops it when the line has no room. A link
// without a destination renders its text literally, and an unclosed bracket
// falls back to a literal '['.
func parseLink(rs []rune, i int) ([]Seg, int, bool) {
	depth := 0
	j := i + 1
	for j < len(rs) {
		if rs[j] == '\\' {
			j += 2
			continue
		}
		if rs[j] == '[' {
			depth++
		} else if rs[j] == ']' {
			if depth == 0 {
				break
			}
			depth--
		}
		j++
	}
	if j >= len(rs) {
		return nil, i, false
	}
	text := string(rs[i+1 : j])

	k := j + 1
	if k >= len(rs) || rs[k] != '(' {
		return parseInline(text), j + 1, true
	}
	depth = 0
	m := k
	for m < len(rs) {
		if rs[m] == '\\' {
			m += 2
			continue
		}
		if rs[m] == '(' {
			depth++
		} else if rs[m] == ')' {
			if depth == 0 {
				break
			}
			depth--
		}
		m++
	}
	if m >= len(rs) {
		return parseInline(text), j + 1, true
	}

	url := strings.TrimSpace(string(rs[k+1 : m]))
	if strings.HasPrefix(url, "<") {
		if end := strings.Index(url, ">"); end > 0 {
			url = url[1:end]
		}
	} else if idx := strings.IndexAny(url, " \t"); idx >= 0 {
		url = url[:idx]
	}
	if url == "" {
		return parseInline(text), m + 1, true
	}

	segs := parseInline(text)
	for x := range segs {
		segs[x].FG = ColorTealSoft
	}
	segs = append(segs, Seg{Text: " (" + url + ")", FG: ColorMuted, Link: true})
	return segs, m + 1, true
}

// parseEmph parses a **bold**, *italic*, ***both*** or ~~strike~~ run. It
// recurses for nested emphasis and uses simple flanking rules (an opener must
// be followed by a non-space, a closer preceded by one; '_' additionally
// cannot sit inside a word) so snake_case prose is not mangled.
func parseEmph(rs []rune, i int) ([]Seg, int, bool) {
	c := rs[i]
	n := 1
	for i+n < len(rs) && rs[i+n] == c {
		n++
	}
	if c == '~' {
		if n < 2 {
			return nil, i, false
		}
		n = 2
	}
	style := styleBold | styleEm
	switch n {
	case 1:
		style = styleEm
	case 2:
		style = styleBold
	}
	if c == '~' {
		style = styleStrike
	}

	openEnd := i + n
	if openEnd < len(rs) && rs[openEnd] == ' ' {
		return nil, i, false
	}
	if c == '_' && i > 0 && isAlnum(rs[i-1]) {
		return nil, i, false
	}

	var inner []Seg
	var lit []rune
	flushLit := func() {
		if len(lit) > 0 {
			inner = append(inner, NewSeg(string(lit), nil))
			lit = lit[:0]
		}
	}

	j := openEnd
	for j < len(rs) {
		if rs[j] == c {
			m := 1
			for j+m < len(rs) && rs[j+m] == c {
				m++
			}
			leftFlank := j > openEnd && rs[j-1] != ' '
			okIntraword := c != '_' || j+m >= len(rs) || !isAlnum(rs[j+m])
			if m >= n && leftFlank && okIntraword {
				flushLit()
				return styleSegs(inner, style), j + n, true
			}
			// Not a closer at this level: try it as nested emphasis, else literal.
			if s, ni, ok := parseEmph(rs, j); ok {
				flushLit()
				inner = append(inner, s...)
				j = ni
				continue
			}
			lit = append(lit, rs[j:j+m]...)
			j += m
			continue
		}
		if rs[j] == '\\' && j+1 < len(rs) && isEscapable(rs[j+1]) {
			lit = append(lit, rs[j+1])
			j += 2
			continue
		}
		if rs[j] == '`' {
			flushLit()
			if s, ni, ok := parseCode(rs, j); ok {
				inner = append(inner, s)
				j = ni
				continue
			}
		}
		if rs[j] == '[' {
			flushLit()
			if s, ni, ok := parseLink(rs, j); ok {
				inner = append(inner, s...)
				j = ni
				continue
			}
		}
		lit = append(lit, rs[j])
		j++
	}
	return nil, i, false
}

// styleSegs applies an emphasis style to already-parsed inner segments.
func styleSegs(segs []Seg, style int) []Seg {
	for i := range segs {
		if style&styleStrike != 0 {
			segs[i].Strike = append(segs[i].Strike, Span{From: 0, To: lipgloss.Width(segs[i].Text)})
		}
		switch style & (styleBold | styleEm) {
		case styleBold:
			segs[i].FG = ColorCream
		case styleEm:
			segs[i].FG = ColorTealSoft
		case styleBold | styleEm:
			segs[i].FG = ColorCream
			segs[i].Emph = append(segs[i].Emph, Span{From: 0, To: lipgloss.Width(segs[i].Text)})
		}
	}
	return segs
}
