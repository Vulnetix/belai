package tags

import (
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// matchText bounds how much of a document the detector reads.
const matchText = 64 << 10

// maxWords bounds the words considered, so a pathological file costs the same
// as an ordinary one.
const maxWords = 16000

// accept is the evidence a topic needs: three ordinary distinct terms, or one
// strong term and one ordinary one. score = raw / (raw + accept), so a topic
// that just qualifies scores 0.5.
const accept = 1.5

// Match runs the deterministic detector over a document's text and path and
// returns the topics it finds, best first, at most MaxTopics.
//
// The text is split into words at non-letters and at camelCase boundaries,
// lower-cased and lightly stemmed; each topic's word and phrase table is then
// looked up in one pass (linear in the text), and its anchored regular
// expressions are tried once. A term is counted once however often it
// appears, with a small bonus for repetition.
func Match(in Input) []Topic {
	load()
	text := head(in.Chunks, matchText)
	rel := strings.ToLower(strings.ReplaceAll(in.Rel, "\\", "/"))
	raw := make([]float64, len(topics))
	counts := map[string]int{}

	ws := words(text)
	if len(ws) > maxWords {
		ws = ws[:maxWords]
	}
	var key strings.Builder
	for i := range ws {
		key.Reset()
		for n := 1; n <= maxGram && i+n <= len(ws); n++ {
			if n > 1 {
				key.WriteByte(' ')
			}
			key.WriteString(ws[i+n-1])
			if _, ok := termIndex[key.String()]; ok {
				counts[key.String()]++
			}
		}
	}
	for k, n := range counts {
		bonus := 1 + 0.2*math.Min(math.Log2(float64(n)), 3)
		for _, h := range termIndex[k] {
			w := 0.5
			if h.strong {
				w = 1.0
			}
			raw[h.topic] += w * bonus
		}
	}
	for i, t := range topics {
		for _, p := range t.paths {
			if strings.Contains(rel, p) {
				raw[i] += 1.0
			}
		}
		if text != "" {
			for _, re := range t.res {
				if re.MatchString(text) {
					raw[i] += 1.0
				}
			}
		}
	}
	var out []Topic
	for i, r := range raw {
		if r >= accept {
			out = append(out, Topic{ID: topics[i].ID, Score: float32(r / (r + accept)), Src: SrcRegex})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > MaxTopics {
		out = out[:MaxTopics]
	}
	return out
}

// words splits text into lower-case stemmed words. A boundary falls at any
// non-letter and non-digit, between a lower-case letter and an upper-case one,
// and before the last capital of an acronym followed by a word (HTTPServer is
// http and server).
func words(text string) []string {
	var (
		out  []string
		cur  []rune
		prev rune
	)
	flush := func() {
		if len(cur) > 0 && len(cur) <= 40 {
			out = append(out, stem(strings.ToLower(string(cur))))
		}
		cur = cur[:0]
	}
	rs := []rune(text)
	for i, r := range rs {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			prev = 0
			continue
		}
		if len(cur) > 0 {
			switch {
			case unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev)):
				flush()
			case unicode.IsUpper(r) && unicode.IsUpper(prev) && i+1 < len(rs) && unicode.IsLower(rs[i+1]):
				flush()
			}
		}
		cur = append(cur, r)
		prev = r
	}
	flush()
	return out
}

// stem trims a plural s from a word of more than three letters.
func stem(w string) string {
	if utf8.RuneCountInString(w) > 3 && strings.HasSuffix(w, "s") &&
		!strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is") {
		return w[:len(w)-1]
	}
	return w
}
