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
		open, lastDone := -1, -1
		live := 0
		for i, it := range b.Items {
			if it.Deleted {
				continue
			}
			live++
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
// changed.
func (s *Store) Reconcile(prov Provenance, present map[string]bool, ref string) ([]Item, error) {
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
