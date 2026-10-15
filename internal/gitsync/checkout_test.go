package gitsync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckoutSwitchesToALocalBranch(t *testing.T) {
	work, _ := repos(t)
	g(t, work, "branch", "feature")
	h := newHygiene(work)
	line, err := h.Checkout(context.Background(), "feature")
	if err != nil || line != "checked out feature" {
		t.Fatalf("checkout = %q, %v", line, err)
	}
	if got := g(t, work, "branch", "--show-current"); got != "feature" {
		t.Fatalf("on %q", got)
	}
	if line, err := h.Checkout(context.Background(), "feature"); err != nil || !strings.HasPrefix(line, "already on") {
		t.Fatalf("second checkout = %q, %v", line, err)
	}
}

// A branch only origin has is fetched and created tracking origin's.
func TestCheckoutFetchesABranchOnlyOriginHas(t *testing.T) {
	work, other := repos(t)
	g(t, other, "checkout", "-b", "remote-only")
	commit(t, other, "r.txt", "r\n", "remote work")
	g(t, other, "push", "-u", "origin", "remote-only")

	line, err := newHygiene(work).Checkout(context.Background(), "remote-only")
	if err != nil || !strings.Contains(line, "fetched from origin") {
		t.Fatalf("checkout = %q, %v", line, err)
	}
	if up := g(t, work, "rev-parse", "--abbrev-ref", "@{u}"); up != "origin/remote-only" {
		t.Fatalf("upstream = %q", up)
	}
	if _, err := os.Stat(filepath.Join(work, "r.txt")); err != nil {
		t.Fatal("the fetched branch's file is missing")
	}
}

func TestCheckoutRefusals(t *testing.T) {
	work, _ := repos(t)
	g(t, work, "branch", "feature")
	h := newHygiene(work)
	ctx := context.Background()
	for _, bad := range []string{"", "-x", "a..b", "has space", "x.lock", "feature;rm"} {
		if _, err := h.Checkout(ctx, bad); err == nil {
			t.Errorf("branch %q accepted", bad)
		}
	}
	if _, err := h.Checkout(ctx, "nowhere"); err == nil || !strings.Contains(err.Error(), "could not be fetched") {
		t.Fatalf("missing branch: %v", err)
	}

	// Uncommitted tracked changes are never carried or discarded.
	write(t, work, "a.txt", "changed\n")
	if err := h.CanCheckout(ctx, "feature"); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("dirty tree: %v", err)
	}
	if _, err := h.Checkout(ctx, "feature"); err == nil {
		t.Fatal("checked out over uncommitted changes")
	}
	g(t, work, "checkout", "--", "a.txt")

	// An operation underway is left alone.
	gitDir := g(t, work, "rev-parse", "--absolute-git-dir")
	write(t, gitDir, "MERGE_HEAD", g(t, work, "rev-parse", "HEAD")+"\n")
	if _, err := h.Checkout(ctx, "feature"); err == nil || !strings.Contains(err.Error(), "merge") {
		t.Fatalf("merge in progress: %v", err)
	}
	if err := os.Remove(filepath.Join(gitDir, "MERGE_HEAD")); err != nil {
		t.Fatal(err)
	}
	if got := g(t, work, "branch", "--show-current"); got != "main" {
		t.Fatalf("a refusal moved the checkout to %q", got)
	}
}

// Git's own refusal is the reason: a branch checked out in another worktree.
func TestCheckoutSurfacesGitsRefusal(t *testing.T) {
	work, _ := repos(t)
	g(t, work, "branch", "feature")
	wt := filepath.Join(t.TempDir(), "wt")
	g(t, work, "worktree", "add", wt, "feature")
	_, err := newHygiene(work).Checkout(context.Background(), "feature")
	if err == nil || !strings.Contains(err.Error(), "git refused") {
		t.Fatalf("checkout = %v", err)
	}
}

// A detached HEAD may be switched away from.
func TestCheckoutFromDetachedHead(t *testing.T) {
	work, _ := repos(t)
	g(t, work, "checkout", "--detach")
	if _, err := newHygiene(work).Checkout(context.Background(), "main"); err != nil {
		t.Fatal(err)
	}
	if got := g(t, work, "branch", "--show-current"); got != "main" {
		t.Fatalf("on %q", got)
	}
}
