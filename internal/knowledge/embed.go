package knowledge

import (
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"unicode"
)

// Dim is the size of the hashed vector space. Vectors are sparse in it, so a
// large space costs nothing and keeps unrelated words from sharing a bucket.
const Dim = 1 << 16

// feat is one non-zero coordinate of a hashed vector.
type feat struct {
	B uint32
	W float32
}

// Feature weights: a word is the strongest signal, a word pair carries a little
// order, and a character trigram lets "authenticate" meet "authentication".
const (
	wordWeight    = 1.0
	pairWeight    = 0.6
	trigramWeight = 0.25
	minTrigramLen = 5
)

// terms splits text into lower-case words. A camelCase or snake_case name
// separates, a word of one or two letters goes, and a plural s is trimmed.
// Unlike a path ranker's, the stop list is only the filler of running prose:
// "code" and "file" are real terms in a document.
func terms(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) >= 3 || len(cur) == 2 && hasDigit(cur) {
			w := string(cur)
			if len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
				w = w[:len(w)-1]
			}
			if !stopWords[w] {
				out = append(out, w)
			}
		}
		cur = cur[:0]
	}
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if unicode.IsUpper(r) && i > 0 && (unicode.IsLower(rs[i-1]) || unicode.IsDigit(rs[i-1])) {
				flush()
			}
			cur = append(cur, unicode.ToLower(r))
		default:
			flush()
		}
	}
	flush()
	return out
}

func hasDigit(rs []rune) bool {
	for _, r := range rs {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true, "this": true, "from": true,
	"are": true, "was": true, "were": true, "has": true, "have": true, "not": true, "but": true,
	"can": true, "will": true, "you": true, "your": true, "its": true, "into": true, "than": true,
	"then": true, "when": true, "which": true, "what": true, "how": true, "does": true,
}

func hashTerm(s string) (bucket uint32, sign float32) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	v := h.Sum64()
	sign = 1
	if v>>63 == 1 {
		sign = -1
	}
	return uint32(v % Dim), sign
}

// termFreqs turns text into the sparse term-frequency vector of its features.
// The result is sorted by bucket and holds a sublinear count (1+ln n) per
// bucket, signed so that two unrelated terms that collide tend to cancel
// rather than add.
func termFreqs(text string) []feat {
	words := terms(text)
	if len(words) == 0 {
		return nil
	}
	acc := map[uint32]float64{}
	add := func(term string, weight float64) {
		b, sign := hashTerm(term)
		acc[b] += float64(sign) * weight
	}
	counts := map[string]int{}
	for _, w := range words {
		counts[w]++
	}
	for w, n := range counts {
		scale := 1 + math.Log(float64(n))
		add(w, wordWeight*scale)
		if r := []rune(w); len(r) >= minTrigramLen {
			for i := 0; i+3 <= len(r); i++ {
				add("#"+string(r[i:i+3]), trigramWeight*scale)
			}
		}
	}
	pairs := map[string]int{}
	for i := 0; i+1 < len(words); i++ {
		pairs[words[i]+" "+words[i+1]]++
	}
	for p, n := range pairs {
		add("&"+p, pairWeight*(1+math.Log(float64(n))))
	}
	out := make([]feat, 0, len(acc))
	for b, w := range acc {
		if w != 0 {
			out = append(out, feat{B: b, W: float32(w)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].B < out[j].B })
	return out
}
