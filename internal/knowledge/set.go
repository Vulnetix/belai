package knowledge

import (
	"fmt"
	"sort"
)

// SearchLimit is the most chunks one index offers a search before the token
// cap trims the merged answer.
const SearchLimit = 24

// Set merges several indexes (a profile's, the project's, a session's) into
// one answer. The zero value and a nil *Set are empty.
type Set struct {
	idx []*Index
	// fn, when set, supplies the indexes afresh on every call, so a Store can
	// swap its profile index without the Set a session holds going stale.
	fn func() []*Index
}

func (s *Set) list() []*Index {
	if s.fn != nil {
		return s.fn()
	}
	return s.idx
}

// NewSet returns a set over the non-nil indexes, in the order given.
func NewSet(ix ...*Index) *Set {
	s := &Set{}
	for _, i := range ix {
		if i != nil {
			s.idx = append(s.idx, i)
		}
	}
	return s
}

// Empty reports whether the set holds no document at all.
func (s *Set) Empty() bool {
	if s == nil {
		return true
	}
	for _, i := range s.list() {
		if len(i.addresses()) > 0 {
			return false
		}
	}
	return true
}

// Search ranks every index's chunks against query and returns the best hits
// whose estimated size adds up to at most maxTokens (a hit larger than what is
// left is skipped, not cut). allow, when non-nil, drops a hit before it
// counts: it is how a deny rule on the source path applies to retrieval.
func (s *Set) Search(query string, maxTokens int, allow func(Hit) bool) []Hit {
	if s == nil {
		return nil
	}
	var all []Hit
	for _, i := range s.list() {
		all = append(all, i.search(query, SearchLimit)...)
	}
	sort.SliceStable(all, func(a, b int) bool {
		if all[a].Score != all[b].Score {
			return all[a].Score > all[b].Score
		}
		if all[a].Address != all[b].Address {
			return all[a].Address < all[b].Address
		}
		return all[a].Start < all[b].Start
	})
	var out []Hit
	used := 0
	// The same passage can sit in two indexes: a profile may list a directory
	// (.vulnetix) the project index covers too. One copy is enough.
	seen := map[string]bool{}
	for _, h := range all {
		if allow != nil && !allow(h) {
			continue
		}
		if h.Source != "" {
			key := fmt.Sprintf("%s\x00%d\x00%d", h.Source, h.Start, h.End)
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		if maxTokens > 0 && used+h.Tokens > maxTokens {
			continue
		}
		out = append(out, h)
		used += h.Tokens
	}
	return out
}

// Match returns the addresses of the documents for which match is true,
// sorted. match is given the address and the document's host source path, the
// latter for permission filtering only.
func (s *Set) Match(match func(address, source string) bool) []string {
	if s == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, i := range s.list() {
		for _, d := range i.Docs() {
			key := d.Source
			if key == "" {
				key = d.Address
			}
			if !seen[key] && match(d.Address, d.Source) {
				seen[key] = true
				out = append(out, d.Address)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Indexes returns the indexes the set searches, in order. A view that browses
// documents uses it to reach each one; the slice is the caller's.
func (s *Set) Indexes() []*Index {
	if s == nil {
		return nil
	}
	return append([]*Index(nil), s.list()...)
}
