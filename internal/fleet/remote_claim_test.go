package fleet

import (
	"context"
	"os/exec"
	"testing"

	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
)

// A worker started in its own project claims a card filed in another
// checkout of the same repository: another machine's project key differs,
// and the Software Factory writes the project as owner/name. A card of
// another repository with the same name stays put.
func TestWorkerClaimsTheRepositorysCardFromAnotherMachine(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	store, reg := testEnv(t)
	p := builderProfile()
	p.Kanban.Project = "current"
	var claimed []string
	w := newWorker(t, store, reg, p, func(ctx context.Context, tt Turn) (run.Result, error) {
		if tt.Claim != nil {
			claimed = append(claimed, tt.Claim.Item)
		}
		return complete(ctx, tt)
	})
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@github.com:Acme/API.git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = w.Repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	other, _, err := store.Add(kanban.ItemInput{Title: "same name, other owner", Labels: []string{"build"}},
		kanban.Provenance{Project: "api", ProjectKey: "api-0badc0de", Remote: "someone/api"})
	if err != nil {
		t.Fatal(err)
	}
	mine, _, err := store.Add(kanban.ItemInput{Title: "filed on the laptop", Labels: []string{"build"}},
		kanban.Provenance{Project: "api", ProjectKey: "api-deadbeef", Remote: "acme/api"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0] != mine.ID {
		t.Fatalf("claimed %v, want only %s", claimed, mine.ID)
	}
	if got, _ := store.Get(other.ID); got.List != kanban.Backlog {
		t.Fatalf("the other repository's card moved to %s", got.List)
	}
	if r := w.claimRequest("x"); r.Remote != "acme/api" {
		t.Fatalf("claim remote %q", r.Remote)
	}
}
