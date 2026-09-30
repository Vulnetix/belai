package fleet

import (
	"context"

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
func (w *Worker) applyGates(ctx context.Context, claim *tools.WorkerClaim) {
	k := w.Profile.Kanban
	if k == nil || k.Gates == nil {
		return
	}
	claim.GatesRequired = k.Gates.Require
	claim.GateReview = k.Gates.Review
	claim.GateRoot = w.Repo
	for _, s := range w.detectedSuites(ctx) {
		claim.GateSuites = append(claim.GateSuites, tools.GateSuite{Name: s.Name, Ecosystem: s.Ecosystem})
	}
}
