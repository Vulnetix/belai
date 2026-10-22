package fleet

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/kanban"
)

const guardPR = "https://github.com/acme/web/pull/148"

// stubOpenPRs makes the forge answer with urls (or err) and counts the asks.
func stubOpenPRs(t *testing.T, urls []string, err error) *atomic.Int32 {
	t.Helper()
	var asks atomic.Int32
	old := listOpenCardPRs
	listOpenCardPRs = func(context.Context, string) ([]forge.PR, error) {
		asks.Add(1)
		var prs []forge.PR
		for _, u := range urls {
			prs = append(prs, forge.PR{URL: u, State: "open"})
		}
		return prs, err
	}
	openPRMu.Lock()
	openPRCache = map[string]openPRList{}
	openPRMu.Unlock()
	t.Cleanup(func() { listOpenCardPRs = old })
	return &asks
}

func publisher() agentprofile.AgentProfile {
	p := sweeper()
	p.Workspace = &agentprofile.WorkspaceSpec{Isolation: agentprofile.IsolationWorktree, Publish: agentprofile.PublishDraftPR}
	return p
}

func TestSweepFilesNoSecondCardWhileTheFirstPullRequestIsOpen(t *testing.T) {
	store, reg := testEnv(t)
	asks := stubOpenPRs(t, []string{guardPR}, nil)
	w := newWorker(t, store, reg, publisher(), complete)
	now := time.Now()
	w.now = func() time.Time { return now }

	writeReview(t, w.Repo, secHeadOld, "GHSA-aaaa-bbbb-cccc")
	w.syncCards(secHeadOld, true)
	cards := securityCards(t, store)
	if len(cards) != 1 {
		t.Fatalf("%d cards after the first sweep", len(cards))
	}
	// The patcher finished and the verifier released it to done; the pull
	// request waits for review on the forge.
	if _, err := store.SetPR(cards[0].ID, guardPR, "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Move(cards[0].ID, kanban.Done, "fixed", "s"); err != nil {
		t.Fatal(err)
	}

	writeReview(t, w.Repo, secHeadNew, "GHSA-aaaa-bbbb-cccc")
	w.syncCards(secHeadNew, true)
	if n := len(securityCards(t, store)); n != 1 {
		t.Fatalf("%d cards while the pull request is open, want 1", n)
	}

	// The pull request is closed unmerged and the finding is still reported.
	stubOpenPRs(t, nil, nil)
	now = now.Add(openPRTTL + time.Second)
	w.syncCards(secHeadNew, true)
	if n := len(securityCards(t, store)); n != 2 {
		t.Fatalf("%d cards after the pull request closed, want a new one", n)
	}
	if asks.Load() != 1 {
		t.Fatalf("the forge was asked %d times for two sweeps inside the cache window", asks.Load())
	}
}

func TestOpenPRFuncCachesAndFailsOpen(t *testing.T) {
	asks := stubOpenPRs(t, []string{guardPR}, nil)
	now := time.Now()
	for range 3 {
		open, err := openPRFunc(context.Background(), "/repo", now)
		if err != nil || !open(guardPR) || open("https://github.com/acme/web/pull/1") {
			t.Fatalf("open = %v", err)
		}
	}
	if asks.Load() != 1 {
		t.Fatalf("asked %d times inside the window", asks.Load())
	}
	if _, err := openPRFunc(context.Background(), "/repo", now.Add(openPRTTL)); err != nil || asks.Load() != 2 {
		t.Fatalf("not asked again after the window: %v %d", err, asks.Load())
	}

	asks = stubOpenPRs(t, nil, errors.New("gh: not logged in"))
	for range 2 {
		if open, err := openPRFunc(context.Background(), "/repo", now); err == nil || open != nil {
			t.Fatal("a failed listing was trusted")
		}
	}
	if asks.Load() != 1 {
		t.Fatalf("a failed listing was asked %d times inside the window", asks.Load())
	}
}

func TestAWorkerThatDoesNotPublishAsksNothing(t *testing.T) {
	store, reg := testEnv(t)
	asks := stubOpenPRs(t, []string{guardPR}, nil)
	w := newWorker(t, store, reg, sweeper(), complete)
	if open := w.guardOpenPR(context.Background()); open != nil || asks.Load() != 0 {
		t.Fatalf("a non-publishing worker consulted the forge (%d asks)", asks.Load())
	}
}
