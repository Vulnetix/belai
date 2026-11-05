package fuzzy

import (
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/sanitize"
)

// Tier names how an old string was found in a body. The ladder is fixed and
// ordered: each tier is tried only when every tier before it found nothing.
type Tier int

const (
	// TierNone means no tier found the text.
	TierNone Tier = iota
	// TierExact is a byte-for-byte match. It always wins.
	TierExact
	// TierLineEnding matches line for line once CRLF, LF and CR are treated as
	// the same terminator.
	TierLineEnding
	// TierTrailing also ignores spaces and tabs at the end of each line.
	TierTrailing
	// TierIndent also allows every line of the match to differ from the old
	// text by one and the same leading whitespace prefix.
	TierIndent
)

// String returns the tier's name as the Edit result and the docs use it.
func (t Tier) String() string {
	switch t {
	case TierExact:
		return "exact"
	case TierLineEnding:
		return "line endings folded"
	case TierTrailing:
		return "trailing whitespace ignored"
	case TierIndent:
		return "indentation shifted"
	}
	return "none"
}

// Problem is why a unique relaxed match was refused rather than applied.
type Problem int

const (
	// ProblemNone means the match can be applied.
	ProblemNone Problem = iota
	// ProblemMixedLineEndings means the matched lines end in more than one kind
	// of terminator (or the file does, for a one-line match that adds lines), so
	// there is no single ending to fit a replacement to.
	ProblemMixedLineEndings
	// ProblemMixedIndent means the indentation delta is a mix of tabs and
	// spaces, so there is no single indent character to fit a replacement to.
	ProblemMixedIndent
)

// String returns a short reason for the problem.
func (p Problem) String() string {
	switch p {
	case ProblemMixedLineEndings:
		return "the matched lines mix line endings"
	case ProblemMixedIndent:
		return "the indentation differs by a mix of tabs and spaces"
	}
	return ""
}

// Options controls Locate.
type Options struct {
	// Relaxed enables the tiers after TierExact. Off, Locate is a plain
	// substring count.
	Relaxed bool
}

// Result is what Locate found.
type Result struct {
	// Tier is the tier that produced Matches, or TierNone.
	Tier Tier
	// Matches is the number of places the text was found at Tier. For
	// TierExact it counts non-overlapping occurrences like strings.Count; for a
	// relaxed tier it counts every start line that matches.
	Matches int
	// Start and End are the byte span of the match in the body. Set when
	// Matches is 1, and for TierExact when Matches is at least 1 (the first).
	Start, End int
	// Problem is set on a unique relaxed match that cannot be fitted.
	Problem Problem

	eol       string // terminator replacements are fitted to
	delta     string // indentation delta
	addIndent bool   // true: body = delta + old; false: old = delta + body
}

// line is one line of a text: content without its terminator, the byte
// offsets, and the terminator that followed it (empty on the last line).
type line struct {
	start, end int // content span
	next       int // offset after the terminator
	term       string
}

// splitLines scans s for lines ended by CRLF, LF or a lone CR.
func splitLines(s string) []line {
	var out []line
	start := 0
	for i := 0; i < len(s); {
		switch s[i] {
		case '\n':
			out = append(out, line{start: start, end: i, next: i + 1, term: "\n"})
			i++
			start = i
		case '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				out = append(out, line{start: start, end: i, next: i + 2, term: "\r\n"})
				i += 2
			} else {
				out = append(out, line{start: start, end: i, next: i + 1, term: "\r"})
				i++
			}
			start = i
		default:
			i++
		}
	}
	if start < len(s) {
		out = append(out, line{start: start, end: len(s), next: len(s)})
	}
	return out
}

func content(s string, l line) string { return s[l.start:l.end] }

func trimTrailing(s string) string { return strings.TrimRight(s, " \t") }

func isBlank(s string) bool { return strings.Trim(s, " \t") == "" }

func leading(s string) string { return s[:len(s)-len(strings.TrimLeft(s, " \t"))] }

// Locate finds old in body. An exact substring wins whatever the options say.
// Otherwise, with Relaxed, the tiers run in order; the first to match anywhere
// decides, and Matches is how many places it matched. A caller applies the
// match only when Matches is 1 and Problem is ProblemNone.
//
// The relaxed tiers compare whole lines, never part of one, and need at least
// one line of old that is not blank.
func Locate(body, old string, opts Options) Result {
	if old == "" {
		return Result{}
	}
	if n := strings.Count(body, old); n > 0 {
		i := strings.Index(body, old)
		return Result{Tier: TierExact, Matches: n, Start: i, End: i + len(old)}
	}
	if !opts.Relaxed {
		return Result{}
	}
	ol := splitLines(old)
	hasText := false
	for _, l := range ol {
		if !isBlank(content(old, l)) {
			hasText = true
			break
		}
	}
	if !hasText {
		return Result{}
	}
	bl := splitLines(body)
	for _, tier := range []Tier{TierLineEnding, TierTrailing, TierIndent} {
		if r := locateTier(body, old, bl, ol, tier); r.Matches > 0 {
			return r
		}
	}
	return Result{}
}

func locateTier(body, old string, bl, ol []line, tier Tier) Result {
	n := len(ol)
	wantTerm := ol[n-1].term != ""
	res := Result{Tier: tier}
	for i := 0; i+n <= len(bl); i++ {
		last := bl[i+n-1]
		if wantTerm && last.term == "" {
			continue
		}
		delta, add, ok := matchWindow(body, old, bl[i:i+n], ol, tier)
		if !ok {
			continue
		}
		res.Matches++
		if res.Matches > 1 {
			continue
		}
		res.Start = bl[i].start
		res.End = last.end
		if wantTerm {
			res.End = last.next
		}
		res.delta, res.addIndent = delta, add
		if tier == TierIndent && delta != "" && strings.Contains(delta, " ") && strings.Contains(delta, "\t") {
			res.Problem = ProblemMixedIndent
		}
		eol, mixed := spanEOL(bl, i, n, wantTerm)
		res.eol = eol
		if mixed {
			res.Problem = ProblemMixedLineEndings
		}
	}
	if res.Matches != 1 {
		res.Problem = ProblemNone
	}
	return res
}

// matchWindow compares the window's lines to old's under the tier. For the
// indent tier it also returns the single delta that relates them.
func matchWindow(body, old string, win, ol []line, tier Tier) (delta string, add, ok bool) {
	haveDelta := false
	for k := range ol {
		b := content(body, win[k])
		o := content(old, ol[k])
		if tier >= TierTrailing {
			b, o = trimTrailing(b), trimTrailing(o)
		}
		if tier < TierIndent {
			if b != o {
				return "", false, false
			}
			continue
		}
		if isBlank(b) || isBlank(o) {
			if isBlank(b) != isBlank(o) {
				return "", false, false
			}
			continue
		}
		bi, oi := leading(b), leading(o)
		if b[len(bi):] != o[len(oi):] {
			return "", false, false
		}
		var d string
		var a bool
		switch {
		case bi == oi:
			d, a = "", true
		case strings.HasSuffix(bi, oi):
			d, a = bi[:len(bi)-len(oi)], true
		case strings.HasSuffix(oi, bi):
			d, a = oi[:len(oi)-len(bi)], false
		default:
			return "", false, false
		}
		if !haveDelta {
			delta, add, haveDelta = d, a, true
			continue
		}
		if d != delta || (d != "" && a != add) {
			return "", false, false
		}
	}
	return delta, add, true
}

// spanEOL reports the terminator inside the matched lines. When the span holds
// none (a one-line match) it falls back to the file's, and a file that mixes
// kinds is reported as mixed.
func spanEOL(bl []line, from, n int, wantTerm bool) (eol string, mixed bool) {
	seen := map[string]bool{}
	for k := from; k < from+n; k++ {
		if bl[k].term == "" {
			continue
		}
		if k == from+n-1 && !wantTerm {
			continue
		}
		seen[bl[k].term] = true
	}
	if len(seen) == 0 {
		for _, l := range bl {
			if l.term != "" {
				seen[l.term] = true
			}
		}
	}
	switch len(seen) {
	case 0:
		return "\n", false
	case 1:
		for t := range seen {
			return t, false
		}
	}
	return "\n", true
}

// refitError is the error Refit and Apply return when a match cannot be fitted.
type refitError string

func (e refitError) Error() string { return string(e) }

// Refit fits a replacement to the file around a relaxed match: its line
// terminators become the file's, and under TierIndent each non-blank line gains
// or loses the same indentation delta the match had. An exact match is
// returned as written. A match with a Problem, or an indent that cannot be
// removed from a line, is an error and nothing is applied.
func Refit(newS string, r Result) (string, error) {
	if r.Tier == TierExact {
		return newS, nil
	}
	if r.Tier == TierNone || r.Matches != 1 {
		return "", refitError("no unique match to fit")
	}
	if r.Problem != ProblemNone {
		return "", refitError(r.Problem.String())
	}
	lines := splitLines(newS)
	var b strings.Builder
	for _, l := range lines {
		text := content(newS, l)
		if r.Tier == TierIndent && r.delta != "" && !isBlank(text) {
			if r.addIndent {
				text = r.delta + text
			} else if strings.HasPrefix(text, r.delta) {
				text = text[len(r.delta):]
			} else {
				return "", refitError("the replacement has a line that does not carry the indentation to be removed")
			}
		}
		b.WriteString(text)
		if l.term != "" {
			b.WriteString(r.eol)
		}
	}
	return b.String(), nil
}

// Apply replaces the located span of body with newS fitted to it.
func Apply(body, newS string, r Result) (string, error) {
	fitted, err := Refit(newS, r)
	if err != nil {
		return "", err
	}
	if r.Matches < 1 || r.End < r.Start || r.End > len(body) {
		return "", refitError("no match to apply")
	}
	return body[:r.Start] + fitted + body[r.End:], nil
}

// Differs says, as the harness measured it, how a near line differs from the
// wanted one. It is an enum so a diagnostic names a kind of difference without
// repeating file text.
type Differs string

const (
	// DiffersLineEnding means the lines are equal once terminators are folded.
	DiffersLineEnding Differs = "line-ending"
	// DiffersTrailing means they are equal once trailing spaces and tabs go.
	DiffersTrailing Differs = "trailing-whitespace"
	// DiffersIndent means they are equal once surrounding whitespace goes.
	DiffersIndent Differs = "indent"
	// DiffersContent means the text itself differs.
	DiffersContent Differs = "content"
)

// Near is one candidate place in a body that resembles the wanted text.
type Near struct {
	// Line is the 1-based line of the body the candidate's best line is on.
	Line int
	// Differs is how the candidate window differs from the wanted text.
	Differs Differs
	// Snippet is that line, cleaned and capped. A caller that must not echo
	// file text drops it.
	Snippet string
}

const (
	// SnippetRunes caps a Near snippet.
	SnippetRunes = 200
	// nearScanLines bounds how many body lines Nearest scores.
	nearScanLines = 20000
	// nearCompareRunes bounds the text compared per line.
	nearCompareRunes = 400
)

// Nearest ranks up to n places in body that resemble old, best first. The
// first non-blank line of old is scored against every line of body with Score
// (in both directions, so a line that gained or lost text still ranks) and an
// exact match after trimming scores highest. Candidates inside one another's
// window are collapsed to the better one. It returns nil for an empty body,
// an old with no text or n below 1.
func Nearest(body, old string, n int) []Near {
	if n < 1 || body == "" {
		return nil
	}
	ol := splitLines(old)
	anchor := -1
	for i, l := range ol {
		if !isBlank(content(old, l)) {
			anchor = i
			break
		}
	}
	if anchor < 0 {
		return nil
	}
	want := strings.TrimSpace(clip(content(old, ol[anchor])))
	bl := splitLines(body)
	if len(bl) > nearScanLines {
		bl = bl[:nearScanLines]
	}
	type cand struct{ idx, score int }
	var cands []cand
	for i, l := range bl {
		got := strings.TrimSpace(clip(content(body, l)))
		if got == "" {
			continue
		}
		var s int
		if got == want {
			s = 1 << 20
		} else {
			a, okA := Score(want, got)
			b, okB := Score(got, want)
			switch {
			case okA && okB:
				s = subseqFloor + max(a, b)
			case okA:
				s = subseqFloor + a
			case okB:
				s = subseqFloor + b
			default:
				// A typo breaks the subsequence either way, so a line that
				// shares most of its bigrams still ranks, below any line the
				// query is a subsequence of.
				d := dice(want, got)
				if d < diceMin {
					continue
				}
				s = int(d * 1000)
			}
		}
		cands = append(cands, cand{i, s})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	span := max(len(ol), 1)
	var out []Near
	var taken []int
pick:
	for _, c := range cands {
		for _, t := range taken {
			if d := c.idx - t; d < span && -d < span {
				continue pick
			}
		}
		taken = append(taken, c.idx)
		out = append(out, Near{
			Line:    c.idx + 1,
			Differs: windowDiffers(body, old, bl, ol, c.idx-anchor),
			Snippet: sanitize.Line(content(body, bl[c.idx]), SnippetRunes),
		})
		if len(out) == n {
			break
		}
	}
	return out
}

const (
	// subseqFloor lifts a subsequence score above every bigram score.
	subseqFloor = 1 << 12
	// diceMin is the bigram overlap below which a line is not a candidate.
	diceMin = 0.5
)

// dice is the Sørensen-Dice coefficient over rune bigrams, 0 to 1.
func dice(a, b string) float64 {
	ga, gb := bigrams(a), bigrams(b)
	if len(ga) == 0 || len(gb) == 0 {
		return 0
	}
	shared, total := 0, 0
	for g, n := range ga {
		total += n
		shared += min(n, gb[g])
	}
	for _, n := range gb {
		total += n
	}
	return 2 * float64(shared) / float64(total)
}

func bigrams(s string) map[[2]rune]int {
	r := []rune(s)
	out := map[[2]rune]int{}
	for i := 0; i+1 < len(r); i++ {
		out[[2]rune{r[i], r[i+1]}]++
	}
	return out
}

func clip(s string) string {
	r := []rune(s)
	if len(r) > nearCompareRunes {
		return string(r[:nearCompareRunes])
	}
	return s
}

// windowDiffers classifies how the body window starting at line from differs
// from old, by the weakest comparison under which every line agrees.
func windowDiffers(body, old string, bl, ol []line, from int) Differs {
	if from < 0 || from+len(ol) > len(bl) {
		return DiffersContent
	}
	level := DiffersLineEnding
	for k := range ol {
		b, o := content(body, bl[from+k]), content(old, ol[k])
		switch {
		case b == o:
		case trimTrailing(b) == trimTrailing(o):
			if level == DiffersLineEnding {
				level = DiffersTrailing
			}
		case strings.TrimSpace(b) == strings.TrimSpace(o):
			if level != DiffersContent {
				level = DiffersIndent
			}
		default:
			return DiffersContent
		}
	}
	return level
}
