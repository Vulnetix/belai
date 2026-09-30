package kanban

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Request coverage. A request card the scout works is split into clauses, the
// independently omittable parts of what was asked. Each handoff says which
// clauses it covers, and the harness files a gap card for any clause no
// handoff covers, so an omitted part of a request is visible on the board
// instead of quietly missing. The clauses are the scout's words, cleaned and
// capped; the harness's rule (a clause with no covering handoff is a gap) is
// deterministic.

// Limits.
const (
	// MaxClauses is the most clauses one request card carries.
	MaxClauses = 12
	// MaxClauseRunes caps one clause's text.
	MaxClauseRunes = 160
)

// Clause is one independently omittable part of a request.
type Clause struct {
	// ID is C1, C2, ... by position, assigned by the harness.
	ID   string
	Text string
}

var clauseIDRE = regexp.MustCompile(`^C([1-9]|1[0-2])$`)

// ErrClause marks clauses that failed validation.
var ErrClause = errors.New("kanban: invalid request clause")

// NormClauses validates the clause texts a scout records and assigns ids by
// position. Each text is cleaned to one line and capped; an empty or repeated
// clause, or more than MaxClauses, is an error.
func NormClauses(texts []string) ([]Clause, error) {
	if len(texts) == 0 {
		return nil, fmt.Errorf("%w: a request has at least one clause", ErrClause)
	}
	if len(texts) > MaxClauses {
		return nil, fmt.Errorf("%w: at most %d clauses; fold the rest into those", ErrClause, MaxClauses)
	}
	var out []Clause
	seen := map[string]bool{}
	for i, t := range texts {
		t = CleanTitle(t)
		if utf8.RuneCountInString(t) > MaxClauseRunes {
			r := []rune(t)
			t = strings.TrimSpace(string(r[:MaxClauseRunes-1])) + "…"
		}
		if t == "" {
			return nil, fmt.Errorf("%w: clause %d is empty", ErrClause, i+1)
		}
		key := strings.ToLower(t)
		if seen[key] {
			return nil, fmt.Errorf("%w: clause %d repeats an earlier one", ErrClause, i+1)
		}
		seen[key] = true
		out = append(out, Clause{ID: fmt.Sprintf("C%d", i+1), Text: t})
	}
	return out, nil
}

// NormCovers validates the clause ids a handoff says it covers against the
// parent's clauses, in order, without repeats. An unknown id is an error.
func NormCovers(covers []string, clauses []Clause) ([]string, error) {
	var out []string
	for _, c := range covers {
		c = strings.ToUpper(strings.TrimSpace(c))
		if !clauseIDRE.MatchString(c) || !slices.ContainsFunc(clauses, func(cl Clause) bool { return cl.ID == c }) {
			return nil, fmt.Errorf("%w: %q is not one of the request's clauses", ErrClause, c)
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out, nil
}

// CoverageGaps returns the clause ids of it that no child covers. A deleted
// child covers nothing.
func (it Item) CoverageGaps(children []Item) []string {
	covered := map[string]bool{}
	for _, ch := range children {
		if ch.Deleted || ch.Parent != it.ID {
			continue
		}
		for _, c := range ch.Covers {
			covered[c] = true
		}
	}
	var gaps []string
	for _, cl := range it.Clauses {
		if !covered[cl.ID] {
			gaps = append(gaps, cl.ID)
		}
	}
	return gaps
}

// Children returns the items handed off from the item, live ones only.
func (s *Store) Children(ref string) ([]Item, error) {
	var out []Item
	var ferr error
	err := s.read(func(b *Board) {
		i, err := find(b, ref)
		if err != nil {
			ferr = err
			return
		}
		id := b.Items[i].ID
		for _, it := range b.Items {
			if !it.Deleted && it.Parent == id {
				out = append(out, cloneItem(it))
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return out, ferr
}

// SetClauses records the request's clauses on a card. Only the worker holding
// the card's claim may, and the clauses replace any earlier ones.
func (s *Store) SetClauses(ref, holder string, texts []string) (Item, error) {
	clauses, err := NormClauses(texts)
	if err != nil {
		return Item{}, err
	}
	var out Item
	err = s.mutate(true, func(b *Board) error {
		i, err := find(b, ref)
		if err != nil {
			return err
		}
		it := &b.Items[i]
		if holder == "" || it.ClaimedBy != holder {
			return ErrLeaseLost
		}
		it.Clauses = clauses
		it.Updated, it.Dirty = s.nowMs(), true
		out = cloneItem(*it)
		return nil
	})
	return out, err
}
