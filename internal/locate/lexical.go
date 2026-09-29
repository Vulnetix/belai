package locate

import (
	"math"
	"path"
	"sort"
	"strings"
	"unicode"
)

// Lexical scoring: BM25 over the words of a path and, when read, the names a
// file declares. It is the order used when no Rater answers, the pre-filter
// that decides what a Rater is asked about, and the tie-break under it.

var stop = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true, "this": true, "from": true, "into": true,
	"how": true, "what": true, "where": true, "when": true, "which": true, "does": true, "code": true,
	"find": true, "file": true, "files": true, "please": true, "show": true, "goal": true,
}

// words splits text into lower-case words: camelCase, snake_case, kebab-case
// and path separators all separate; short words and stop words go. A plural
// "s" is trimmed so "handlers" meets "handler".
func words(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) >= 3 || hasDigit(cur) && len(cur) >= 2 {
			w := string(cur)
			if len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
				w = w[:len(w)-1]
			}
			if !stop[w] {
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

// doc is a scored document: a path with optional declaration names.
type doc struct {
	path  string
	tf    map[string]float64
	len   float64
	decls []Decl
}

func newDoc(p string, decls []Decl) *doc {
	d := &doc{path: p, tf: map[string]float64{}, decls: decls}
	// The name of the file weighs most, then its directories, then what it
	// declares.
	base := path.Base(p)
	for _, w := range words(strings.TrimSuffix(base, path.Ext(base))) {
		d.tf[w] += 3
		d.len += 3
	}
	for _, w := range words(path.Dir(p)) {
		d.tf[w]++
		d.len++
	}
	for _, dc := range decls {
		for _, w := range words(dc.Name) {
			d.tf[w] += 2
			d.len += 2
		}
	}
	return d
}

// bm25 scores docs against query words. Scores are relative: divide by the
// best to normalise.
func bm25(docs []*doc, query []string) []float64 {
	scores := make([]float64, len(docs))
	if len(docs) == 0 || len(query) == 0 {
		return scores
	}
	df := map[string]int{}
	var total float64
	for _, d := range docs {
		total += d.len
		for w := range d.tf {
			df[w]++
		}
	}
	avg := total / float64(len(docs))
	if avg == 0 {
		avg = 1
	}
	const k1, b = 1.2, 0.75
	seen := map[string]bool{}
	var uniq []string
	for _, q := range query {
		if !seen[q] {
			seen[q] = true
			uniq = append(uniq, q)
		}
	}
	for i, d := range docs {
		for _, q := range uniq {
			f := d.tf[q]
			if f == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(docs))-float64(df[q])+0.5)/(float64(df[q])+0.5))
			scores[i] += idf * (f * (k1 + 1)) / (f + k1*(1-b+b*d.len/avg))
		}
	}
	return scores
}

// normalise scales scores to 0..1 by the best.
func normalise(scores []float64) []float64 {
	best := 0.0
	for _, s := range scores {
		best = math.Max(best, s)
	}
	out := make([]float64, len(scores))
	if best == 0 {
		return out
	}
	for i, s := range scores {
		out[i] = s / best
	}
	return out
}

// bestLine is the line of the declaration that shares the most words with the
// query, or 0.
func bestLine(decls []Decl, query []string) int {
	want := map[string]bool{}
	for _, q := range query {
		want[q] = true
	}
	best, line := 0, 0
	for _, d := range decls {
		n := 0
		for _, w := range words(d.Name) {
			if want[w] {
				n++
			}
		}
		if n > best {
			best, line = n, d.Line
		}
	}
	return line
}

// rank returns the indexes of docs by descending score, path breaking ties so
// the order is stable between runs.
func rank(docs []*doc, scores []float64) []int {
	idx := make([]int, len(docs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		if scores[idx[a]] != scores[idx[b]] {
			return scores[idx[a]] > scores[idx[b]]
		}
		return docs[idx[a]].path < docs[idx[b]].path
	})
	return idx
}
