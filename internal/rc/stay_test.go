package rc

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// The wire names are fixed: the website sends "stay" and "stayIdleMax"
// (seconds), and a request that omits them keeps the old behaviour.
func TestDispatchStayWireNames(t *testing.T) {
	var r sessionsync.Dispatch
	if err := json.Unmarshal([]byte(`{"id":"d","kind":"worker","cwd":"/x","profile":"belai:builder","stay":true,"stayIdleMax":7200,"someFutureField":1}`), &r); err != nil {
		t.Fatal(err)
	}
	if !r.Stay || r.StayIdleMax != 7200 {
		t.Fatalf("decoded %+v", r)
	}
	var old sessionsync.Dispatch
	if err := json.Unmarshal([]byte(`{"id":"d","kind":"worker","cwd":"/x","profile":"belai:builder"}`), &old); err != nil || old.Stay || old.StayIdleMax != 0 {
		t.Fatalf("an old request decoded as %+v, %v", old, err)
	}
	out, _ := json.Marshal(sessionsync.Dispatch{ID: "d", Kind: "worker"})
	if s := string(out); strings.Contains(s, "stay") {
		t.Fatalf("an unset stay is sent: %s", s)
	}
}

// A worker or crew start carries stay to `belai agent start -stay`; without it
// nothing is passed, so the default stays false.
func TestWorkerStartCarriesStay(t *testing.T) {
	d, proj, _, workers := agentDaemon(t, nil)

	if _, why := d.startWorkers(sessionsync.Dispatch{Kind: "worker", Cwd: proj, Profile: "belai:builder"}); why != "" {
		t.Fatalf("refused: %s", why)
	}
	if plain := agentStartArgs((*workers)[0]); slices.Contains(plain, "-stay") || slices.Contains(plain, "-stay-idle-max") {
		t.Fatalf("a request without stay passed it: %v", plain)
	}

	if _, why := d.startWorkers(sessionsync.Dispatch{Kind: "worker", Cwd: proj, Profile: "belai:builder", Stay: true}); why != "" {
		t.Fatalf("refused: %s", why)
	}
	got := agentStartArgs((*workers)[1])
	if !slices.Contains(got, "-stay") || slices.Contains(got, "-stay-idle-max") || got[len(got)-1] != "belai:builder" {
		t.Fatalf("argv = %v", got)
	}

	if _, why := d.startWorkers(sessionsync.Dispatch{Kind: "crew", Cwd: proj, Crew: "crew1", Fill: true, Stay: true, StayIdleMax: 3600}); why != "" {
		t.Fatalf("refused: %s", why)
	}
	got = agentStartArgs((*workers)[2])
	i := slices.Index(got, "-stay-idle-max")
	if !slices.Contains(got, "-stay") || i < 0 || got[i+1] != "1h0m0s" || !slices.Contains(got, "-crew") || !slices.Contains(got, "-fill") {
		t.Fatalf("argv = %v", got)
	}
}

// The idle cap is bounded by the daemon: a huge or negative value never
// reaches the argv as more than the hard maximum, and a stored schedule (which
// drains by design) never stays.
func TestWorkerStartClampsTheIdleCapAndDrainWins(t *testing.T) {
	d, proj, _, workers := agentDaemon(t, nil)

	for _, secs := range []int{1 << 30, 10 * 24 * 3600} {
		if _, why := d.startWorkers(sessionsync.Dispatch{Kind: "worker", Cwd: proj, Profile: "belai:builder", Stay: true, StayIdleMax: secs}); why != "" {
			t.Fatalf("refused: %s", why)
		}
	}
	for _, w := range *workers {
		if w.StayIdleMax != fleet.MaxStayIdleMax {
			t.Errorf("idle cap %s, want the hard maximum %s", w.StayIdleMax, fleet.MaxStayIdleMax)
		}
		args := agentStartArgs(w)
		if i := slices.Index(args, "-stay-idle-max"); i < 0 || args[i+1] != "24h0m0s" {
			t.Errorf("argv = %v", args)
		}
	}

	*workers = nil
	if _, why := d.startWorkers(sessionsync.Dispatch{Kind: "worker", Cwd: proj, Profile: "belai:builder", Stay: true, StayIdleMax: -5}); why != "" {
		t.Fatalf("refused: %s", why)
	}
	if w := (*workers)[0]; w.StayIdleMax != 0 {
		t.Errorf("a negative cap was kept: %s", w.StayIdleMax)
	}

	*workers = nil
	if _, why := d.startWorkersDrain(sessionsync.Dispatch{Kind: "worker", Cwd: proj, Profile: "belai:builder", Stay: true}, true); why != "" {
		t.Fatalf("refused: %s", why)
	}
	args := agentStartArgs((*workers)[0])
	if slices.Contains(args, "-stay") || !slices.Contains(args, "-drain") {
		t.Fatalf("a drained start stays: %v", args)
	}
}
