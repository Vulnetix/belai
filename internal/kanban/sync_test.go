package kanban

import (
	"context"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/sessionsync"
)

// ackRemote acknowledges every push the way the backend does, echoing the
// pushed updatedAt with a fresh version, and records what it was sent.
type ackRemote struct {
	version int64
	pushed  []sessionsync.KanbanItem
}

func (r *ackRemote) KanbanBatch(_ context.Context, items []sessionsync.KanbanItem) ([]sessionsync.KanbanAck, error) {
	acks := make([]sessionsync.KanbanAck, 0, len(items))
	for _, it := range items {
		r.version++
		r.pushed = append(r.pushed, it)
		acks = append(acks, sessionsync.KanbanAck{ID: it.ID, UpdatedAt: it.UpdatedAt, Version: r.version, Applied: true})
	}
	return acks, nil
}

func (r *ackRemote) KanbanList(_ context.Context, since int64, _ int) ([]sessionsync.KanbanItem, int64, bool, error) {
	return nil, since, false, nil
}

// A renewal that crosses half a lease is pushed with the later leaseUntil and
// the claim's own updatedAt: a lease-only push, not a newer edit. Updated stays
// as the claim left it, before and after the push.
func TestLeaseOnlyPushDoesNotBumpUpdated(t *testing.T) {
	s := testStore(t)
	clock := time.UnixMilli(50_000_000)
	s.now = func() time.Time { return clock }
	addItem(t, s, ItemInput{Title: "long build"})
	r := claimReq("w1")
	claimed, err := s.Claim(r)
	if err != nil {
		t.Fatal(err)
	}
	remote := &ackRemote{}
	sy := NewSyncer(s, remote, SyncOptions{})
	if err := sy.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(claimed.ID); got.Dirty || got.LeaseSynced != claimed.LeaseUntil {
		t.Fatalf("the claim push did not record the synced lease: %+v", got)
	}
	for range 2 {
		clock = clock.Add(r.Lease / 3)
		if err := s.Renew(claimed.ID, "w1", r.Lease); err != nil {
			t.Fatal(err)
		}
	}
	if err := sy.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(remote.pushed) != 2 {
		t.Fatalf("%d pushes, want the claim and one renewal", len(remote.pushed))
	}
	lease := remote.pushed[1]
	if lease.UpdatedAt != claimed.Updated || lease.Agent == nil || lease.Agent.ClaimedBy != "w1" {
		t.Fatalf("the renewal push is not lease-only: updatedAt %d (claim %d), agent %+v", lease.UpdatedAt, claimed.Updated, lease.Agent)
	}
	want := clock.UnixMilli() + r.Lease.Milliseconds()
	if lease.Agent.LeaseUntil != want {
		t.Fatalf("pushed lease %d, want %d", lease.Agent.LeaseUntil, want)
	}
	got, _ := s.Get(claimed.ID)
	if got.Updated != claimed.Updated || got.Dirty || got.LeaseSynced != want || got.ServerVersion != 2 {
		t.Fatalf("after the lease push: %+v", got)
	}
	// Nothing more to push until the next half lease.
	if err := sy.Flush(context.Background()); err != nil || len(remote.pushed) != 2 {
		t.Fatalf("pushes %d err %v", len(remote.pushed), err)
	}
}
