package mergefix

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
)

// Choice is how one hunk was resolved.
type Choice string

const (
	// Both keeps both sides, current first.
	Both Choice = "both"
	// BothIncomingFirst keeps both sides, incoming first.
	BothIncomingFirst Choice = "both-incoming-first"
	// Current keeps the pull request head's side only.
	Current Choice = "current"
	// Incoming keeps the base's side only.
	Incoming Choice = "incoming"
	// Mixed is built from the two sides' lines, but is none of the above.
	Mixed Choice = "mixed"
	// Other is new text, or a hunk the classifier could not place.
	Other Choice = "other"
)

// Uniform are the choices a strategy may name for a hunk.
var Uniform = []Choice{Both, BothIncomingFirst, Current, Incoming}

// ValidChoice reports whether c is a choice.
func ValidChoice(c Choice) bool {
	return slices.Contains(Uniform, c) || c == Mixed || c == Other
}

// NoteChoice is the word a note uses for a choice: both orders of keeping both
// sides are "both".
func (c Choice) NoteChoice() string {
	if c == BothIncomingFirst {
		return string(Both)
	}
	return string(c)
}

// HunkResult is the classification of one hunk. Digest tells two different
// mixed or other resolutions apart; it is empty for the uniform choices.
type HunkResult struct {
	File   string `json:"file"`
	Hunk   int    `json:"hunk"`
	Choice Choice `json:"choice"`
	Digest string `json:"digest,omitempty"`
}

// Key names the hunk in a choice vector.
func (r HunkResult) Key() string { return r.File + "#" + strconv.Itoa(r.Hunk) }

// entry is the hunk's element of the choice vector.
func (r HunkResult) entry() string {
	s := r.Key() + "=" + string(r.Choice)
	if r.Digest != "" {
		s += ":" + r.Digest
	}
	return s
}

// norm is a line as the classifier compares it: indentation and trailing
// space do not decide which side a line came from.
func norm(s string) string { return strings.TrimSpace(s) }

// nonblank returns the normalised, non-empty lines.
func nonblank(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if n := norm(l); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func digest(lines []string) string {
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:4])
}

// ClassifyRegion classifies the text a hunk was resolved to against its two
// sides. Blank lines, indentation and trailing space are ignored.
func ClassifyRegion(region, ours, theirs []string) (Choice, string) {
	x, o, t := nonblank(region), nonblank(ours), nonblank(theirs)
	switch {
	case slices.Equal(x, concat(o, t)):
		return Both, ""
	case slices.Equal(x, concat(t, o)):
		return BothIncomingFirst, ""
	case slices.Equal(x, o):
		return Current, ""
	case slices.Equal(x, t):
		return Incoming, ""
	}
	d := digest(x)
	if len(x) == 0 {
		return Other, d
	}
	sides := map[string]bool{}
	for _, l := range o {
		sides[l] = true
	}
	for _, l := range t {
		sides[l] = true
	}
	from := 0
	for _, l := range x {
		if sides[l] {
			from++
		}
	}
	if from*2 >= len(x) {
		return Mixed, d
	}
	return Other, d
}

func concat(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	return append(append(out, a...), b...)
}

// ClassifyWhole classifies a whole-file conflict by blob id: result is the
// resolved file's blob, "" when the file is gone.
func ClassifyWhole(oursBlob, theirsBlob, result string) (Choice, string) {
	switch result {
	case oursBlob:
		return Current, ""
	case theirsBlob:
		return Incoming, ""
	}
	d := result
	if d == "" {
		d = "deleted"
	}
	if len(d) > 8 {
		d = d[:8]
	}
	return Other, d
}

// anchorLines is how many lines of the common text on each side of a hunk
// locate it in the resolved file when the common text was edited.
const anchorLines = 3

// findSeq finds seq (non-blank, normalised lines) in r at or after from,
// skipping blank lines of r. It returns the index of the first matched line
// and the index just past the last, or -1, -1.
func findSeq(r []string, from int, seq []string) (int, int) {
	if len(seq) == 0 {
		return from, from
	}
	for p := max(from, 0); p < len(r); p++ {
		if norm(r[p]) != seq[0] {
			continue
		}
		j, q := 1, p+1
		for j < len(seq) && q < len(r) {
			n := norm(r[q])
			if n == "" {
				q++
				continue
			}
			if n != seq[j] {
				break
			}
			j++
			q++
		}
		if j == len(seq) {
			return p, q
		}
	}
	return -1, -1
}

// firstNonblank is the index of the first non-blank line of r at or after from.
func firstNonblank(r []string, from int) int {
	for p := from; p < len(r); p++ {
		if norm(r[p]) != "" {
			return p
		}
	}
	return len(r)
}

// Regions locates each hunk's resolution in the resolved lines: the text
// between the common text before it and the common text after it. A hunk that
// cannot be placed (its surroundings were rewritten) is {-1, -1}.
func Regions(resolved []string, f File) [][2]int {
	out := make([][2]int, len(f.Hunks))
	cur := 0
	for i := range f.Hunks {
		out[i] = [2]int{-1, -1}
		pre := nonblank(f.Common[i])
		start := -1
		if len(pre) == 0 {
			start = cur
		} else if s, e := findSeq(resolved, cur, pre); s >= 0 && s == firstNonblank(resolved, cur) {
			start = e
		} else {
			// The common text was edited: anchor on its last lines, fewer when
			// an edit falls among them.
			for n := min(anchorLines, len(pre)); n > 0 && start < 0; n-- {
				if _, e := findSeq(resolved, cur, pre[len(pre)-n:]); e >= 0 {
					start = e
				}
			}
		}
		if start < 0 {
			continue
		}
		post := nonblank(f.Common[i+1])
		end := -1
		switch {
		case len(post) == 0 && i == len(f.Hunks)-1:
			end = len(resolved)
		case len(post) == 0:
			// Two hunks with nothing between them cannot be told apart.
		default:
			for n := min(anchorLines, len(post)); n > 0 && end < 0; n-- {
				if s, _ := findSeq(resolved, start, post[:n]); s >= 0 {
					end = s
				}
			}
		}
		if end < 0 {
			cur = start
			continue
		}
		out[i] = [2]int{start, end}
		cur = end
	}
	return out
}

// Classify classifies every hunk of a text file against what it was resolved
// to. gone means the file no longer exists.
func Classify(f File, resolved []byte, gone bool) []HunkResult {
	out := make([]HunkResult, 0, len(f.Hunks))
	if gone {
		for _, h := range f.Hunks {
			out = append(out, HunkResult{File: f.Path, Hunk: h.Index, Choice: Other, Digest: "deleted"})
		}
		return out
	}
	r := SplitLines(resolved)
	regions := Regions(r, f)
	for i, h := range f.Hunks {
		reg := regions[i]
		if reg[0] < 0 {
			out = append(out, HunkResult{File: f.Path, Hunk: h.Index, Choice: Other, Digest: "unplaced"})
			continue
		}
		c, d := ClassifyRegion(r[reg[0]:reg[1]], h.Ours, h.Theirs)
		out = append(out, HunkResult{File: f.Path, Hunk: h.Index, Choice: c, Digest: d})
	}
	return out
}

// Resolve is the text choice c makes of a hunk. Mixed and Other have no
// fixed text and return nil.
func Resolve(h Hunk, c Choice) []string {
	switch c {
	case Both:
		return concat(h.Ours, h.Theirs)
	case BothIncomingFirst:
		return concat(h.Theirs, h.Ours)
	case Current:
		return slices.Clone(h.Ours)
	case Incoming:
		return slices.Clone(h.Theirs)
	}
	return nil
}

// Canonical is the classification the text of choice c would get: keeping both
// sides of a hunk whose current side is empty is the incoming side, and the
// classifier says so the same way for a prediction and a result.
func Canonical(f File, h Hunk, c Choice) Choice {
	if f.Whole {
		switch c {
		case Both, Current:
			return Current
		default:
			return Incoming
		}
	}
	got, _ := ClassifyRegion(Resolve(h, c), h.Ours, h.Theirs)
	return got
}
