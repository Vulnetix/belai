package tools

import (
	"context"
	"math"
	"sort"
	"strings"
	"unicode"
)

// Candidate is something ToolSearch can return: a deferred tool, or an
// installed skill. Descriptions are used to rank and are never returned; the
// result of a search names candidates only.
type Candidate struct {
	Name        string
	Description string
	// Skill marks an installed skill. It is found by the Skill tool, not
	// loaded into the tool list.
	Skill bool
}

// Key is the candidate's identity: tools and skills may share a name.
func (c Candidate) Key() string {
	if c.Skill {
		return "skill:" + c.Name
	}
	return "tool:" + c.Name
}

// SkillCatalog is implemented by a catalog that can also search installed
// skills. Skills whose author disabled model invocation are not offered.
type SkillCatalog interface {
	Skills() []Candidate
}

// SearchRanker refines a keyword search with a decision backend. det is the
// deterministic top of the search, pool the wider set the backend may add
// from; the result is the final ordered list, at most limit long. ok false
// means the ranker did not run and the deterministic list stands.
type SearchRanker interface {
	Rank(ctx context.Context, query string, det, pool []Candidate, limit int) (ranked []Candidate, ok bool)
}

// stopwords are dropped from queries and descriptions before ranking.
var stopwords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "to": true, "of": true, "for": true,
	"in": true, "on": true, "with": true, "is": true, "it": true, "this": true, "that": true, "be": true,
	"as": true, "at": true, "by": true, "from": true, "my": true, "me": true, "i": true, "you": true,
	"we": true, "please": true, "can": true, "could": true, "would": true, "should": true, "how": true,
	"what": true, "when": true, "where": true, "which": true, "do": true, "does": true, "did": true,
	"just": true, "into": true, "using": true, "use": true, "tool": true, "tools": true, "run": true,
}

// tokenize splits text into lower-case search terms: camelCase and snake_case
// words are separated, stopwords and one-letter words dropped, and a light
// stem applied so "reading", "reads" and "read" meet.
func tokenize(s string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = cur[:0]
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			// A capital after a lower-case letter or digit starts a new word.
			if unicode.IsUpper(r) && i > 0 && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1])) {
				flush()
			}
			cur = append(cur, unicode.ToLower(r))
		default:
			flush()
		}
	}
	flush()
	out := words[:0]
	for _, w := range words {
		if len(w) < 2 || stopwords[w] {
			continue
		}
		out = append(out, stem(w))
	}
	return out
}

// stem strips the common English endings so related words meet: "ies" to
// "y", plural "es" after s, x, z, ch and sh, a plain plural "s", "ing" and
// "ed". A word keeps at least three letters. The same stem is applied to the
// query and to the candidates, so it need not be a real word.
func stem(w string) string {
	switch {
	case len(w) >= 5 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) >= 5 && strings.HasSuffix(w, "es") && strings.ContainsAny(w[len(w)-3:len(w)-2], "sxz"):
		return w[:len(w)-2]
	case len(w) >= 6 && strings.HasSuffix(w, "es") && (strings.HasSuffix(w[:len(w)-2], "ch") || strings.HasSuffix(w[:len(w)-2], "sh")):
		return w[:len(w)-2]
	case len(w) >= 4 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is"):
		return w[:len(w)-1]
	case len(w) >= 6 && strings.HasSuffix(w, "ing"):
		return w[:len(w)-3]
	case len(w) >= 5 && strings.HasSuffix(w, "ed"):
		return w[:len(w)-2]
	}
	return w
}

// scored is a candidate with its search score.
type scored struct {
	c     Candidate
	score float64
}

// RankCandidates ranks candidates against a query with BM25 over each
// candidate's name (weighted three times) and description, plus a bonus for a
// query that names a candidate outright. Candidates with no matching term are
// left out. The order is deterministic: score, then name.
func RankCandidates(cands []Candidate, query string) []Candidate {
	terms := tokenize(query)
	if len(terms) == 0 || len(cands) == 0 {
		return nil
	}
	type doc struct {
		tf  map[string]float64
		len float64
	}
	docs := make([]doc, len(cands))
	df := map[string]int{}
	var total float64
	for i, c := range cands {
		tf := map[string]float64{}
		n := 0.0
		for _, w := range tokenize(c.Name) {
			tf[w] += 3
			n += 3
		}
		for _, w := range tokenize(c.Description) {
			tf[w]++
			n++
		}
		docs[i] = doc{tf: tf, len: n}
		total += n
		for w := range tf {
			df[w]++
		}
	}
	avg := total / float64(len(cands))
	if avg == 0 {
		avg = 1
	}
	const k1, b = 1.2, 0.75
	lowerQuery := strings.ToLower(query)
	var out []scored
	for i, c := range cands {
		s := 0.0
		for _, t := range terms {
			f := docs[i].tf[t]
			if f == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(cands))-float64(df[t])+0.5)/(float64(df[t])+0.5))
			s += idf * (f * (k1 + 1)) / (f + k1*(1-b+b*docs[i].len/avg))
		}
		if s == 0 {
			continue
		}
		if len(c.Name) >= 3 && strings.Contains(lowerQuery, strings.ToLower(c.Name)) {
			s *= 1.5
		}
		out = append(out, scored{c, s})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].c.Name < out[j].c.Name
	})
	ranked := make([]Candidate, len(out))
	for i, s := range out {
		ranked[i] = s.c
	}
	return ranked
}
