package kanban

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vulnetix/belai/internal/session"
)

// CheckpointPrefix starts the label that holds a card for a person. A card
// carrying one is never claimed by a worker: a flow stage that needs a human
// decision before the next stage runs routes its card with a checkpoint, and a
// person clears it with Decide (docs/kanban.md, "Human checkpoints").
const CheckpointPrefix = "checkpoint:"

// Checkpoint reasons. They are a closed set so the label, the notification
// text and the audit fact carry a name the harness chose, never model text.
const (
	CheckpointPlanApproval = "plan_approval"
	CheckpointRootCause    = "root_cause"
	CheckpointVisual       = "visual"
	CheckpointPrePR        = "pre_pr"
)

// CheckpointReasons lists every reason a stage may hold a card for.
var CheckpointReasons = []string{CheckpointPlanApproval, CheckpointRootCause, CheckpointVisual, CheckpointPrePR}

// ValidCheckpoint reports whether reason names a checkpoint.
func ValidCheckpoint(reason string) bool { return slices.Contains(CheckpointReasons, reason) }

// CheckpointLabel is the label that holds a card for reason.
func CheckpointLabel(reason string) string { return CheckpointPrefix + reason }

// HeldFor returns the reason a person must decide on the card before a worker
// may take it. A checkpoint label this Belai does not know holds the card too,
// so a newer Belai's checkpoint is never bypassed by an older one.
func HeldFor(it Item) (string, bool) {
	for _, l := range it.Labels {
		if r, ok := strings.CutPrefix(l, CheckpointPrefix); ok {
			return r, true
		}
	}
	return "", false
}

// ErrNotHeld means Decide was asked about a card no checkpoint holds.
var ErrNotHeld = errors.New("kanban: the card is not held at a checkpoint")

// ErrHeldClaimed refuses a decision on a card a worker holds.
var ErrHeldClaimed = errors.New("kanban: the card is claimed; a checkpoint is decided once its stage has released it")

// Decision is a person's answer at a checkpoint.
type Decision struct {
	// Approve lets the card go on: the checkpoint label is cleared and the
	// card stays in its list for the next stage to claim.
	Approve bool
	// Back is where a rejected card goes. A rejection clears the checkpoint
	// label too, so a card sent to backlog or review is claimable again, by
	// whichever stage's labels it still carries: a planner's handoff keeps its
	// build label, so a builder would build the rejected plan. Blocked is the
	// safe place, a person's to triage, and what the TUI and the website use.
	// Backlog and review count as a bounce, which bounds a card passed back and
	// forth.
	Back List
	// Note is the person's reason. It is kept in the card's history.
	Note string
}

// Decide records a person's decision at a checkpoint. It is the only way a
// checkpoint label leaves a card: a worker's outcome cannot drop one (see
// Release). The caller names who decided in sessionID; the board records the
// decision and the reason, not the person.
func (s *Store) Decide(ref string, d Decision, sessionID string) (Item, error) {
	if !d.Approve && !d.Back.Valid() {
		return Item{}, ErrBadList
	}
	var out Item
	err := s.mutate(true, func(b *Board) error {
		i, err := find(b, ref)
		if err != nil {
			return err
		}
		it := &b.Items[i]
		now := s.nowMs()
		if it.Claimed(now) || it.ClaimedBy != "" {
			return ErrHeldClaimed
		}
		reason, ok := HeldFor(*it)
		if !ok {
			return ErrNotHeld
		}
		it.Labels = slices.DeleteFunc(slices.Clone(it.Labels), func(l string) bool { return strings.HasPrefix(l, CheckpointPrefix) })
		verb := "approved"
		from, to := it.List, it.List
		if !d.Approve {
			verb, to = "rejected", d.Back
			if d.Back == Backlog || d.Back == Review {
				it.Bounces++
			}
			it.Attempts = 0
		}
		note := fmt.Sprintf("checkpoint %s %s", reason, verb)
		if n := CleanBody(d.Note, MaxNoteBytes); n != "" {
			note += ": " + n
		}
		appendHistory(it, Move{ID: session.MustID(), From: from, To: to, At: now, SessionID: sessionID, Note: CleanBody(note, MaxNoteBytes)})
		it.List, it.Updated, it.Dirty = to, now, true
		out = cloneItem(*it)
		return nil
	})
	return out, err
}

// isHeld reports whether a checkpoint holds the card.
func isHeld(it Item) bool {
	_, ok := HeldFor(it)
	return ok
}

// keepCheckpoints removes from drop every label that holds a card for a person,
// so a worker's outcome can clear its own labels but never a checkpoint.
func keepCheckpoints(drop []string) []string {
	return slices.DeleteFunc(slices.Clone(drop), func(l string) bool { return strings.HasPrefix(l, CheckpointPrefix) })
}
