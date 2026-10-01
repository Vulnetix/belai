package tags

import (
	"sort"
	"strings"
)

// Sample picks the chunks of a document to show a decision backend. When the
// document has at most maxChunks chunks and they fit maxBytes, all of them are
// sent in order. Otherwise the sample is the first chunk, the last and the
// ones between spread evenly, as many as maxChunks and maxBytes allow; a chunk
// that alone is larger than the budget is cut to it. The result keeps document
// order, so the backend reads the text as it was written.
func Sample(chunks []string, maxChunks, maxBytes int) []string {
	if len(chunks) == 0 || maxBytes <= 0 {
		return nil
	}
	if maxChunks <= 0 {
		maxChunks = 1
	}
	total := 0
	for _, c := range chunks {
		total += len(c)
	}
	if len(chunks) <= maxChunks && total <= maxBytes {
		return append([]string(nil), chunks...)
	}
	want := maxChunks
	if want > len(chunks) {
		want = len(chunks)
	}
	// Spread want indexes over the chunks, always including both ends.
	pick := func(n int) []int {
		if n == 1 {
			return []int{0}
		}
		idx := make([]int, 0, n)
		for i := 0; i < n; i++ {
			idx = append(idx, i*(len(chunks)-1)/(n-1))
		}
		return dedupe(idx)
	}
	for n := want; n >= 1; n-- {
		idx := pick(n)
		size := 0
		for _, i := range idx {
			size += len(chunks[i])
		}
		if size <= maxBytes || n == 1 {
			out := make([]string, 0, len(idx))
			room := maxBytes
			for _, i := range idx {
				c := chunks[i]
				if len(c) > room {
					c = cut(c, room)
				}
				if c == "" {
					continue
				}
				out = append(out, c)
				room -= len(c)
			}
			return out
		}
	}
	return nil
}

func dedupe(idx []int) []int {
	sort.Ints(idx)
	out := idx[:0]
	for i, v := range idx {
		if i == 0 || v != idx[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// Merge combines the deterministic topics with a decision backend's scores.
// asked is the set of topic ids the backend was asked about and scores holds
// the valid answers. A topic that was asked and answered takes the backend's
// verdict: kept at or above cut with its score, dropped below it, even when the
// detector found it. A topic that was not asked, or was asked and left
// unanswered, keeps the detector's verdict. The result is best first, at most
// MaxTopics.
func Merge(found []Topic, asked map[string]bool, scores map[string]float64, cut float64) []Topic {
	var out []Topic
	seen := map[string]bool{}
	for id, s := range scores {
		if !asked[id] {
			continue
		}
		if _, ok := Lookup(id); !ok {
			continue
		}
		seen[id] = true
		if s >= cut {
			out = append(out, Topic{ID: id, Score: float32(s), Src: SrcJev})
		}
	}
	for _, t := range found {
		if seen[t.ID] {
			continue
		}
		out = append(out, t)
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

// LabelText is the harness-composed single line indexed beside a document's text so
// a search by label finds it. It holds only label keys and values, the topic
// labels from the vocabulary and the words of the path: nothing from the
// document's own text except what those tables name.
func LabelText(r Result, rel string) string {
	var b strings.Builder
	b.WriteString("labels:")
	for _, l := range r.All() {
		b.WriteByte(' ')
		b.WriteString(l)
	}
	if r.Lang != "" {
		b.WriteString(" ; language: " + r.Lang)
		if r.Lang == "Go" {
			b.WriteString(" golang")
		}
	}
	if len(r.Topics) > 0 {
		b.WriteString(" ; topics:")
		for i, t := range r.Topics {
			if d, ok := Lookup(t.ID); ok {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString(" " + d.Label)
			}
		}
	}
	if rel != "" {
		b.WriteString(" ; path: " + strings.ReplaceAll(rel, "/", " "))
	}
	return b.String()
}
