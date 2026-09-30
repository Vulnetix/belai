package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vulnetix/belai/internal/kanban"
)

// KanbanGateName is the reviewer's manual-gate tool.
const KanbanGateName = "KanbanGate"

// maxGateEvidence caps the one line of evidence a manual gate keeps.
const maxGateEvidence = 200

// KanbanGate records a reviewer's decision on one manual acceptance gate of
// the item it holds. It is bound to the claim: it takes no item id, writes
// only the claimed item, and touches only a manual gate. A runnable gate is
// decided by the harness running its suite, so no model can mark one met. The
// harness reads the gates when the turn ends and routes the card from them.
type KanbanGate struct{ KanbanBase }

// Definition describes the tool.
func (t KanbanGate) Definition() Definition {
	desc := "Record your decision on one manual acceptance gate of the item you hold. " +
		"met: you checked the outcome and cite what you saw (file:line, a command and its result). " +
		"unmet: the outcome does not hold; say what is missing. " +
		"abandoned: the outcome is genuinely impossible within this task; a person must decide, and the card will not be done. " +
		"You cannot record a runnable gate: the harness runs those itself. " +
		"The harness moves the card from the gates when you finish; you never move it yourself."
	props := map[string]Property{
		"gate":     {Type: "string", Description: "The gate id, G1 to G8."},
		"state":    {Type: "string", Enum: []string{string(kanban.GateMet), string(kanban.GateUnmet), string(kanban.GateAbandoned)}, Description: "Your decision."},
		"evidence": {Type: "string", Description: "One line: what you checked and saw, or why it is unmet or impossible."},
	}
	return Definition{Name: KanbanGateName, Description: desc, Properties: props, Required: []string{"gate", "state", "evidence"}}
}

// Kind is the harness-composed confirmation.
func (KanbanGate) Kind() Kind { return KindKanbanWrite }

// Execute records the decision.
func (t KanbanGate) Execute(ctx context.Context, args map[string]any) (Result, error) {
	c := t.Claim
	if c == nil || !c.GateReview {
		return Result{}, errors.New("KanbanGate is only available to a reviewer holding a claim")
	}
	id, _ := argString(args, "gate")
	id = strings.ToUpper(strings.TrimSpace(id))
	rawState, _ := argString(args, "state")
	state := kanban.GateState(strings.TrimSpace(rawState))
	if !slices.Contains([]kanban.GateState{kanban.GateMet, kanban.GateUnmet, kanban.GateAbandoned}, state) {
		return Result{}, errors.New("state must be met, unmet or abandoned")
	}
	evidence, _ := argString(args, "evidence")
	evidence = oneLine(evidence, maxGateEvidence)
	if evidence == "" {
		return Result{}, errors.New("a gate decision needs one line of evidence")
	}
	if err := t.checkWorkerTarget(c.Item); err != nil {
		return Result{}, err
	}
	it, err := t.Store.Get(c.Item)
	if err != nil {
		return Result{}, kanbanErr(err, c.Item)
	}
	g, ok := it.GateByID(id)
	if !ok {
		return Result{}, fmt.Errorf("%s has no gate %q", it.Short(), id)
	}
	if g.Kind != kanban.GateManual {
		return Result{}, fmt.Errorf("gate %s is runnable: the harness decides it by running its suite, not you", id)
	}
	it, err = t.Store.SetGate(c.Item, c.Worker, id, state, "", evidence)
	if err != nil {
		return Result{}, kanbanErr(err, c.Item)
	}
	return kanbanWrite("gate", it, fmt.Sprintf("%s gate %s recorded: %s. The harness routes the card when you finish.", it.Short(), id, state)), nil
}

var _ Tool = KanbanGate{}
