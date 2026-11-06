package knowledge

import (
	"regexp"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/locate"
	"github.com/vulnetix/belai/internal/offload"
)

// Source chunking bounds, in estimated tokens. A declaration smaller than
// MinSourceChunkTokens is joined to the one after it, so a file of one-line
// constants is not a hundred chunks; one larger than MaxSourceChunkTokens is cut
// by SplitText, so a long function is still searchable by its parts.
const (
	MinSourceChunkTokens = 48
	MaxSourceChunkTokens = 600
	// maxSymbolRunes bounds the names a merged chunk carries.
	maxSymbolRunes = 200
)

// leadingRe matches a line that opens a declaration group (`const (`), which
// belongs with the first spec under it.
var leadingRe = regexp.MustCompile(`^\s*(?:const|var|type)\s*\(\s*$`)

// isLeading reports whether a line is a comment, attribute or decorator that
// introduces the declaration below it.
func isLeading(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	for _, p := range []string{"//", "#", "/*", "*", "@"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return leadingRe.MatchString(line)
}

type sourceSeg struct {
	from, to int // 1-based, inclusive
	syms     []string
}

// SplitSource cuts a source file into chunks that begin at a declaration: a Go
// file is parsed (go/parser, through locate.ParseDecls) and any other text is
// scanned for declaration keywords. The comment lines directly above a
// declaration go with it, a run of small declarations is joined, and an
// over-long one is cut by SplitText. Each chunk carries the names it declares
// in Symbol. A file with no declaration at all, or one that does not parse, is
// cut by SplitText alone. Line spans are the file's own, so a row can point at
// the real line.
func SplitSource(path, text string) []RawChunk {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	decls := locate.ParseDecls(path, []byte(text))
	if len(decls) == 0 {
		return SplitText(text)
	}
	lines := strings.Split(text, "\n")
	sort.SliceStable(decls, func(i, j int) bool { return decls[i].Line < decls[j].Line })

	var segs []sourceSeg
	prevLine := 0
	for _, d := range decls {
		if d.Line < 1 || d.Line > len(lines) || d.Line == prevLine {
			// A second name on a line (const a, b = 1, 2) joins the first's chunk.
			if d.Line == prevLine && len(segs) > 0 {
				segs[len(segs)-1].syms = append(segs[len(segs)-1].syms, declName(d))
			}
			continue
		}
		start := d.Line
		for j := d.Line - 2; j >= prevLine && j >= 0 && isLeading(lines[j]); j-- {
			start = j + 1
		}
		segs = append(segs, sourceSeg{from: start, syms: []string{declName(d)}})
		prevLine = d.Line
	}
	if len(segs) == 0 {
		return SplitText(text)
	}
	for i := range segs {
		if i+1 < len(segs) {
			segs[i].to = segs[i+1].from - 1
		} else {
			segs[i].to = len(lines)
		}
	}
	if segs[0].from > 1 {
		segs = append([]sourceSeg{{from: 1, to: segs[0].from - 1}}, segs...)
	}

	tokens := func(s sourceSeg) int { return offload.Tokens(strings.Join(lines[s.from-1:s.to], "\n")) }
	var merged []sourceSeg
	for _, s := range segs {
		s = trimSeg(lines, s)
		if s.to < s.from {
			continue
		}
		if n := len(merged); n > 0 {
			last := merged[n-1]
			if tokens(last) < MinSourceChunkTokens && tokens(last)+tokens(s) <= MaxSourceChunkTokens {
				merged[n-1] = sourceSeg{from: last.from, to: s.to, syms: append(append([]string(nil), last.syms...), s.syms...)}
				continue
			}
		}
		merged = append(merged, s)
	}

	var out []RawChunk
	for _, s := range merged {
		body := strings.Join(lines[s.from-1:s.to], "\n")
		sym := joinSymbols(s.syms)
		if offload.Tokens(body) <= MaxSourceChunkTokens {
			if t := strings.TrimSpace(body); t != "" {
				out = append(out, RawChunk{Start: s.from, End: s.to, Text: t, Symbol: sym})
			}
			continue
		}
		for _, p := range SplitText(body) {
			out = append(out, RawChunk{Start: s.from - 1 + p.Start, End: s.from - 1 + p.End, Text: p.Text, Symbol: sym})
		}
	}
	return out
}

// trimSeg drops blank lines from both ends of a segment.
func trimSeg(lines []string, s sourceSeg) sourceSeg {
	for s.from <= s.to && strings.TrimSpace(lines[s.from-1]) == "" {
		s.from++
	}
	for s.to >= s.from && strings.TrimSpace(lines[s.to-1]) == "" {
		s.to--
	}
	return s
}

func declName(d locate.Decl) string {
	if d.Container != "" {
		return d.Container + "." + d.Name
	}
	return d.Name
}

// joinSymbols joins the names a chunk declares, bounded.
func joinSymbols(syms []string) string {
	var b strings.Builder
	for _, s := range syms {
		if s == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(s)
		if b.Len() >= maxSymbolRunes {
			break
		}
	}
	r := []rune(b.String())
	if len(r) > maxSymbolRunes {
		r = r[:maxSymbolRunes]
	}
	return string(r)
}
