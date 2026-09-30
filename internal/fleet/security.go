package fleet

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vulnetix/belai/internal/commands"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/scanartifacts"
	"github.com/vulnetix/belai/internal/vulnetixcli"
)

// The security duties of a worker (kanban.security). Everything here is
// harness code: it runs the review, reads the artefacts, files and
// reconciles the cards. A model is never asked whether a scan ran or a
// finding exists, and no scanner or advisory text reaches a card: a card
// holds identifiers, versions, paths and a severity word.

// artefactDir is where the Vulnetix CLI leaves a repository's scan artefacts.
func (w *Worker) artefactDir() string { return filepath.Join(w.Repo, ".vulnetix") }

// headRef is the full commit id of the repository's HEAD.
func (w *Worker) headRef(ctx context.Context) (string, error) {
	if w.Head != nil {
		return w.Head(ctx)
	}
	out, err := git(ctx, hardenedGit("", ""), w.Repo, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", err
	}
	if ref := kanban.CleanRef(out); ref != "" {
		return ref, nil
	}
	return "", fmt.Errorf("HEAD is not a commit id")
}

// runReview runs the fixed review scanner table over the repository.
func (w *Worker) runReview(ctx context.Context) error {
	if w.Review != nil {
		return w.Review(ctx)
	}
	cli, err := vulnetixcli.Detect()
	if err != nil {
		return err
	}
	_, err = commands.Vulnetix{CLI: cli, Workdir: w.Repo}.Run(ctx)
	return err
}

// securityStep runs the worker's harness duties before it looks for a card:
// the sweep once per start, then a reconcile from the artefacts already on
// disk. It never fails the worker; a problem is logged and the worker goes on
// to claim what the board holds.
func (w *Worker) securityStep(ctx context.Context, project string) {
	if w.Profile.Kanban == nil || w.Profile.Kanban.Security == nil || w.Item != "" {
		return
	}
	sec := w.Profile.Kanban.Security
	switch {
	case sec.Sweep && !w.swept:
		w.swept = true
		// A working worker keeps its crew mates waiting rather than letting
		// them leave while the scan runs.
		w.Record.State = StateWorking
		w.save()
		w.sweep(ctx)
		w.Record.State = StateIdle
		w.save()
	case sec.Reconcile:
		w.reconcileFromDisk(ctx)
	}
}

// sweep makes sure a review ran on HEAD and that every finding it reports has
// a card. The only gate is evidence in the artefacts that HEAD was scanned:
// there is no clock, cache or daily limit.
func (w *Worker) sweep(ctx context.Context) {
	head, err := w.headRef(ctx)
	if err != nil {
		w.logf("security sweep: no HEAD: %v", err)
		return
	}
	dir := w.artefactDir()
	if ok, ev := scanartifacts.ReviewedAt(dir, head); ok {
		w.logf("security sweep: %s already carries a review of %s; not scanning", strings.Join(ev.Files, ", "), head[:12])
	} else {
		w.logf("security sweep: no review of %s; running it", head[:12])
		if err := w.runReview(ctx); err != nil {
			w.logf("security sweep: review: %v", err)
		}
		if ok, _ := scanartifacts.ReviewedAt(dir, head); !ok {
			w.logf("security sweep: the review left no artefact for %s; filing nothing", head[:12])
			return
		}
	}
	w.syncCards(head, true)
}

// reconcileFromDisk compares the cards with the artefacts already on disk. It
// never scans: when no artefact records HEAD it leaves the cards alone and
// lets the sweep worker bring them up to date.
func (w *Worker) reconcileFromDisk(ctx context.Context) {
	head, err := w.headRef(ctx)
	if err != nil {
		return
	}
	sig := w.artefactSignature(head)
	if sig == w.reconciled {
		return
	}
	if ok, _ := scanartifacts.ReviewedAt(w.artefactDir(), head); !ok {
		return
	}
	w.syncCards(head, false)
	w.reconciled = sig
}

// artefactSignature names the state of HEAD and the artefacts, so an
// unchanged state is not read again on every poll. It only skips repeat work:
// the card operations are idempotent, so a stale signature costs nothing.
func (w *Worker) artefactSignature(head string) string {
	var b strings.Builder
	b.WriteString(head)
	entries, _ := os.ReadDir(w.artefactDir())
	for _, e := range entries {
		name := e.Name()
		if name != "memory.yaml" && !strings.HasSuffix(name, ".sarif") {
			continue
		}
		if fi, err := e.Info(); err == nil {
			fmt.Fprintf(&b, "|%s:%d:%d", name, fi.ModTime().UnixNano(), fi.Size())
		}
	}
	return b.String()
}

// syncCards files a card for each finding (when upsert is set) and turns the
// cards of findings that left the report into gone cards for the verifier.
// Only kinds whose artefact records head take part in the reconcile.
func (w *Worker) syncCards(head string, upsert bool) {
	res := scanartifacts.ReadReviewFindings(w.artefactDir(), head)
	if len(res.Covered) == 0 {
		w.logf("security sweep: no artefact for %s can be read; cards left as they are", head[:12])
		return
	}
	prov := kanban.ProvenanceFor(w.Repo, w.Record.ID, headless.HostID())
	present := make(map[string]bool, len(res.Findings))
	created, refreshed, reopened := 0, 0, 0
	for _, f := range res.Findings {
		present[f.ID] = true
		if !upsert {
			continue
		}
		_, ch, err := w.Store.UpsertFinding(findingCard(f, head), prov)
		if err != nil {
			w.logf("security sweep: card for %s: %v", f.ID, err)
			continue
		}
		switch ch {
		case kanban.FindingCreated:
			created++
		case kanban.FindingReopened:
			reopened++
		default:
			refreshed++
		}
	}
	gone, err := w.Store.Reconcile(prov, present, head, func(id string) bool { return res.Covered[scanartifacts.KindOfID(id)] })
	if err != nil {
		w.logf("security sweep: reconcile: %v", err)
	}
	if upsert || len(gone) > 0 {
		w.logf("security sweep: %d findings at %s: %d new cards, %d reopened, %d already carded, %d gone from the report", len(res.Findings), head[:12], created, reopened, refreshed, len(gone))
	}
}

// findingCard composes a card from a parsed finding: identifiers only.
func findingCard(f scanartifacts.ReviewFinding, ref string) kanban.FindingInput {
	var title strings.Builder
	title.WriteString("[" + f.Kind + "] ")
	if f.Kind == scanartifacts.KindSCA {
		title.WriteString(f.ID)
		if f.Package != "" {
			title.WriteString(" " + f.Package)
		}
	} else {
		title.WriteString(f.Rule)
		if f.File != "" {
			title.WriteString(" " + f.File)
		}
	}
	var body strings.Builder
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&body, "%s: %s\n", k, v)
		}
	}
	line("finding", f.ID)
	line("kind", f.Kind)
	line("rule", f.Rule)
	line("package", f.Package)
	line("ecosystem", f.Ecosystem)
	line("version", f.Version)
	line("file", f.File)
	if f.Line > 0 {
		line("line", fmt.Sprint(f.Line))
	}
	line("severity", f.Severity)
	line("first seen at", ref[:12])
	return kanban.FindingInput{
		Finding: f.ID, Title: title.String(), Body: body.String(),
		Priority: severityPriority(f.Severity), Labels: []string{kanban.LabelVuln}, Ref: ref,
	}
}

func severityPriority(s string) int {
	switch s {
	case "critical":
		return 3
	case "high":
		return 2
	case "medium":
		return 1
	}
	return 0
}
