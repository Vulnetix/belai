package kanban

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/vulnetix/belai/internal/session"
)

// Verdict is a security worker's recorded conclusion about one finding.
type Verdict string

// Verdicts. Rejected is the verifier's alone: it sends the card back to a
// patcher.
const (
	VerdictFixed         Verdict = "fixed"
	VerdictFalsePositive Verdict = "false_positive"
	VerdictNoFix         Verdict = "no_fix"
	VerdictNeedsHuman    Verdict = "needs_human"
	VerdictRejected      Verdict = "rejected"
)

// Valid reports whether v is a known verdict.
func (v Verdict) Valid() bool {
	switch v {
	case VerdictFixed, VerdictFalsePositive, VerdictNoFix, VerdictNeedsHuman, VerdictRejected:
		return true
	}
	return false
}

// Labels a security card carries. LabelVuln routes it to a patcher,
// LabelGone marks a finding that left the scan report, and LabelNeedsVerify
// routes it to the verifier.
const (
	LabelVuln        = "vuln"
	LabelGone        = "gone"
	LabelNeedsVerify = "needs-verify"
)

var (
	findingRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
	refRE     = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// CleanFinding returns the advisory id when it is a plain identifier.
func CleanFinding(s string) (string, bool) {
	s = strings.TrimSpace(s)
	return s, findingRE.MatchString(s)
}

// CleanRef returns a lower-cased full commit id, or "" for anything else.
func CleanRef(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if refRE.MatchString(s) {
		return s
	}
	return ""
}

// FindingInput is one finding a scan reported. Every field is composed by
// the harness from parsed artefacts, never from a model.
type FindingInput struct {
	Finding  string
	Title    string
	Body     string
	Priority int
	Labels   []string
	// Ref is the commit of the scan that reported it.
	Ref string
	// Once files the card only when no card at all (open or done) carries the
	// finding: a finished card is never reopened. A card whose title an open
	// card in the project already has is not filed either, so hand-filed work
	// is not doubled.
	Once bool
}

// FindingChange says what UpsertFinding did.
type FindingChange string

// Changes.
const (
	FindingCreated   FindingChange = "created"
	FindingRefreshed FindingChange = "refreshed"
	FindingReopened  FindingChange = "reopened"
)

// UpsertFinding makes sure exactly one unfinished card exists for a finding
// in the project. A card that is already open has its SeenRef refreshed; one
// that had been marked gone and is unclaimed goes back to the backlog, since
// the finding is present again. When every card for the finding is done, a
// new card is created with the latest done card as its parent.
func (s *Store) UpsertFinding(in FindingInput, prov Provenance) (Item, FindingChange, error) {
	id, ok := CleanFinding(in.Finding)
	if !ok {
		return Item{}, "", fmt.Errorf("kanban: %q is not a finding id", in.Finding)
	}
	ref := CleanRef(in.Ref)
	if ref == "" {
		return Item{}, "", fmt.Errorf("kanban: %q is not a commit id", in.Ref)
	}
	title := CleanTitle(in.Title)
	if title == "" {
		return Item{}, "", ErrNoTitle
	}
	var out Item
	var change FindingChange
	err := s.mutate(true, func(b *Board) error {
		now := s.nowMs()
		open, lastDone, sameTitle := -1, -1, -1
		deletedSeen := false
		live := 0
		want := normTitle(title)
		for i, it := range b.Items {
			if it.Deleted {
				if it.Finding == id && sameProject(it, prov) {
					deletedSeen = true
				}
				continue
			}
			live++
			if it.List != Done && sameTitle < 0 && normTitle(it.Title) == want && sameProject(it, prov) {
				sameTitle = i
			}
			if it.Finding != id || !sameProject(it, prov) {
				continue
			}
			if it.List != Done {
				open = i
			} else if lastDone < 0 || it.Updated > b.Items[lastDone].Updated {
				lastDone = i
			}
		}
		if open >= 0 {
			it := &b.Items[open]
			change = FindingRefreshed
			changed := false
			if it.SeenRef != ref {
				changed = true
				it.SeenRef, it.Updated, it.Dirty = ref, now, true
			}
			if it.List == Review && slices.Contains(it.Labels, LabelGone) && it.ClaimedBy == "" {
				appendHistory(it, Move{ID: session.MustID(), From: it.List, To: Backlog, At: now, SessionID: prov.SessionID,
					Note: "present again in the scan of " + ref[:12]})
				labels := slices.DeleteFunc(slices.Clone(it.Labels), func(l string) bool { return l == LabelGone || l == LabelNeedsVerify })
				it.Labels = NormLabels(append(labels, LabelVuln))
				it.List, it.Verdict, it.Updated, it.Dirty = Backlog, "", now, true
				change, changed = FindingReopened, true
			}
			out = cloneItem(*it)
			if !changed {
				return errNoWrite
			}
			return nil
		}
		if in.Once {
			// A finished card, or an open one a person filed under the same
			// title, already covers it.
			switch {
			case lastDone >= 0:
				out, change = cloneItem(b.Items[lastDone]), FindingRefreshed
				return errNoWrite
			case deletedSeen:
				// A person deleted it; that stands.
				out, change = Item{}, FindingRefreshed
				return errNoWrite
			case sameTitle >= 0:
				out, change = cloneItem(b.Items[sameTitle]), FindingRefreshed
				return errNoWrite
			}
		}
		if live >= MaxItems {
			return ErrFull
		}
		nid, err := session.NewID()
		if err != nil {
			return err
		}
		parent := ""
		if lastDone >= 0 {
			parent = b.Items[lastDone].ID
		}
		item := Item{
			ID: nid, Title: title, Body: CleanBody(in.Body, MaxBodyBytes), List: Backlog,
			Project: CleanTitle(prov.Project), ProjectKey: prov.ProjectKey, Dir: prov.Dir,
			HostID: prov.HostID, SessionID: prov.SessionID,
			Created: now, Updated: now, Dirty: true,
			Labels: NormLabels(in.Labels), Priority: ClampPriority(in.Priority), Parent: parent,
			Finding: id, SeenRef: ref,
		}
		appendHistory(&item, Move{ID: session.MustID(), To: Backlog, At: now, SessionID: prov.SessionID})
		b.Items = append(b.Items, item)
		out, change = cloneItem(item), FindingCreated
		if lastDone >= 0 {
			change = FindingReopened
		}
		return nil
	})
	if errors.Is(err, errNoWrite) {
		return out, change, nil
	}
	return out, change, err
}

// Reconcile finds the unfinished cards of a project whose finding is absent
// from the scan of ref and was last seen on another commit. Each unclaimed
// one becomes a "gone" card: verdict fixed, in review for the verifier, and
// no longer offered to a patcher. Cards that are claimed, blocked or done are
// left alone, and so is one already marked gone. It returns the cards it
// changed. covered says whether the scan looked for a finding at all (a kind
// whose scanner did not run proves nothing); nil covers every finding.
func (s *Store) Reconcile(prov Provenance, present map[string]bool, ref string, covered func(finding string) bool) ([]Item, error) {
	ref = CleanRef(ref)
	if ref == "" {
		return nil, fmt.Errorf("kanban: not a commit id")
	}
	var out []Item
	err := s.mutate(true, func(b *Board) error {
		now := s.nowMs()
		for i := range b.Items {
			it := &b.Items[i]
			if it.Deleted || it.Finding == "" || !sameProject(*it, prov) {
				continue
			}
			if covered != nil && !covered(it.Finding) {
				continue
			}
			if it.List == Done || it.List == Blocked || it.ClaimedBy != "" || present[it.Finding] || it.SeenRef == ref {
				continue
			}
			if it.List == Review && slices.Contains(it.Labels, LabelGone) {
				continue
			}
			note := "absent from the scan of " + ref[:12]
			if it.SeenRef != "" {
				note += "; last seen at " + it.SeenRef[:12]
			}
			appendHistory(it, Move{ID: session.MustID(), From: it.List, To: Review, At: now, SessionID: prov.SessionID, Note: note})
			labels := slices.DeleteFunc(slices.Clone(it.Labels), func(l string) bool { return l == LabelVuln })
			it.Labels = NormLabels(append(labels, LabelGone, LabelNeedsVerify))
			it.List, it.Verdict, it.Updated, it.Dirty = Review, VerdictFixed, now, true
			out = append(out, cloneItem(*it))
		}
		if len(out) == 0 {
			return errNoWrite
		}
		return nil
	})
	if errors.Is(err, errNoWrite) {
		return nil, nil
	}
	return out, err
}

// SetVerdict records a security verdict on the item holder has claimed,
// appending note (the worker's cleaned justification) to its history. It never
// changes the list: routing is the harness's, from the verdict, at release.
func (s *Store) SetVerdict(ref, holder string, v Verdict, note, sessionID string) (Item, error) {
	if !v.Valid() {
		return Item{}, fmt.Errorf("kanban: %q is not a verdict", v)
	}
	var out Item
	err := s.mutate(true, func(b *Board) error {
		i, err := find(b, ref)
		if err != nil {
			return err
		}
		it := &b.Items[i]
		if holder == "" || it.ClaimedBy != holder {
			return ErrLeaseLost
		}
		now := s.nowMs()
		it.Verdict, it.Updated, it.Dirty = v, now, true
		if n := CleanBody(note, MaxNoteBytes); n != "" {
			appendHistory(it, Move{ID: session.MustID(), From: it.List, To: it.List, At: now, SessionID: sessionID, Note: n})
		}
		out = cloneItem(*it)
		return nil
	})
	return out, err
}

// CloseAbsent moves to done the unfinished, unclaimed cards of a project
// whose finding starts with prefix, is absent from present, and was last seen
// on another commit than ref. It is for cards whose subject has gone away (a
// failing test that passes now) and that need no verifier. covered says
// whether the run looked for the finding at all; nil covers every finding. It
// returns the cards it closed.
func (s *Store) CloseAbsent(prov Provenance, prefix string, present map[string]bool, ref string, covered func(finding string) bool) ([]Item, error) {
	ref = CleanRef(ref)
	if ref == "" || prefix == "" {
		return nil, fmt.Errorf("kanban: a prefix and a commit id are needed")
	}
	var out []Item
	err := s.mutate(true, func(b *Board) error {
		now := s.nowMs()
		for i := range b.Items {
			it := &b.Items[i]
			if it.Deleted || !strings.HasPrefix(it.Finding, prefix) || !sameProject(*it, prov) {
				continue
			}
			if it.List == Done || it.ClaimedBy != "" || present[it.Finding] || it.SeenRef == ref {
				continue
			}
			if covered != nil && !covered(it.Finding) {
				continue
			}
			appendHistory(it, Move{ID: session.MustID(), From: it.List, To: Done, At: now, SessionID: prov.SessionID,
				Note: "no longer reported at " + ref[:12]})
			it.List, it.Updated, it.Dirty = Done, now, true
			out = append(out, cloneItem(*it))
		}
		if len(out) == 0 {
			return errNoWrite
		}
		return nil
	})
	if errors.Is(err, errNoWrite) {
		return nil, nil
	}
	return out, err
}
