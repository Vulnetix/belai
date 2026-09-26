package projectregistry

import (
	"context"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/repomap"
)

func TestRepoMapClaimSingleFlight(t *testing.T) {
	workdir := t.TempDir()
	head := "abc1234"

	// First claimer wins.
	claimed, err := ClaimRepoMap(workdir, head, "sess-1")
	if err != nil || !claimed {
		t.Fatalf("first claim = %v, %v; want true", claimed, err)
	}
	// A second claimer while running and fresh does not.
	claimed, err = ClaimRepoMap(workdir, head, "sess-2")
	if err != nil || claimed {
		t.Fatalf("second claim = %v, %v; want false", claimed, err)
	}

	// Storing makes it ready and waitable.
	if err := StoreRepoMap(workdir, repomap.Map{Module: "/r", Head: head}); err != nil {
		t.Fatal(err)
	}
	m, ok := WaitRepoMap(context.Background(), workdir, head, time.Second)
	if !ok || m.Head != head {
		t.Fatalf("WaitRepoMap = %+v, %v; want ready", m, ok)
	}

	// A ready map for the same HEAD is not re-claimed.
	claimed, err = ClaimRepoMap(workdir, head, "sess-3")
	if err != nil || claimed {
		t.Fatalf("claim over ready = %v, %v; want false", claimed, err)
	}
}

func TestRepoMapStaleClaimReclaimed(t *testing.T) {
	workdir := t.TempDir()
	head := "deadbee"

	if _, err := ClaimRepoMap(workdir, head, "sess-1"); err != nil {
		t.Fatal(err)
	}
	// Backdate the running claim so it is stale.
	if err := Mutate(func(r *Registry) error {
		e := r.entryByKey(entryKey(workdir))
		if e == nil {
			t.Fatal("entry missing")
		}
		e.RepoMapAt = time.Now().Add(-2 * RepoMapStaleAfter)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := ClaimRepoMap(workdir, head, "sess-2")
	if err != nil || !claimed {
		t.Fatalf("stale claim = %v, %v; want true", claimed, err)
	}
}
