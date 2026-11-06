package patchfmt

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/fuzzy"
	"github.com/vulnetix/belai/internal/sanitize"
)

// Tolerance is how far an anchor may stray from the file's text.
type Tolerance int

const (
	// Strict matches whole lines byte for byte.
	Strict Tolerance = iota
	// Whitespace also folds line endings, ignores trailing spaces and tabs and
	// allows one consistent indentation shift (the Edit tool's ladder).
	Whitespace
	// Unicode adds a last rung that folds typographic punctuation (curly quotes,
	// dashes, no-break and other wide spaces, the ellipsis) to ASCII. It is the
	// user's explicit opt-in: it can match text that differs from the file in
	// visible characters.
	Unicode
)

// String is the setting value.
func (t Tolerance) String() string {
	switch t {
	case Whitespace:
		return "whitespace"
	case Unicode:
		return "unicode"
	}
	return "strict"
}

// ParseTolerance reads a patch_tolerance value.
func ParseTolerance(s string) (Tolerance, bool) {
	switch s {
	case "strict":
		return Strict, true
	case "whitespace":
		return Whitespace, true
	case "unicode":
		return Unicode, true
	}
	return Strict, false
}

// Narrower returns the stricter of two tolerances.
func Narrower(a, b Tolerance) Tolerance {
	if a < b {
		return a
	}
	return b
}

// Note says how one hunk was anchored when it was not an exact match.
type Note struct {
	Hunk int
	Tier string
}

// Plan anchors every hunk of an Update against body and returns the new
// contents. Hunks are located in the original body, so their order in the patch
// does not matter and an earlier hunk never moves a later one; two hunks whose
// lines overlap are an error. Nothing is changed unless every hunk anchors in
// exactly one place. path is only named in errors.
func Plan(path, body string, hunks []Hunk, tol Tolerance) (string, []Note, error) {
	type span struct {
		start, end int
		text       string
		hunk       int
	}
	spans := make([]span, 0, len(hunks))
	var notes []Note
	name := sanitize.Line(path, 120)
	for i, h := range hunks {
		n := i + 1
		scope := 0
		if h.Header != "" {
			at, err := findHeader(body, h.Header, tol)
			if err != nil {
				return "", nil, fmt.Errorf("hunk %d of %s: %w", n, name, err)
			}
			scope = at
		}
		a, err := anchor(body, scope, h, tol)
		if err != nil {
			return "", nil, fmt.Errorf("hunk %d of %s: %w", n, name, err)
		}
		if a.tier != fuzzy.TierExact || a.folded {
			notes = append(notes, Note{Hunk: n, Tier: a.tierName()})
		}
		spans = append(spans, span{a.start, a.end, a.text, n})
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i := 1; i < len(spans); i++ {
		if spans[i].start < spans[i-1].end {
			return "", nil, fmt.Errorf("hunks %d and %d of %s overlap; merge them into one hunk", spans[i-1].hunk, spans[i].hunk, name)
		}
	}
	var b strings.Builder
	at := 0
	for _, s := range spans {
		b.WriteString(body[at:s.start])
		b.WriteString(s.text)
		at = s.end
	}
	b.WriteString(body[at:])
	out := b.String()
	if out == body {
		return "", nil, fmt.Errorf("the patch leaves %s unchanged", name)
	}
	return out, notes, nil
}

// anchored is where one hunk landed and what replaces it.
type anchored struct {
	start, end int
	text       string
	tier       fuzzy.Tier
	folded     bool
}

func (a anchored) tierName() string {
	if a.folded {
		return "Unicode punctuation folded"
	}
	return a.tier.String()
}

// findHeader returns the offset just after the one line of body that the
// header names. Strict compares the line as written; the other tolerances
// ignore surrounding whitespace.
func findHeader(body, header string, tol Tolerance) (int, error) {
	want := header
	if tol >= Whitespace {
		want = strings.TrimSpace(header)
	}
	count, after := 0, 0
	for off := 0; off < len(body); {
		end := strings.IndexByte(body[off:], '\n')
		next := len(body)
		line := body[off:]
		if end >= 0 {
			line, next = body[off:off+end], off+end+1
		}
		line = strings.TrimSuffix(line, "\r")
		if tol >= Whitespace {
			line = strings.TrimSpace(line)
		}
		if line == want {
			count++
			if count == 1 {
				after = next
			}
		}
		off = next
	}
	switch {
	case count == 0:
		return 0, fmt.Errorf("the @@ line is not in the file; copy a whole line of the file after @@")
	case count > 1:
		return 0, fmt.Errorf("the @@ line matches %d lines; use a line that appears once", count)
	}
	return after, nil
}

// anchor finds one hunk in body[scope:].
func anchor(body string, scope int, h Hunk, tol Tolerance) (anchored, error) {
	text := body[scope:]
	if h.AtEnd {
		text = tail(text, strings.Count(h.Old, "\n"))
	}
	base := len(body) - len(text)
	// A final line with no terminator at the end of the file is matched by the
	// same lines without theirs.
	variants := [][2]string{{h.Old, h.New}}
	if strings.HasSuffix(h.Old, "\n") {
		variants = append(variants, [2]string{strings.TrimSuffix(h.Old, "\n"), strings.TrimSuffix(h.New, "\n")})
	}
	if h.Old == "" {
		// An append at the end of the file: no old lines to find.
		return appendAt(body, h)
	}
	for _, v := range variants {
		if v[0] == "" {
			continue
		}
		r := fuzzy.Locate(text, v[0], fuzzy.Options{Relaxed: tol >= Whitespace, Lines: true})
		folded := false
		if r.Matches == 0 && tol >= Unicode {
			var ok bool
			r, ok = locateFolded(text, v[0])
			folded = ok
		}
		if r.Matches == 0 {
			continue
		}
		switch {
		case r.Matches > 1:
			return anchored{}, fmt.Errorf("the lines match %d places in the file; add context lines or an @@ line to make it unique", r.Matches)
		case r.Problem != fuzzy.ProblemNone:
			return anchored{}, fmt.Errorf("the lines match only after whitespace differences (%s) and %s, so nothing was changed; copy the lines exactly as Read shows them", r.Tier, r.Problem)
		}
		fitted, err := fuzzy.Refit(v[1], r)
		if err != nil {
			return anchored{}, fmt.Errorf("the lines match only after whitespace differences (%s) but the new lines cannot be fitted to the file's indentation or line endings; give them the same indentation as the old lines", r.Tier)
		}
		return anchored{start: base + r.Start, end: base + r.End, text: fitted, tier: r.Tier, folded: folded}, nil
	}
	return anchored{}, notFound(body, h, tol)
}

// appendAt adds the new lines of a hunk with no old lines at the end of the
// file, after a line terminator when the file lacks one, in the file's line
// ending when it uses CRLF throughout.
func appendAt(body string, h Hunk) (anchored, error) {
	add := h.New
	crlf := strings.Contains(body, "\r\n") && !strings.Contains(strings.ReplaceAll(body, "\r\n", ""), "\n")
	if crlf {
		add = strings.ReplaceAll(add, "\n", "\r\n")
	}
	if body != "" && !strings.HasSuffix(body, "\n") && !strings.HasSuffix(body, "\r") {
		if crlf {
			add = "\r\n" + add
		} else {
			add = "\n" + add
		}
	}
	return anchored{start: len(body), end: len(body), text: add, tier: fuzzy.TierExact}, nil
}

// notFound is the error for a hunk that anchors nowhere. It names the closest
// line by number and measured kind of difference and never quotes it.
func notFound(body string, h Hunk, tol Tolerance) error {
	near := fuzzy.Nearest(body, h.Old, 1)
	hint := ""
	if tol == Strict {
		hint = " (patch_tolerance is strict)"
	}
	if len(near) == 0 {
		return fmt.Errorf("the lines to replace were not found and no line resembles them%s; Read the file again", hint)
	}
	return fmt.Errorf("the lines to replace were not found%s; the closest line is %d, which differs in %s; Read the file again", hint, near[0].Line, near[0].Differs)
}

// tail returns the last n lines of s, or all of s when it has fewer.
func tail(s string, n int) string {
	if n < 1 {
		return ""
	}
	var starts []int
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			fallthrough
		case '\n':
			starts = append(starts, start)
			start = i + 1
		}
	}
	if start < len(s) {
		starts = append(starts, start)
	}
	if n >= len(starts) {
		return s
	}
	return s[starts[len(starts)-n]:]
}
