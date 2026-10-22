package kanban

import (
	"strings"
)

// The duplicate guard keeps two agents from building the same fix.
//
// A security card stands for a finding (an advisory id) and, for a dependency
// finding, for the package a fix changes. Several advisories on one package
// are cleared by the same bump, and a finding whose card is done but whose pull
// request is still waiting for review is still present in the next scan. Left
// alone, each of those became a fresh card and a fresh pull request for the
// same change. The guard holds back a backlog card while another card already
// stands for the same fix and is either being worked or has a pull request that
// is still open. It reads harness fields only (Finding, PR, ClaimedBy, the
// package and ecosystem lines the harness wrote into the body), never model
// text, and it runs under the board lock so two workers cannot both pass it.

// Lane is the fix a dependency card stands for: the package a change touches,
// whichever advisory asked for it.
type Lane struct {
	Ecosystem string
	Package   string
}

// LaneOf returns the lane of a harness-filed dependency card, or false for a
// card with no finding or no package line.
func LaneOf(it Item) (Lane, bool) {
	if it.Finding == "" {
		return Lane{}, false
	}
	var l Lane
	for _, line := range strings.Split(it.Body, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.ToLower(strings.TrimSpace(v))
		switch strings.TrimSpace(k) {
		case "package":
			if l.Package == "" {
				l.Package = v
			}
		case "ecosystem":
			if l.Ecosystem == "" {
				l.Ecosystem = v
			}
		}
	}
	return l, l.Package != ""
}

func (l Lane) key() string { return l.Ecosystem + "\x00" + l.Package }

// Hold says which card already stands for the fix a held card would build.
type Hold struct {
	// By is the short id of the card in front.
	By string
	// PR is its open pull request, empty while it is only being worked.
	PR string
	// Why is "same finding" or "same package".
	Why string
}

// String is the sentence a log line or a card note carries.
func (h Hold) String() string {
	s := h.Why + " as " + h.By
	if h.PR != "" {
		s += ", whose pull request " + h.PR + " is still open"
	} else {
		s += ", which a worker is on"
	}
	return s
}

// holder is one card that is in front of any later card for the same fix.
type holder struct {
	id, short, pr string
}

// holdIndex finds, for a card, the card in front of it. A card is in front when
// a worker holds its claim, or it has a pull request the forge still reports
// open.
type holdIndex struct {
	byFinding map[string][]holder
	byLane    map[string][]holder
}

// newHoldIndex indexes the board's holders. open reports whether a pull request
// url is still open; nil means the forge could not be asked, and then only the
// claims count.
func newHoldIndex(b *Board, now int64, open func(pr string) bool) holdIndex {
	h := holdIndex{byFinding: map[string][]holder{}, byLane: map[string][]holder{}}
	for _, it := range b.Items {
		if it.Deleted || it.Finding == "" {
			continue
		}
		pr := ""
		switch {
		case it.PR != "" && open != nil && open(it.PR):
			pr = it.PR
		case it.Claimed(now):
		default:
			continue
		}
		hd := holder{id: it.ID, short: it.Short(), pr: pr}
		h.byFinding[it.Finding] = append(h.byFinding[it.Finding], hd)
		if l, ok := LaneOf(it); ok {
			h.byLane[l.key()] = append(h.byLane[l.key()], hd)
		}
	}
	return h
}

// of returns the card in front of it, if any. A card is never in front of itself.
func (h holdIndex) of(it Item) (Hold, bool) {
	if it.Finding == "" {
		return Hold{}, false
	}
	first := func(hs []holder) (holder, bool) {
		for _, hd := range hs {
			if hd.id != it.ID {
				return hd, true
			}
		}
		return holder{}, false
	}
	if hd, ok := first(h.byFinding[it.Finding]); ok {
		return Hold{By: hd.short, PR: hd.pr, Why: "same finding"}, true
	}
	if l, ok := LaneOf(it); ok {
		if hd, ok := first(h.byLane[l.key()]); ok {
			return Hold{By: hd.short, PR: hd.pr, Why: "same package"}, true
		}
	}
	return Hold{}, false
}

// Held lists the unclaimed backlog cards of a security finding that another
// card is in front of, by item id. open is the claim request's OpenPR.
func (s *Store) Held(open func(pr string) bool) (map[string]Hold, error) {
	out := map[string]Hold{}
	err := s.read(func(b *Board) {
		now := s.nowMs()
		idx := newHoldIndex(b, now, open)
		for _, it := range b.Items {
			if it.Deleted || it.List != Backlog || it.ClaimedBy != "" {
				continue
			}
			if h, ok := idx.of(it); ok {
				out[it.ID] = h
			}
		}
	})
	return out, err
}
