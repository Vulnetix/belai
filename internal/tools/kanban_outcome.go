package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vulnetix/belai/internal/kanban"
)

// KanbanOutcomeName is the tool a worker records how its item ends with.
const KanbanOutcomeName = "KanbanOutcome"

// Outcome is how a worker says its item ended.
type Outcome string

// The outcomes a worker may record.
const (
	// OutcomeSuccess: the work is done and the item moves on.
	OutcomeSuccess Outcome = "success"
	// OutcomeFailure: the work is not done and the item goes back, counting an
	// attempt. A review that rejects a branch is a failure.
	OutcomeFailure Outcome = "failure"
	// OutcomeBlocked: the work cannot continue without a person.
	OutcomeBlocked Outcome = "blocked"
)

// Outcomes lists the outcomes in the order the tool offers them.
func Outcomes() []Outcome { return []Outcome{OutcomeSuccess, OutcomeFailure, OutcomeBlocked} }

// maxOutcomeReason caps the one line of reason an outcome keeps.
const maxOutcomeReason = 400

// OutcomeRecord is the outcome a worker recorded on its claim. The harness
// reads it after the turn to route the item. The reason is already cleaned.
type OutcomeRecord struct {
	Outcome Outcome
	Reason  string
}

// RecordedOutcome returns the outcome the worker recorded this claim, if any. A
// later call replaces an earlier one.
func (c *WorkerClaim) RecordedOutcome() (OutcomeRecord, bool) {
	if c == nil {
		return OutcomeRecord{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.outcome == nil {
		return OutcomeRecord{}, false
	}
	return *c.outcome, true
}

func (c *WorkerClaim) recordOutcome(r OutcomeRecord) {
	c.mu.Lock()
	c.outcome = &r
	c.mu.Unlock()
}

// KanbanOutcome records how the item the worker holds ends: done, not done, or
// stuck. It is the structured form of the decision a model otherwise only
// implies by how it words its last message, which a separate evaluator has to
// read back. It is bound to the claim: it takes no item id, sets no list and
// writes only the claimed item. The harness routes the card from the record when
// the turn ends, after its own checks: a recorded success does not override an
// unmet gate or a missing verdict, and a recorded failure or block always stands.
type KanbanOutcome struct{ KanbanBase }

// Definition describes the tool.
func (t KanbanOutcome) Definition() Definition {
	desc := "Record how the item you hold ends, once you have decided. " +
		"success: the work is done, including everything the task asked for beyond the change itself (publishing it, recording verdicts and gate decisions). " +
		"failure: the work is not done or not acceptable, and the item goes back with your reasons; a review that rejects a branch is a failure. " +
		"blocked: the work cannot continue without a person; say what is needed. " +
		"Call it last. The turn ends when you do, and the harness moves the card from this record; you never move it yourself."
	props := map[string]Property{
		"outcome": {Type: "string", Enum: outcomeNames(), Description: "How the item ends."},
		"reason":  {Type: "string", Description: "One line: why. For a failure or a block, what is wrong or missing."},
	}
	return Definition{Name: KanbanOutcomeName, Description: desc, Properties: props, Required: []string{"outcome", "reason"}}
}

func outcomeNames() []string {
	var out []string
	for _, o := range Outcomes() {
		out = append(out, string(o))
	}
	return out
}

// Kind is the harness-composed confirmation.
func (KanbanOutcome) Kind() Kind { return KindKanbanWrite }

// Execute records the outcome.
func (t KanbanOutcome) Execute(ctx context.Context, args map[string]any) (Result, error) {
	c := t.Claim
	if c == nil {
		return Result{}, errors.New("KanbanOutcome is only available to a worker that holds a claim")
	}
	raw, _ := argString(args, "outcome")
	o := Outcome(strings.TrimSpace(raw))
	if !slices.Contains(Outcomes(), o) {
		return Result{}, fmt.Errorf("outcome must be one of: %s", strings.Join(outcomeNames(), ", "))
	}
	reason, _ := argString(args, "reason")
	reason = oneLine(reason, maxOutcomeReason)
	if reason == "" {
		return Result{}, errors.New("an outcome needs a one-line reason")
	}
	if err := t.checkWorkerTarget(c.Item); err != nil {
		return Result{}, err
	}
	note := "outcome " + string(o) + ": " + reason
	it, err := t.Store.UpdateAs(c.Item, kanban.Patch{Note: note}, t.prov().SessionID, t.holder())
	if err != nil {
		return Result{}, kanbanErr(err, c.Item)
	}
	c.recordOutcome(OutcomeRecord{Outcome: o, Reason: reason})
	return kanbanWrite("outcome", it, fmt.Sprintf("%s outcome recorded: %s. The turn ends now and the harness routes the card.", it.Short(), o)), nil
}

var _ Tool = KanbanOutcome{}
