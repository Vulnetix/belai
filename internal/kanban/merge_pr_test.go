package kanban

import (
	"testing"
	"time"
)

// A pulled pull request only fills: the one the forge coordinator recorded on
// the card survives a host that never pushed the branch itself, and a pull
// never replaces one this host recorded.
func TestKeepAgentPullRequestIsFillOnly(t *testing.T) {
	const pulled = "https://github.com/acme/app/pull/7"
	r := Item{ID: "x", PR: pulled}
	keepAgent(&r, Item{ID: "x"})
	if r.PR != pulled {
		t.Fatalf("an empty local PR must take the pulled one, got %q", r.PR)
	}
	r = Item{ID: "x", PR: pulled}
	keepAgent(&r, Item{ID: "x", PR: "https://github.com/acme/app/pull/3"})
	if r.PR != "https://github.com/acme/app/pull/3" {
		t.Fatalf("a local PR must stand, got %q", r.PR)
	}
	r = Item{ID: "x"}
	keepAgent(&r, Item{ID: "x", PR: "https://github.com/acme/app/pull/3"})
	if r.PR != "https://github.com/acme/app/pull/3" {
		t.Fatalf("an agent-less pull must not clear the local PR, got %q", r.PR)
	}
}

// A newer local change that has not been pushed keeps its own fields, and
// still takes the pull request the coordinator recorded.
func TestMergeLocalWinsStillFillsThePullRequest(t *testing.T) {
	s := testStore(t)
	clock := time.UnixMilli(9_000_000)
	s.now = func() time.Time { return clock }
	it := addItem(t, s, ItemInput{Title: "handed over"})
	local, _ := s.Get(it.ID)
	w := ToWire(local)
	w.UpdatedAt = local.Updated - 10 // older than the local, unpushed change
	w.Agent.PR = "https://github.com/acme/app/pull/7"
	if _, err := s.Merge([]Item{FromWire(w)}, 1); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(it.ID)
	if got.PR != "https://github.com/acme/app/pull/7" || got.Title != "handed over" {
		t.Fatalf("got %+v", got)
	}
}
