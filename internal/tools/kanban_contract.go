package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/kanban"
)

// KanbanContractName is the scout's request-clause tool.
const KanbanContractName = "KanbanContract"

// KanbanContract records the clauses of the request card the worker holds: the
// independently omittable parts of what was asked. It is bound to the claim,
// takes no item id and writes only the claimed card. Each handoff then says
// which clauses it covers, and the harness files a gap card for a clause no
// handoff covers.
type KanbanContract struct{ KanbanBase }

// Definition describes the tool.
func (t KanbanContract) Definition() Definition {
	desc := "Record the clauses of the request on the item you hold, before you file any handoff. " +
		"A clause is one independently omittable part of what was asked: something a reader would notice missing if it were not done. " +
		"Include every part, and every constraint that changes what counts as done; leave out optional ideas. " +
		fmt.Sprintf("At most %d clauses of at most %d characters each; the harness numbers them C1, C2 and so on. ", kanban.MaxClauses, kanban.MaxClauseRunes) +
		"Then give each handoff the clauses it covers. Recording again replaces the list, and is refused once a handoff exists."
	props := map[string]Property{
		"clauses": {Type: "array", Items: &Property{Type: "string"}, Description: "One short sentence per clause."},
	}
	return Definition{Name: KanbanContractName, Description: desc, Properties: props, Required: []string{"clauses"}}
}

// Kind is the harness-composed confirmation.
func (KanbanContract) Kind() Kind { return KindKanbanWrite }

// Execute records the clauses.
func (t KanbanContract) Execute(ctx context.Context, args map[string]any) (Result, error) {
	c := t.Claim
	if c == nil || !c.Coverage {
		return Result{}, errors.New("KanbanContract is only available to a worker that plans a request and holds a claim")
	}
	if len(c.HandedOff()) > 0 {
		return Result{}, errors.New("the clauses are recorded before the first handoff; a handoff already exists")
	}
	texts, err := argStrings(args, "clauses")
	if err != nil {
		return Result{}, err
	}
	if err := t.checkWorkerTarget(c.Item); err != nil {
		return Result{}, err
	}
	it, err := t.Store.SetClauses(c.Item, c.Worker, texts)
	if err != nil {
		return Result{}, kanbanErr(err, c.Item)
	}
	var b strings.Builder
	for _, cl := range it.Clauses {
		b.WriteString(" " + cl.ID)
	}
	return kanbanWrite("contract", it, fmt.Sprintf("%s has %d clauses:%s. Give each handoff the clauses it covers.", it.Short(), len(it.Clauses), b.String())), nil
}

// parseCovers reads a handoff's covers argument against the parent's clauses.
// A worker that plans a request must cover at least one clause with every
// handoff, and must have recorded the clauses first.
func (c *WorkerClaim) parseCovers(store *kanban.Store, args map[string]any) ([]string, error) {
	if !c.Coverage {
		return nil, nil
	}
	parent, err := store.Get(c.Item)
	if err != nil {
		return nil, kanbanErr(err, c.Item)
	}
	if len(parent.Clauses) == 0 {
		return nil, errors.New("record the request's clauses with KanbanContract before filing a handoff")
	}
	covers, err := argStrings(args, "covers")
	if err != nil {
		return nil, err
	}
	covers, err = kanban.NormCovers(covers, parent.Clauses)
	if err != nil {
		return nil, err
	}
	if len(covers) == 0 {
		return nil, errors.New("every handoff must say which of the request's clauses it covers (covers); a task that covers none is not part of the request")
	}
	return covers, nil
}

var _ Tool = KanbanContract{}
