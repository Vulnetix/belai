package knowledge

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// Hybrid ranking for the source index. A source query is often an exact name
// (parseConfig, ErrCapReached) that cosine similarity over hashed words ranks
// below a chunk that merely talks about parsing. Each source chunk therefore
// also gets BM25 postings over words and whole identifiers, and the two ranked
// lists are fused by reciprocal rank (rrfK). Nothing here calls a model or
// reads anything but chunk text the index already holds.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
	// rrfK is the reciprocal-rank-fusion constant.
	rrfK = 60
	// bm25Floor drops a BM25 candidate scoring under this share of the best
	// one, so a query's most common word does not pad the list.
	bm25Floor = 0.25
	// bm25Depth is how many candidates each list contributes to the fusion.
	bm25Depth = 100
)

var identRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// codeTerms is terms plus, for every identifier that terms splits (camelCase,
// snake_case, a digit run), the identifier whole and lower-cased, so an exact
// name is one term of its own.
func codeTerms(s string) []string {
	out := terms(s)
	for _, id := range identRe.FindAllString(s, -1) {
		if len(id) < 4 {
			continue
		}
		low := strings.ToLower(id)
		if parts := terms(id); len(parts) == 1 && parts[0] == low {
			continue
		}
		out = append(out, low)
	}
	return out
}

// codeVectorText is what a source chunk's term vector is computed from: the
// file's path and the names it declares, then the text. The path and names are
// not part of the stored text.
func codeVectorText(rel, symbol, text string) string {
	return rel + " " + symbol + "\n" + text
}

type bm25Posting struct {
	chunk int32
	tf    int32
}

// bm25 is the derived postings of a source index. It is rebuilt from the stored
// chunks whenever the index changes, so the snapshot format does not carry it.
type bm25 struct {
	post  map[string][]bm25Posting
	dl    []int32
	avgdl float64
}

func (ix *Index) buildBM25() {
	b := &bm25{post: map[string][]bm25Posting{}, dl: make([]int32, len(ix.chunks))}
	var total int64
	for ci, c := range ix.chunks {
		rel := ""
		if int(c.Doc) >= 0 && int(c.Doc) < len(ix.docs) {
			rel = relOf(ix.docs[c.Doc].Address)
		}
		words := codeTerms(codeVectorText(rel, c.Symbol, c.Text))
		b.dl[ci] = int32(len(words))
		total += int64(len(words))
		counts := map[string]int32{}
		for _, w := range words {
			counts[w]++
		}
		for w, n := range counts {
			b.post[w] = append(b.post[w], bm25Posting{chunk: int32(ci), tf: n})
		}
	}
	if len(ix.chunks) > 0 {
		b.avgdl = float64(total) / float64(len(ix.chunks))
	}
	ix.bm = b
}

type rankedChunk struct {
	c int32
	s float32
}

// bm25Rank returns the chunks that match query, best first, cut to depth and to
// the floor share of the best score. It holds the index's read lock.
func (ix *Index) bm25Rank(query string, depth int) []rankedChunk {
	b := ix.bm
	if b == nil || len(ix.chunks) == 0 {
		return nil
	}
	seen := map[string]bool{}
	score := map[int32]float64{}
	n := float64(len(ix.chunks))
	for _, w := range codeTerms(query) {
		if seen[w] {
			continue
		}
		seen[w] = true
		ps := b.post[w]
		if len(ps) == 0 {
			continue
		}
		df := float64(len(ps))
		idf := math.Log(1 + (n-df+0.5)/(df+0.5))
		for _, p := range ps {
			tf := float64(p.tf)
			norm := 1 - bm25B + bm25B*float64(b.dl[p.chunk])/math.Max(b.avgdl, 1)
			score[p.chunk] += idf * tf * (bm25K1 + 1) / (tf + bm25K1*norm)
		}
	}
	var all []rankedChunk
	var best float64
	for c, s := range score {
		if s > best {
			best = s
		}
		all = append(all, rankedChunk{c, float32(s)})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].s != all[j].s {
			return all[i].s > all[j].s
		}
		return all[i].c < all[j].c
	})
	cut := len(all)
	for i, a := range all {
		if float64(a.s) < best*bm25Floor {
			cut = i
			break
		}
	}
	all = all[:cut]
	if depth > 0 && len(all) > depth {
		all = all[:depth]
	}
	return all
}

// fuse merges a cosine ranking and a BM25 ranking by reciprocal rank. The score
// is scaled so a chunk first in both lists is 1, which keeps a source hit
// comparable with the cosine scores of the other indexes in a Set. Ties break
// toward the lexical list (an exact name is what a code query usually wants),
// then the cosine score, then the chunk's position, so the order is stable.
func fuse(cos, lex []rankedChunk) []rankedChunk {
	rrf := map[int32]float64{}
	cosScore := map[int32]float32{}
	lexRank := map[int32]int{}
	for i, r := range cos {
		rrf[r.c] += 1 / float64(rrfK+i+1)
		cosScore[r.c] = r.s
	}
	for i, r := range lex {
		rrf[r.c] += 1 / float64(rrfK+i+1)
		lexRank[r.c] = i + 1
	}
	lexOf := func(c int32) int {
		if r, ok := lexRank[c]; ok {
			return r
		}
		return math.MaxInt
	}
	out := make([]rankedChunk, 0, len(rrf))
	top := 2 / float64(rrfK+1)
	for c, v := range rrf {
		out = append(out, rankedChunk{c: c, s: float32(v / top)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].s != out[j].s {
			return out[i].s > out[j].s
		}
		if a, b := lexOf(out[i].c), lexOf(out[j].c); a != b {
			return a < b
		}
		if cosScore[out[i].c] != cosScore[out[j].c] {
			return cosScore[out[i].c] > cosScore[out[j].c]
		}
		return out[i].c < out[j].c
	})
	return out
}
