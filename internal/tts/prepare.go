package tts

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// GroupChars is the target size of one request. Sentences are packed up to
	// it, so the first group is short enough to start playing quickly.
	GroupChars = 800
	// MaxChars bounds what one message may read aloud.
	MaxChars = 12000
)

var (
	fenceRE  = regexp.MustCompile("(?s)```.*?(?:```|$)")
	imageRE  = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	linkRE   = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	urlRE    = regexp.MustCompile(`https?://\S+`)
	htmlRE   = regexp.MustCompile(`<[^>\n]{1,200}>`)
	inlineRE = regexp.MustCompile("`([^`\n]*)`")
	emphRE   = regexp.MustCompile(`(\*\*|__|\*|_)([^*_\n]+)(\*\*|__|\*|_)`)
	headRE   = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s*`)
	bulletRE = regexp.MustCompile(`(?m)^\s*(?:[-*+]|\d+[.)])\s+`)
	quoteRE  = regexp.MustCompile(`(?m)^\s*>\s?`)
	ruleRE   = regexp.MustCompile(`(?m)^\s*(?:[-*_]\s*){3,}$`)
	tableSep = regexp.MustCompile(`^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$`)
	spacesRE = regexp.MustCompile(`[ \t]+`)
	blankRE  = regexp.MustCompile(`\n{2,}`)
	endRE    = regexp.MustCompile(`[.!?]["')\]]*$`)
)

// Prepare turns a markdown message into the pieces to synthesise, in order.
// Fenced code is not read: each block becomes "Code omitted." Links keep their
// text, bare URLs become "link", tables read as comma separated rows and
// markdown markers are dropped. Nothing is added that the message did not say
// except those two phrases. Text beyond MaxChars is cut at a sentence.
func Prepare(md string) []string {
	s := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= ' ' && r != 0x7f {
			return r
		}
		return -1
	}, strings.ReplaceAll(md, "\r\n", "\n"))
	s = fenceRE.ReplaceAllString(s, "\nCode omitted.\n")
	s = imageRE.ReplaceAllString(s, "")
	s = linkRE.ReplaceAllString(s, "$1")
	s = urlRE.ReplaceAllString(s, "link")
	s = htmlRE.ReplaceAllString(s, "")
	s = flattenTables(s)
	s = headRE.ReplaceAllString(s, unit)
	s = quoteRE.ReplaceAllString(s, "")
	s = ruleRE.ReplaceAllString(s, "")
	s = bulletRE.ReplaceAllString(s, unit)
	s = inlineRE.ReplaceAllString(s, "$1")
	s = emphRE.ReplaceAllString(s, "$2")
	s = spacesRE.ReplaceAllString(s, " ")
	if strings.TrimSpace(strings.ReplaceAll(s, unit, "")) == "" {
		return nil
	}
	var sentences, prose []string
	flush := func() {
		if len(prose) > 0 {
			sentences = append(sentences, splitSentences(ensureEnd(strings.Join(prose, " ")))...)
			prose = nil
		}
	}
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
			flush()
		case strings.HasPrefix(t, unit):
			// A heading, list item or table row is a sentence of its own.
			flush()
			if t = strings.TrimSpace(strings.TrimPrefix(t, unit)); t != "" {
				sentences = append(sentences, splitSentences(ensureEnd(t))...)
			}
		default:
			prose = append(prose, t)
		}
	}
	flush()
	return group(sentences)
}

// unit marks the start of a line that is a sentence by itself.
const unit = "\x01"

func ensureEnd(s string) string {
	if endRE.MatchString(s) {
		return s
	}
	return s + "."
}

// flattenTables reads a markdown table row by row with commas and drops the
// separator row.
func flattenTables(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "|") || strings.Count(t, "|") >= 2 {
			if tableSep.MatchString(t) {
				continue
			}
			cells := strings.Split(strings.Trim(t, "|"), "|")
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
			}
			l = unit + strings.Join(cells, ", ")
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// splitSentences splits at ". ", "! " and "? ", leaving abbreviations such as
// "e.g." whole when the next word is lower case.
func splitSentences(p string) []string {
	var out []string
	start := 0
	rs := []rune(p)
	for i := 0; i < len(rs)-1; i++ {
		if (rs[i] == '.' || rs[i] == '!' || rs[i] == '?') && rs[i+1] == ' ' {
			next := i + 2
			if rs[i] == '.' && next < len(rs) && rs[next] >= 'a' && rs[next] <= 'z' {
				continue
			}
			out = append(out, strings.TrimSpace(string(rs[start:i+1])))
			start = i + 2
		}
	}
	if tail := strings.TrimSpace(string(rs[start:])); tail != "" {
		out = append(out, tail)
	}
	return out
}

// group packs sentences into pieces of about GroupChars, splitting a sentence
// longer than that at a word, and stops at MaxChars.
func group(sentences []string) []string {
	var out []string
	var cur strings.Builder
	total := 0
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, t)
		}
		cur.Reset()
	}
	for _, s := range sentences {
		for utf8.RuneCountInString(s) > GroupChars {
			cut := cutAt(s, GroupChars)
			flush()
			out = append(out, strings.TrimSpace(s[:cut]))
			total += cut
			s = strings.TrimSpace(s[cut:])
		}
		if total+utf8.RuneCountInString(s) > MaxChars {
			break
		}
		if cur.Len() > 0 && utf8.RuneCountInString(cur.String())+1+utf8.RuneCountInString(s) > GroupChars {
			flush()
		}
		if cur.Len() > 0 {
			cur.WriteByte(' ')
		}
		cur.WriteString(s)
		total += utf8.RuneCountInString(s)
	}
	flush()
	return out
}

// cutAt returns the byte offset of the last space within the first n runes of
// s, or of the n-th rune when there is none.
func cutAt(s string, n int) int {
	byteAt, last := 0, -1
	for i, r := range s {
		if n == 0 {
			break
		}
		if r == ' ' {
			last = i
		}
		byteAt = i + utf8.RuneLen(r)
		n--
	}
	if last > 0 {
		return last
	}
	return byteAt
}
