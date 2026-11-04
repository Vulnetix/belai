package rc

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// The reserved worker count is part of the catalogue hash, so a change is
// advertised again; it is hashed as clamped under the cap, so a value the cap
// already bounds changes nothing.
func TestInventoryHashFollowsReservedWorkers(t *testing.T) {
	none := Inventory{MaxWorkers: 4}
	some := Inventory{MaxWorkers: 4, ReservedWorkers: 1}
	if none.catalogueHash() == some.catalogueHash() {
		t.Fatal("the catalogue hash ignores the reserved workers")
	}
	if a, b := (Inventory{MaxWorkers: 4, ReservedWorkers: 3}), (Inventory{MaxWorkers: 4, ReservedWorkers: 9}); a.catalogueHash() != b.catalogueHash() {
		t.Fatal("two values clamped to the same count hash differently")
	}
}

// LocalInventory reads agents.factory_reserved_workers from the user's
// settings.
func TestLocalInventoryReadsReservedWorkers(t *testing.T) {
	home(t)
	if n := LocalInventory().ReservedWorkers; n != 0 {
		t.Fatalf("unset = %d", n)
	}
	n := 2
	if err := config.Mutate(config.ScopeGlobal, "", func(s *config.Settings) error {
		s.Agents = &config.AgentsSettings{FactoryReservedWorkers: &n}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := LocalInventory().ReservedWorkers; got != 2 {
		t.Fatalf("got %d, want 2", got)
	}
}

// belai rc --factory-reserved-workers reaches `belai agent start` as a flag,
// even as 0; without it the flag is left out and the worker's own setting
// applies.
func TestAgentStartArgsCarryReservedWorkers(t *testing.T) {
	if args := agentStartArgs(WorkerStart{Profile: "belai:builder"}); slices.Contains(args, "-factory-reserved-workers") {
		t.Fatalf("an unset reservation passed: %v", args)
	}
	for _, n := range []int{0, 4} {
		args := agentStartArgs(WorkerStart{Profile: "belai:builder", ReservedWorkers: &n})
		i := slices.Index(args, "-factory-reserved-workers")
		if i < 0 || args[i+1] != map[int]string{0: "0", 4: "4"}[n] || args[len(args)-1] != "belai:builder" {
			t.Fatalf("argv %v", args)
		}
	}
	d, proj, _, workers := agentDaemon(t, nil)
	n := 3
	d.o.ReservedWorkers = &n
	if _, why := d.startWorkers(sessionsync.Dispatch{Kind: "worker", Cwd: proj, Profile: "belai:builder"}); why != "" {
		t.Fatalf("refused: %s", why)
	}
	if got := (*workers)[0].ReservedWorkers; got == nil || *got != 3 {
		t.Fatalf("start reservation %v, want 3", got)
	}
}

// The daemon advertises the reservation clamped under the cap it reports, the
// --factory-reserved-workers override in place of the setting.
func TestDaemonAdvertisesReservedWorkers(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	site := &fakeSite{acks: map[string][3]string{}, delivered: make(chan struct{}, 8)}
	srv := httptest.NewServer(site)
	defer srv.Close()
	client, err := sessionsync.NewClient(srv.URL, func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	n := 9
	d, err := New(Options{
		Client: client, HostID: testHost, HeartbeatEvery: 10 * time.Millisecond, PollWait: time.Second, Exe: "/bin/belai",
		MaxWorkers: 6, ReservedWorkers: &n,
		Inventory: func() Inventory { return Inventory{MaxWorkers: 4, ReservedWorkers: 1} },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		site.mu.Lock()
		rc := site.host.RC
		site.mu.Unlock()
		if rc != nil {
			if rc.MaxWorkers != 6 || rc.ReservedWorkers != 5 {
				t.Errorf("advertised max %d reserved %d, want 6 and 5", rc.MaxWorkers, rc.ReservedWorkers)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the host never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}
