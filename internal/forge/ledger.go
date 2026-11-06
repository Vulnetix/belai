package forge

import (
	"path/filepath"
	"sort"
)

// MaxLedgerPaths caps the paths a Ledger remembers, so a pathological session
// cannot grow it without bound. A path past the cap is not tracked, which
// leaves it out of every commit: the safe direction.
const MaxLedgerPaths = 2000

// Ledger remembers, for one session, which dirty paths the session's own tools
// made and which it shares with edits made before it. A path is "owned" when
// it was clean at the start of the first turn that changed it, and "mixed"
// when it was already dirty then: the edits already in it cannot be told from
// the turn's, so it is never committed on the session's word.
//
// A Ledger is not safe for concurrent use; the TUI keeps it on the UI loop.
type Ledger struct {
	owned map[string]bool
	mixed map[string]bool
}

// Before returns the paths to treat as "dirty before this turn": the tree's
// dirty paths at the turn's start minus the ones the session already owns, so a
// later turn refining its own file is not mistaken for a foreign edit.
func (l *Ledger) Before(dirty []string) []string {
	out := make([]string, 0, len(dirty))
	for _, p := range dirty {
		p = filepath.ToSlash(p)
		if !l.owned[p] {
			out = append(out, p)
		}
	}
	return out
}

// Settle records one finished turn: touched are the paths its tools changed
// and before is Before's result for that turn. With known false (the start
// snapshot could not be read) every touched path not already owned is mixed.
func (l *Ledger) Settle(before, touched []string, known bool) {
	was := toSet(before)
	for _, p := range touched {
		p = filepath.ToSlash(p)
		if p == "" || l.owned[p] || l.mixed[p] {
			continue
		}
		if len(l.owned)+len(l.mixed) >= MaxLedgerPaths {
			return
		}
		if !known || was[p] {
			l.setMixed(p)
		} else {
			l.setOwned(p)
		}
	}
}

func (l *Ledger) setOwned(p string) {
	if l.owned == nil {
		l.owned = map[string]bool{}
	}
	l.owned[p] = true
}

func (l *Ledger) setMixed(p string) {
	if l.mixed == nil {
		l.mixed = map[string]bool{}
	}
	l.mixed[p] = true
}

// Owned returns the session-owned paths, sorted.
func (l *Ledger) Owned() []string { return sortedKeys(l.owned) }

// Mixed returns the paths shared with earlier edits, sorted.
func (l *Ledger) Mixed() []string { return sortedKeys(l.mixed) }

// Touched returns every path the ledger holds, sorted.
func (l *Ledger) Touched() []string {
	m := map[string]bool{}
	for p := range l.owned {
		m[p] = true
	}
	for p := range l.mixed {
		m[p] = true
	}
	return sortedKeys(m)
}

// Prune forgets every path that is no longer dirty. A mixed path that has been
// committed or reverted is clean again, and a later edit to it starts afresh.
func (l *Ledger) Prune(now []string) {
	dirty := toSet(now)
	for p := range l.owned {
		if !dirty[p] {
			delete(l.owned, p)
		}
	}
	for p := range l.mixed {
		if !dirty[p] {
			delete(l.mixed, p)
		}
	}
}

// Committed forgets paths a commit took.
func (l *Ledger) Committed(paths []string) {
	for _, p := range paths {
		p = filepath.ToSlash(p)
		delete(l.owned, p)
		delete(l.mixed, p)
	}
}

// Reset empties the ledger, as a new or resumed session does.
func (l *Ledger) Reset() {
	l.owned, l.mixed = nil, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
