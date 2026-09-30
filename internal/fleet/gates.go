package fleet

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/testdetect"
	"github.com/vulnetix/belai/internal/tools"
)

// Acceptance gates of a delivery card (kanban.Gate). A gate references a suite
// the harness detected; it never carries a command. This file holds what the
// worker tells a model about the gates it may name and, in verify.go, how the
// harness decides them.

// detectedSuites returns the repository's detected test suites: the harness's
// own table, from marker files, never from a model.
func (w *Worker) detectedSuites(ctx context.Context) []testdetect.Suite {
	return w.suitesAt(ctx, w.Repo)
}

// applyGates fills the claim's gate facts from the profile and the repository.
// A worker with no gates block keeps the handoff tool as it was.
func (w *Worker) applyGates(ctx context.Context, claim *tools.WorkerClaim, it kanban.Item) {
	k := w.Profile.Kanban
	if k == nil || k.Gates == nil {
		return
	}
	claim.GatesRequired = k.Gates.Require
	claim.GateReview = k.Gates.Review
	claim.Coverage = k.Gates.Coverage && w.plansRequest(it)
	claim.GateRoot = w.Repo
	for _, s := range w.detectedSuites(ctx) {
		claim.GateSuites = append(claim.GateSuites, tools.GateSuite{Name: s.Name, Ecosystem: s.Ecosystem})
	}
}

// plansRequest reports whether the card is a request the worker plans: not a
// card the harness seeded from a test run and not one the worker surveyed for
// itself, whose facts the harness already measured.
func (w *Worker) plansRequest(it kanban.Item) bool {
	return !slices.Contains(it.Labels, agentprofile.QualityLabel) && !slices.Contains(it.Labels, agentprofile.SurveyLabel)
}

// coverageActive reports whether the worker plans this card's request and so
// is held to covering it.
func (w *Worker) coverageActive(it kanban.Item) bool {
	k := w.Profile.Kanban
	return k != nil && k.Gates != nil && k.Gates.Coverage && w.plansRequest(it)
}

// reconcileCoverage holds a worker that planned a request to it. After a turn
// the model completed, the card must carry clauses (a request with none was
// never planned), and each clause no handoff covers gets a gap card, filed by
// the harness, so an omitted part of the request is visible on the board. A gap
// card names ids only (the parent card and the clause), never the clause's
// text, and carries no label a worker claims, so it waits for a person.
// FindingInput.Once files each gap once, never reopened after done and never
// doubled.
func (w *Worker) reconcileCoverage(ctx context.Context, o outcome, it kanban.Item) outcome {
	if o.failed || !w.coverageActive(it) {
		return o
	}
	cur, err := w.Store.Get(it.ID)
	if err != nil {
		return o
	}
	if len(cur.Clauses) == 0 {
		o.failed = true
		o.note = fmt.Sprintf("agent %s completed it, but recorded no clauses for the request: record them with KanbanContract, then cover each with a handoff", w.Profile.Name)
		return o
	}
	children, err := w.Store.Children(it.ID)
	if err != nil {
		return o
	}
	gaps := cur.CoverageGaps(children)
	suspects := w.coverageSuspects(ctx, cur, children)
	head, herr := w.headRef(ctx)
	if herr != nil {
		head = strings.Repeat("0", 40)
	}
	prov := kanban.ProvenanceFor(w.Repo, w.Record.ID, headless.HostID())
	filed := 0
	file := func(id, title, body string) {
		_, ch, err := w.Store.UpsertFinding(kanban.FindingInput{
			Finding:  "coverage:" + cur.Short() + ":" + id,
			Title:    title,
			Body:     body + "\n\ncategory: coverage",
			Priority: 1,
			Labels:   []string{CoverageLabel},
			Ref:      head, Once: true,
		}, prov)
		if err != nil {
			w.logf("%s: coverage gap %s: %v", it.Short(), id, err)
			return
		}
		if ch == kanban.FindingCreated {
			filed++
		}
	}
	for _, id := range gaps {
		file(id, fmt.Sprintf("Part %s of request %s is not covered by any task", id, cur.Short()),
			fmt.Sprintf("The scout worked request %s and no handed-off task covers its clause %s. Read the clause on that card, then plan it, file it as a task, or drop it.", cur.Short(), id))
	}
	// A clause a task claims to cover, that the decision model doubts the tasks
	// do, is filed the same way. It is never marked covered or uncovered by the
	// model: the recorded coverage above stands either way.
	var doubted []string
	for _, id := range suspects {
		if !slices.Contains(gaps, id) {
			doubted = append(doubted, id)
			file(id, fmt.Sprintf("Part %s of request %s may not be done by its tasks", id, cur.Short()),
				fmt.Sprintf("The tasks handed off for clause %s of request %s do not seem to do what it asks, by the decision model's rating. Read the clause on that card and those tasks, then plan what is missing or drop this.", id, cur.Short()))
		}
	}
	o.note += fmt.Sprintf("; coverage: %d of %d clauses covered", len(cur.Clauses)-len(gaps), len(cur.Clauses))
	if len(gaps) > 0 {
		o.note += ", gaps " + joinIDs(gaps)
	}
	if len(doubted) > 0 {
		o.note += ", doubted " + joinIDs(doubted)
	}
	if filed > 0 {
		o.note += fmt.Sprintf(", %d gap card(s) filed", filed)
	}
	return o
}

// CoverageLabel marks a gap card the harness filed for an uncovered clause. No
// worker profile claims it, so the card waits for a person.
const CoverageLabel = "coverage"
