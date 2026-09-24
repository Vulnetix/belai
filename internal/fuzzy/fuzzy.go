// Package fuzzy ranks short candidate strings — slash commands, library
// names — against a typed query. A candidate matches when every query rune
// appears in it in order (case-insensitive); the score rewards matches at the
// start, after a word boundary, and in consecutive runs, and penalises the
// runes skipped in between. It is deliberately small: the candidate lists are
// tens of entries and are ranked on every keystroke.
package fuzzy

import (
	"sort"
	"unicode"
)

const (
	matchBase      = 16
	bonusStart     = 24
	bonusBoundary  = 12
	bonusConsec    = 16
	penaltyGap     = 1
	maxLeadPenalty = 8
)

// Score reports whether query is a subsequence of target and how well it
// matches; higher is better. An empty query matches everything with score 0,
// so a caller's own ordering survives when nothing has been typed.
func Score(query, target string) (int, bool) {
	q := []rune(toLower(query))
	if len(q) == 0 {
		return 0, true
	}
	t := []rune(toLower(target))
	best, found := 0, false
	// Try every place the first rune could anchor and keep the best greedy
	// run from there: a later anchor can beat the leftmost one when it sits
	// on a boundary or starts a consecutive run.
	for start := range t {
		if t[start] != q[0] {
			continue
		}
		s, ok := scoreFrom(q, t, start)
		if ok && (!found || s > best) {
			best, found = s, true
		}
	}
	return best, found
}

func scoreFrom(q, t []rune, start int) (int, bool) {
	score := -min(start*penaltyGap, maxLeadPenalty)
	prev := -1
	ti := start
	for _, r := range q {
		for ti < len(t) && t[ti] != r {
			ti++
		}
		if ti == len(t) {
			return 0, false
		}
		score += matchBase
		switch {
		case ti == 0:
			score += bonusStart
		case isBoundary(t[ti-1]):
			score += bonusBoundary
		}
		if prev >= 0 {
			if ti == prev+1 {
				score += bonusConsec
			} else {
				score -= (ti - prev - 1) * penaltyGap
			}
		}
		prev = ti
		ti++
	}
	return score, true
}

func isBoundary(r rune) bool {
	switch r {
	case '-', '_', ':', '/', '.', ' ':
		return true
	}
	return false
}

func toLower(s string) string {
	out := []rune(s)
	for i, r := range out {
		out[i] = unicode.ToLower(r)
	}
	return string(out)
}

// Rank returns the items that match query, best first. Each item is scored
// against every string keys returns and keeps its best score. Ties keep the
// input order, so a caller's grouping and sort survive among equal matches.
// It returns nil when nothing matches.
func Rank[T any](query string, items []T, keys func(T) []string) []T {
	type scored struct {
		item  T
		score int
	}
	var hits []scored
	for _, it := range items {
		best, ok := 0, false
		for _, k := range keys(it) {
			if s, m := Score(query, k); m && (!ok || s > best) {
				best, ok = s, true
			}
		}
		if ok {
			hits = append(hits, scored{it, best})
		}
	}
	if len(hits) == 0 {
		return nil
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]T, len(hits))
	for i, h := range hits {
		out[i] = h.item
	}
	return out
}

// Strings ranks plain strings, each its own key.
func Strings(query string, items []string) []string {
	return Rank(query, items, func(s string) []string { return []string{s} })
}
