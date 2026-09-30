package tools

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/vex"
)

func verdictSetup(t *testing.T, verdicts []string, vexOn bool) (KanbanVerdict, *WorkerClaim, *kanban.Store, kanban.Item) {
	t.Helper()
	store := kanban.Open(filepath.Join(t.TempDir(), "kanban"))
	added, _, err := store.Add(kanban.ItemInput{Title: "[vuln] CVE-1 lodash", Labels: []string{"vuln"}}, kanban.Provenance{Project: "p", ProjectKey: "p-1"})
	if err != nil {
		t.Fatal(err)
	}
	it, err := store.Claim(kanban.ClaimRequest{Worker: "w1", Profile: "patcher", Project: "p", Host: "h", Lease: time.Minute, Labels: []string{"vuln"}})
	if err != nil {
		t.Fatal(err)
	}
	if it.ID != added.ID {
		t.Fatal("claimed the wrong item")
	}
	claim := &WorkerClaim{Worker: "w1", Item: it.ID, Verdicts: verdicts, VEX: vexOn}
	return KanbanVerdict{KanbanBase{Store: store, Claim: claim}}, claim, store, it
}

func call(t KanbanVerdict, args map[string]any) error {
	_, err := t.Execute(context.Background(), args)
	return err
}

func TestKanbanVerdictRecordsOnTheClaimedItemOnly(t *testing.T) {
	tool, claim, store, it := verdictSetup(t, []string{"fixed", "false_positive", "no_fix", "needs_human"}, false)
	err := call(tool, map[string]any{
		"verdict": "no_fix", "justification": "upstream has no release\nwith the patch",
		"tried": []any{"bumped to 4.18 (does not exist)", "checked the advisory for a backport"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if got.Verdict != kanban.VerdictNoFix || got.List != kanban.InProgress {
		t.Fatalf("item %+v: the tool must set the verdict and leave the list alone", got)
	}
	if note := got.LastNote(); !strings.HasPrefix(note, "verdict no_fix: upstream has no release with the patch") || !strings.Contains(note, "tried: bumped to 4.18") {
		t.Fatalf("note %q", note)
	}
	rec, ok := claim.Recorded()
	if !ok || rec.Verdict != kanban.VerdictNoFix || len(rec.Tried) != 2 {
		t.Fatalf("recorded %+v %v", rec, ok)
	}
}

func TestKanbanVerdictEnforcesTheAllowedVerdicts(t *testing.T) {
	tool, _, _, _ := verdictSetup(t, []string{"fixed", "false_positive", "no_fix", "needs_human"}, false)
	if err := call(tool, map[string]any{"verdict": "rejected", "justification": "x"}); err == nil {
		t.Fatal("a patcher rejected a claim")
	}
	if err := call(tool, map[string]any{"verdict": "wontfix", "justification": "x"}); err == nil {
		t.Fatal("an unknown verdict was accepted")
	}
	if err := call(tool, map[string]any{"verdict": "fixed"}); err == nil {
		t.Fatal("a verdict without a justification was accepted")
	}
	if err := call(tool, map[string]any{"verdict": "no_fix", "justification": "x"}); err == nil {
		t.Fatal("no_fix without what was tried was accepted")
	}
	if err := call(tool, map[string]any{"verdict": "false_positive", "justification": "x"}); err == nil {
		t.Fatal("a false positive without evidence was accepted")
	}
	tooMany := []any{"1", "2", "3", "4", "5", "6"}
	if err := call(tool, map[string]any{"verdict": "false_positive", "justification": "x", "evidence": tooMany}); err == nil {
		t.Fatal("six evidence items were accepted")
	}
}

func TestKanbanVerdictNeedsAVEXReasonOnlyForTheVEXWorker(t *testing.T) {
	tool, claim, _, _ := verdictSetup(t, []string{"fixed", "false_positive", "rejected"}, true)
	args := map[string]any{"verdict": "false_positive", "justification": "never imported", "evidence": []any{"grep -rn lodash.template . prints nothing"}}
	if err := call(tool, args); err == nil || !strings.Contains(err.Error(), "vex_reason") {
		t.Fatalf("err = %v", err)
	}
	args["vex_reason"] = "made up"
	if err := call(tool, args); err == nil {
		t.Fatal("a made-up justification was accepted")
	}
	args["vex_reason"] = string(vex.NotInExecutePath)
	if err := call(tool, args); err != nil {
		t.Fatal(err)
	}
	if rec, _ := claim.Recorded(); rec.VEXReason != vex.NotInExecutePath {
		t.Fatalf("recorded %+v", rec)
	}
	if err := call(tool, map[string]any{"verdict": "rejected", "justification": "the branch still pins 4.17.0"}); err != nil {
		t.Fatalf("the VEX worker may reject: %v", err)
	}
}

func TestKanbanVerdictIsBoundToItsClaim(t *testing.T) {
	tool, _, store, it := verdictSetup(t, []string{"fixed"}, false)
	// The lease is lost: another worker holds the item now.
	if _, err := store.Unclaim(it.ID, "taken", "", false); err != nil {
		t.Fatal(err)
	}
	if err := call(tool, map[string]any{"verdict": "fixed", "justification": "done"}); err == nil {
		t.Fatal("a verdict was recorded without holding the claim")
	}
	none := KanbanVerdict{KanbanBase{Store: store}}
	if err := call(none, map[string]any{"verdict": "fixed", "justification": "done"}); err == nil {
		t.Fatal("a session with no claim recorded a verdict")
	}
}

func TestKanbanVerdictIsOnlyOnTheSurfaceOfAWorkerWithVerdicts(t *testing.T) {
	store := kanban.Open(filepath.Join(t.TempDir(), "kanban"))
	names := func(claim *WorkerClaim) []string {
		var out []string
		for _, d := range NewRegistry().WithKanbanWorker(store, nil, claim).Definitions() {
			out = append(out, d.Name)
		}
		return out
	}
	if slices.Contains(names(&WorkerClaim{Worker: "w", Item: "i"}), KanbanVerdictName) {
		t.Fatal("a worker without verdicts got KanbanVerdict")
	}
	if !slices.Contains(names(&WorkerClaim{Worker: "w", Item: "i", Verdicts: []string{"fixed"}}), KanbanVerdictName) {
		t.Fatal("a worker with verdicts has no KanbanVerdict")
	}
	if !IsCoreTool(KanbanVerdictName) {
		t.Fatal("KanbanVerdict must be advertised in full")
	}
}

func TestKanbanUpdateNotableItemsTakeNotesOnly(t *testing.T) {
	store := kanban.Open(filepath.Join(t.TempDir(), "kanban"))
	mine, _, _ := store.Add(kanban.ItemInput{Title: "the sweep item", Labels: []string{"vuln-scan"}}, kanban.Provenance{Project: "p", ProjectKey: "p-1"})
	card, _, _ := store.Add(kanban.ItemInput{Title: "a finding card"}, kanban.Provenance{Project: "p", ProjectKey: "p-1"})
	other, _, _ := store.Add(kanban.ItemInput{Title: "somebody else's"}, kanban.Provenance{Project: "p", ProjectKey: "p-1"})
	if _, err := store.ClaimID(mine.ID, kanban.ClaimRequest{Worker: "w1", Profile: "scout", Host: "h", Lease: time.Minute}); err != nil {
		t.Fatal(err)
	}
	claim := &WorkerClaim{Worker: "w1", Item: mine.ID, Notable: []string{card.ID}}
	up := KanbanUpdate{KanbanBase{Store: store, Claim: claim}}
	ctx := context.Background()

	if _, err := up.Execute(ctx, map[string]any{"id": card.ID, "note": "bump to 4.17.21"}); err != nil {
		t.Fatalf("a note on a notable card: %v", err)
	}
	for name, args := range map[string]map[string]any{
		"title": {"id": card.ID, "title": "renamed"},
		"body":  {"id": card.ID, "body": "rewritten"},
		"note on an item the claim does not name": {"id": other.ID, "note": "x"},
	} {
		if _, err := up.Execute(ctx, args); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// The claimed item itself stays fully editable by its holder.
	if _, err := up.Execute(ctx, map[string]any{"id": mine.ID, "note": "progress"}); err != nil {
		t.Fatalf("a note on the claimed item: %v", err)
	}
	// A card a patcher claimed since is refused a note.
	if _, err := store.ClaimID(card.ID, kanban.ClaimRequest{Worker: "p1", Profile: "patcher", Host: "h", Lease: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := up.Execute(ctx, map[string]any{"id": card.ID, "note": "too late"}); err == nil {
		t.Fatal("a note on a card another worker holds was accepted")
	}
}
