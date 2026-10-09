package kanban

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/sessionsync"
)

func claimReq(worker string) ClaimRequest {
	return ClaimRequest{Worker: worker, Profile: "builder", Host: "host-1", Lease: 5 * time.Minute}
}

func addItem(t *testing.T, s *Store, in ItemInput) Item {
	t.Helper()
	it, dup, err := s.Add(in, prov)
	if err != nil || dup {
		t.Fatalf("add %q: %v dup=%v", in.Title, err, dup)
	}
	return it
}

func TestNormLabelsAndPriority(t *testing.T) {
	got := NormLabels([]string{" Build ", "needs review", "build", "", "a/b", strings.Repeat("x", 40)})
	want := []string{"a-b", "build", "needs-review", strings.Repeat("x", MaxLabelRunes)}
	if !slices.Equal(got, want) {
		t.Fatalf("labels %q, want %q", got, want)
	}
	if ClampPriority(9) != MaxPriority || ClampPriority(-9) != MinPriority || ClampPriority(1) != 1 {
		t.Fatal("clamp")
	}
	if _, err := CleanAssignee("bad name"); !errors.Is(err, ErrBadAssignee) {
		t.Fatalf("assignee: %v", err)
	}
	if a, err := CleanAssignee(" belai:builder "); err != nil || a != "belai:builder" {
		t.Fatalf("assignee %q %v", a, err)
	}
}

func TestClaimPicksPriorityThenAge(t *testing.T) {
	s := testStore(t)
	clock := time.UnixMilli(1_000_000)
	s.now = func() time.Time { clock = clock.Add(time.Millisecond); return clock }
	old := addItem(t, s, ItemInput{Title: "old", Labels: []string{"build"}})
	addItem(t, s, ItemInput{Title: "newer", Labels: []string{"build"}})
	hot := addItem(t, s, ItemInput{Title: "urgent", Labels: []string{"build"}, Priority: 2})
	addItem(t, s, ItemInput{Title: "other label", Labels: []string{"docs"}, Priority: 3})

	r := claimReq("w1")
	r.Labels = []string{"build"}
	got, err := s.Claim(r)
	if err != nil || got.ID != hot.ID {
		t.Fatalf("first claim %v %v, want urgent", got.Title, err)
	}
	if got.List != InProgress || got.ClaimedBy != "w1" || got.ClaimFrom != Backlog || got.LeaseUntil <= 0 {
		t.Fatalf("claimed item %+v", got)
	}
	got, err = s.Claim(r)
	if err != nil || got.ID != old.ID {
		t.Fatalf("second claim %v %v, want old", got.Title, err)
	}
}

func TestClaimRespectsAssigneeDependsAndSkip(t *testing.T) {
	s := testStore(t)
	dep := addItem(t, s, ItemInput{Title: "first"})
	assigned := addItem(t, s, ItemInput{Title: "for reviewer", Assignee: "reviewer"})
	after := addItem(t, s, ItemInput{Title: "second", DependsOn: []string{dep.Short()}})

	r := claimReq("w1")
	r.Skip = []string{dep.ID}
	if _, err := s.Claim(r); !errors.Is(err, ErrNoWork) {
		t.Fatalf("assigned/dependent/skipped item claimed: %v", err)
	}
	r.Profile = "reviewer"
	got, err := s.Claim(r)
	if err != nil || got.ID != assigned.ID {
		t.Fatalf("reviewer claim %v %v", got.Title, err)
	}
	if _, err := s.MoveAs(dep.Short(), Done, "done", "s", "", nil); err != nil {
		t.Fatal(err)
	}
	r = claimReq("w2")
	got, err = s.Claim(r)
	if err != nil || got.ID != after.ID {
		t.Fatalf("dependent item not freed: %v %v", got.Title, err)
	}
	if _, _, err := s.Add(ItemInput{Title: "bad dep", DependsOn: []string{"K-ffffff"}}, prov); err == nil {
		t.Fatal("unknown dependency accepted")
	}
}

func TestClaimRequestValidation(t *testing.T) {
	s := testStore(t)
	for name, r := range map[string]ClaimRequest{
		"no worker":   {Lease: time.Minute},
		"short lease": {Worker: "w", Lease: time.Second},
		"long lease":  {Worker: "w", Lease: 3 * time.Hour},
		"bad list":    {Worker: "w", Lease: time.Minute, Lists: []List{InProgress}},
	} {
		if _, err := s.Claim(r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLeaseRenewReleaseAndReap(t *testing.T) {
	s := testStore(t)
	clock := time.UnixMilli(10_000_000)
	s.now = func() time.Time { return clock }
	it := addItem(t, s, ItemInput{Title: "work", List: Review})
	got, err := s.Claim(ClaimRequest{Worker: "w1", Profile: "p", Lease: time.Minute, Lists: []List{Review}})
	if err != nil || got.ID != it.ID {
		t.Fatal(err)
	}
	updated := got.Updated
	clock = clock.Add(50 * time.Second)
	if err := s.Renew(it.ID, "w1", time.Minute); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Get(it.ID)
	if after.Updated != updated || len(after.History) != len(got.History) {
		t.Fatal("a renewal must not touch Updated or history")
	}
	if err := s.Renew(it.ID, "w2", time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("foreign renew: %v", err)
	}
	// The lease lapses; the next claim reaps it back to review.
	clock = clock.Add(2 * time.Minute)
	if _, err := s.Claim(ClaimRequest{Worker: "w3", Profile: "p", Lease: time.Minute, Lists: []List{Backlog}}); !errors.Is(err, ErrNoWork) {
		t.Fatalf("claim: %v", err)
	}
	reaped, _ := s.Get(it.ID)
	if reaped.List != Review || reaped.ClaimedBy != "" || reaped.Attempts != 1 {
		t.Fatalf("reaped item %+v", reaped)
	}
	if err := s.Renew(it.ID, "w1", time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("renew after reap: %v", err)
	}
	// Claim again and release with an outcome.
	if _, err := s.ClaimID(it.Short(), ClaimRequest{Worker: "w4", Profile: "p", Lease: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Release(it.ID, "w1", Outcome{To: Done}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("foreign release: %v", err)
	}
	out, err := s.Release(it.ID, "w4", Outcome{To: Review, Note: "done", AddLabels: []string{"needs-review"}, Branch: "belai/K-1"})
	if err != nil {
		t.Fatal(err)
	}
	if out.List != Review || out.ClaimedBy != "" || out.Branch != "belai/K-1" || !slices.Contains(out.Labels, "needs-review") || out.Attempts != 1 {
		t.Fatalf("released %+v", out)
	}
}

func TestMoveAsAndUpdateAsRespectClaims(t *testing.T) {
	s := testStore(t)
	it := addItem(t, s, ItemInput{Title: "held"})
	if _, err := s.Claim(claimReq("w1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveAs(it.ID, Done, "", "s", "", nil); !errors.Is(err, ErrClaimed) {
		t.Fatalf("move of claimed item: %v", err)
	}
	if _, err := s.MoveAs(it.ID, Done, "", "s", "w1", nil); !errors.Is(err, ErrClaimed) {
		t.Fatalf("holder move must go through Release: %v", err)
	}
	title := "rewritten"
	if _, err := s.UpdateAs(it.ID, Patch{Title: &title}, "s", "w2"); !errors.Is(err, ErrClaimed) {
		t.Fatalf("foreign update: %v", err)
	}
	if _, err := s.UpdateAs(it.ID, Patch{Title: &title}, "s", "w1"); !errors.Is(err, ErrNotesOnly) {
		t.Fatalf("holder rewrite: %v", err)
	}
	if _, err := s.UpdateAs(it.ID, Patch{Note: "progress"}, "s", "w1"); err != nil {
		t.Fatalf("holder note: %v", err)
	}
	// Compare-and-set on the source list.
	free := addItem(t, s, ItemInput{Title: "free"})
	if _, err := s.MoveAs(free.ID, Done, "", "s", "", []List{Review}); err == nil {
		t.Fatal("CAS ignored")
	}
	// A human override clears the claim.
	if _, err := s.Unclaim(it.ID, "released by hand", "s", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(it.ID); got.ClaimedBy != "" || got.List != Backlog {
		t.Fatalf("unclaimed %+v", got)
	}
}

func TestUnclaimWorker(t *testing.T) {
	s := testStore(t)
	addItem(t, s, ItemInput{Title: "a"})
	addItem(t, s, ItemInput{Title: "b"})
	s.Claim(claimReq("dead"))
	s.Claim(claimReq("dead"))
	ids, err := s.UnclaimWorker("dead", "worker died")
	if err != nil || len(ids) != 2 {
		t.Fatalf("released %v %v", ids, err)
	}
	c, _ := s.Counts("")
	if c[Backlog] != 2 {
		t.Fatalf("counts %v", c)
	}
}

func TestConcurrentClaimsNeverDoubleClaim(t *testing.T) {
	s := testStore(t)
	for i := range 30 {
		addItem(t, s, ItemInput{Title: fmt.Sprintf("item %d", i)})
	}
	var mu sync.Mutex
	seen := map[string]string{}
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			st := Open(s.Path()) // a separate store, as another process would have
			worker := fmt.Sprintf("w%d", w)
			for {
				it, err := st.Claim(claimReq(worker))
				if errors.Is(err, ErrNoWork) {
					return
				}
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				if prev, ok := seen[it.ID]; ok {
					t.Errorf("%s claimed by %s and %s", it.Short(), prev, worker)
				}
				seen[it.ID] = worker
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(seen) != 30 {
		t.Fatalf("claimed %d of 30", len(seen))
	}
}

// TestClaimAcrossProcesses runs two real processes against one board.
func TestClaimAcrossProcesses(t *testing.T) {
	if os.Getenv("BELAI_KANBAN_CLAIM_HELPER") != "" {
		t.Skip("helper")
	}
	s := testStore(t)
	for i := range 20 {
		addItem(t, s, ItemInput{Title: fmt.Sprintf("item %d", i)})
	}
	var outs [2]bytes.Buffer
	var cmds [2]*exec.Cmd
	for i := range cmds {
		cmd := exec.Command(os.Args[0], "-test.run=^TestClaimHelper$")
		cmd.Env = append(os.Environ(), "BELAI_KANBAN_CLAIM_HELPER="+s.Path(), fmt.Sprintf("BELAI_KANBAN_WORKER=p%d", i))
		cmd.Stdout = &outs[i]
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds[i] = cmd
	}
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for _, o := range outs {
		sc := bufio.NewScanner(&o)
		for sc.Scan() {
			id, ok := strings.CutPrefix(sc.Text(), "CLAIMED ")
			if !ok {
				continue
			}
			if seen[id] {
				t.Fatalf("%s claimed twice", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != 20 {
		t.Fatalf("claimed %d of 20", len(seen))
	}
}

func TestClaimHelper(t *testing.T) {
	path := os.Getenv("BELAI_KANBAN_CLAIM_HELPER")
	if path == "" {
		t.Skip("run by TestClaimAcrossProcesses")
	}
	s := Open(path)
	for {
		it, err := s.Claim(claimReq(os.Getenv("BELAI_KANBAN_WORKER")))
		if errors.Is(err, ErrNoWork) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println("CLAIMED " + it.ID)
	}
}

func TestDecodeVersionOneBoard(t *testing.T) {
	type v1Item struct {
		ID, Title string
		List      List
		Created   int64
		Updated   int64
	}
	type v1Board struct {
		Cursor int64
		Items  []v1Item
	}
	var payload bytes.Buffer
	if err := gob.NewEncoder(&payload).Encode(v1Board{Cursor: 3, Items: []v1Item{{ID: "3f9a2c00-0000-4000-8000-000000000000", Title: "old", List: Review, Created: 1, Updated: 1}}}); err != nil {
		t.Fatal(err)
	}
	data := append([]byte(magic), 0, 1)
	data = append(data, payload.Bytes()...)
	sum := sha256.Sum256(payload.Bytes())
	data = append(data, sum[:]...)
	b, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Items) != 1 || b.Items[0].Title != "old" || b.Items[0].ClaimedBy != "" || b.Items[0].Labels != nil {
		t.Fatalf("decoded %+v", b)
	}
	enc, err := Encode(b)
	if err != nil {
		t.Fatal(err)
	}
	if v := binary.BigEndian.Uint16(enc[len(magic):headerLen]); v != formatVersion {
		t.Fatalf("wrote version %d", v)
	}
}

func TestMergeKeepsAgentFieldsTheBackendDoesNotCarry(t *testing.T) {
	s := testStore(t)
	clock := time.UnixMilli(5_000_000)
	s.now = func() time.Time { return clock }
	it := addItem(t, s, ItemInput{Title: "routed", Labels: []string{"build"}, Priority: 2})
	s.Claim(claimReq("w1"))
	local, _ := s.Get(it.ID)

	// An old backend: no agent block, a newer web edit of the title.
	w := ToWire(local)
	w.Agent = nil
	w.Title = "renamed on the web"
	w.UpdatedAt = local.Updated + 10
	if _, err := s.Merge([]Item{FromWire(w)}, 1); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(it.ID)
	if got.Title != "renamed on the web" || got.ClaimedBy != "w1" || got.Priority != 2 || !slices.Contains(got.Labels, "build") {
		t.Fatalf("agent fields lost: %+v", got)
	}

	// A new backend echoes an older lease: the later local lease stands.
	clock = clock.Add(time.Minute)
	if err := s.Renew(it.ID, "w1", 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	renewed, _ := s.Get(it.ID)
	w = ToWire(got)
	w.UpdatedAt = renewed.Updated + 20
	w.Title = "renamed again"
	s.Merge([]Item{FromWire(w)}, 2)
	got, _ = s.Get(it.ID)
	if got.LeaseUntil != renewed.LeaseUntil {
		t.Fatalf("lease regressed %d < %d", got.LeaseUntil, renewed.LeaseUntil)
	}

	// The web releases the claim: a newer write with an empty claimedBy.
	w = ToWire(got)
	w.UpdatedAt = got.Updated + 30
	w.Agent.ClaimedBy = ""
	s.Merge([]Item{FromWire(w)}, 3)
	got, _ = s.Get(it.ID)
	if got.ClaimedBy != "" || got.LeaseUntil != 0 {
		t.Fatalf("web release ignored: %+v", got)
	}
	if err := s.Renew(it.ID, "w1", 5*time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("worker must lose the lease: %v", err)
	}
}

func TestWireCarriesAgentFields(t *testing.T) {
	it := Item{ID: "x", Title: "t", List: Backlog, Labels: []string{"a"}, Priority: 1, Assignee: "b", ClaimedBy: "w", ClaimFrom: Review, LeaseUntil: 9, Branch: "belai/K-1"}
	w := ToWire(it)
	if w.Agent == nil || w.Agent.Assignee != "b" || w.Agent.ClaimFrom != "review" {
		t.Fatalf("wire %+v", w.Agent)
	}
	back := FromWire(w)
	if !back.remoteAgent || back.Branch != "belai/K-1" || back.LeaseUntil != 9 {
		t.Fatalf("back %+v", back)
	}
	if FromWire(sessionsync.KanbanItem{ID: "y"}).remoteAgent {
		t.Fatal("absent agent block marked present")
	}
}

// A pull fetched before another process pushed this host's claim must not
// revert the claim when it lands after the push was marked clean. This is
// the sequence that let two fleet workers claim one item against the live
// backend.
func TestMergeIgnoresAPullOlderThanAPushedClaim(t *testing.T) {
	s := testStore(t)
	clock := time.UnixMilli(7_000_000)
	s.now = func() time.Time { return clock }
	it := addItem(t, s, ItemInput{Title: "contended", Labels: []string{"build"}})
	s.MarkPushed([]Pushed{{ID: it.ID, Updated: it.Updated, Version: 1}})
	stale := ToWire(func() Item { g, _ := s.Get(it.ID); return g }()) // v1, backlog, unclaimed
	stale.Version = 1

	clock = clock.Add(400 * time.Millisecond)
	claimed, err := s.Claim(claimReq("w1"))
	if err != nil {
		t.Fatal(err)
	}
	// Another process pushes the claim and marks it clean.
	s.MarkPushed([]Pushed{{ID: it.ID, Updated: claimed.Updated, Version: 2}})
	// The stale pull lands.
	if _, err := s.Merge([]Item{FromWire(stale)}, 1); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(it.ID)
	if got.List != InProgress || got.ClaimedBy != "w1" {
		t.Fatalf("stale pull reverted the claim: %+v", got)
	}
	if _, err := s.Claim(claimReq("w2")); !errors.Is(err, ErrNoWork) {
		t.Fatalf("a second worker claimed it: %v", err)
	}
}

func TestClaimRespectsPinHost(t *testing.T) {
	s := testStore(t)
	it := addItem(t, s, ItemInput{Title: "pinned"})
	pin := "host-2"
	if _, err := s.Route(it.Short(), RoutePatch{PinHost: &pin}, "s"); err != nil {
		t.Fatal(err)
	}
	r := claimReq("w1") // host-1
	if _, err := s.Claim(r); !errors.Is(err, ErrNoWork) {
		t.Fatalf("item pinned to host-2 claimed on host-1: %v", err)
	}
	if _, err := s.ClaimID(it.Short(), r); !errors.Is(err, ErrNotClaimable) {
		t.Fatalf("ClaimID ignored the pin: %v", err)
	}
	r.Host = "host-2"
	got, err := s.Claim(r)
	if err != nil || got.ID != it.ID || got.PinHost != "host-2" {
		t.Fatalf("pinned host claim %+v %v", got, err)
	}
	if n := got.History[len(got.History)-2].Note; n != "pinned to host host-2" {
		t.Fatalf("route note %q", n)
	}
}

func TestWireCarriesPinHost(t *testing.T) {
	back := FromWire(ToWire(Item{ID: "x", Title: "t", List: Backlog, PinHost: "host-9"}))
	if back.PinHost != "host-9" {
		t.Fatalf("pin lost on the wire: %+v", back)
	}
	// A backend that does not carry the agent block keeps the local pin.
	r := Item{ID: "x"}
	keepAgent(&r, Item{PinHost: "host-9"})
	if r.PinHost != "host-9" {
		t.Fatal("local pin dropped by an agent-less pull")
	}
}

func TestParseAssigneeKinds(t *testing.T) {
	for in, want := range map[string][2]string{
		"":               {"", ""},
		"builder":        {"profile", "builder"},
		"belai:builder":  {"profile", "belai:builder"},
		"worker:builder": {"profile", "builder"},
		"crew:delivery":  {"crew", "delivery"},
		"person:m-1f2a":  {"person", "m-1f2a"},
	} {
		k, n := ParseAssignee(in)
		if string(k) != want[0] || n != want[1] {
			t.Errorf("ParseAssignee(%q) = %q %q, want %v", in, k, n, want)
		}
	}
}

func TestClaimMatchesTypedAssignees(t *testing.T) {
	s := testStore(t)
	addItem(t, s, ItemInput{Title: "for a person", Assignee: "person:m-1f2a"})
	crew := addItem(t, s, ItemInput{Title: "for a crew", Assignee: "crew:delivery"})
	explicit := addItem(t, s, ItemInput{Title: "explicit worker", Assignee: "worker:reviewer"})

	r := claimReq("w1")
	r.Profile = "reviewer"
	got, err := s.Claim(r)
	if err != nil || got.ID != explicit.ID {
		t.Fatalf("worker: prefix not matched: %v %v", got.Title, err)
	}
	// A lone worker never takes a crew's item, and nobody takes a person's.
	r = claimReq("w2")
	if _, err := s.Claim(r); !errors.Is(err, ErrNoWork) {
		t.Fatalf("lone worker took an item: %v", err)
	}
	r.Crew = "docs"
	if _, err := s.Claim(r); !errors.Is(err, ErrNoWork) {
		t.Fatalf("other crew took a crew item: %v", err)
	}
	r.Crew = "delivery"
	got, err = s.Claim(r)
	if err != nil || got.ID != crew.ID {
		t.Fatalf("crew member did not take the crew item: %v %v", got.Title, err)
	}
	r = claimReq("w3")
	r.Profile = "person:m-1f2a"
	r.Crew = "delivery"
	if _, err := s.Claim(r); !errors.Is(err, ErrNoWork) {
		t.Fatalf("a person item was claimed: %v", err)
	}
}

// A handoff assigned to the builder must reach review unassigned, or the
// reviewer, which claims only unassigned items or its own, never takes it.
func TestReleaseOnwardClearsTheReleasersOwnAssignment(t *testing.T) {
	s := testStore(t)
	it := addItem(t, s, ItemInput{Title: "fix the help text", Labels: []string{"build"}, Assignee: "belai:builder"})
	req := claimReq("w1")
	req.Profile = "belai:builder"
	if _, err := s.Claim(req); err != nil {
		t.Fatal(err)
	}
	got, err := s.Release(it.ID, "w1", Outcome{To: Review, AddLabels: []string{"needs-review"}, Releaser: "belai:builder"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Assignee != "" || got.List != Review {
		t.Fatalf("assignee %q on %s, want none on review", got.Assignee, got.List)
	}
}

// Going back where it came from (a failed attempt), the assignment stays.
func TestReleaseBackKeepsTheAssignment(t *testing.T) {
	s := testStore(t)
	it := addItem(t, s, ItemInput{Title: "fix the help text", Labels: []string{"build"}, Assignee: "belai:builder"})
	req := claimReq("w1")
	req.Profile = "belai:builder"
	if _, err := s.Claim(req); err != nil {
		t.Fatal(err)
	}
	got, err := s.Release(it.ID, "w1", Outcome{To: Backlog, Failed: true, Releaser: "belai:builder"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Assignee != "belai:builder" {
		t.Fatalf("assignee %q, want belai:builder kept", got.Assignee)
	}
}
