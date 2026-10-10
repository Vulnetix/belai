package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// probes is a pair of stand-ins for the bwrap and Landlock probes, counting
// how often each ran.
type probes struct {
	bwrap    func(path string) (string, error)
	landlock func(exe string) (int, error)
	bwraps   int
	lls      int
}

// landlockOK is a Landlock probe that finds ABI 4.
func landlockOK(string) (int, error) { return 4, nil }

// landlockNone is a Landlock probe for a kernel without it.
func landlockNone(string) (int, error) {
	return 0, errors.New("not in this kernel (needs Linux 5.13+)")
}

// withProbes runs Backend against a stand-in bwrap on PATH and the given
// probes, with a fresh probe state and no waiting between tries. The cooldown
// is an hour: a test that wants the next call to probe again sets it to zero.
func withProbes(t *testing.T, p *probes) (name, why string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the Linux probes are Linux only")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bwrap"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	oldRun, oldLL, oldTries, oldBackoff, oldRetry := runBwrapProbe, runLandlockProbe, probeTries, probeBackoff, probeRetryAfter
	t.Cleanup(func() {
		runBwrapProbe, runLandlockProbe, probeTries, probeBackoff, probeRetryAfter = oldRun, oldLL, oldTries, oldBackoff, oldRetry
		ResetProbeForTests()
	})
	ResetProbeForTests()
	probeTries, probeBackoff, probeRetryAfter = 4, time.Microsecond, time.Hour
	runBwrapProbe = func(path string) (string, error) {
		p.bwraps++
		return p.bwrap(path)
	}
	runLandlockProbe = func(exe string) (int, error) {
		p.lls++
		return p.landlock(exe)
	}
	name, _ = Backend()
	return name, BackendProblem()
}

func TestBackendRetriesAProbeThatFailedForWantOfProcesses(t *testing.T) {
	n := 0
	p := &probes{landlock: landlockNone, bwrap: func(string) (string, error) {
		n++
		if n < 3 {
			return "bwrap: Creating new namespace failed: Resource temporarily unavailable\n", errors.New("exit status 1")
		}
		return "", nil
	}}
	name, why := withProbes(t, p)

	if name != "bwrap" || why != "" || p.bwraps != 3 || p.lls != 0 {
		t.Fatalf("name %q why %q after %d bwrap and %d landlock calls", name, why, p.bwraps, p.lls)
	}
}

func TestBackendSaysWhyWhenEveryTryRanOutOfProcesses(t *testing.T) {
	p := &probes{landlock: landlockNone, bwrap: func(string) (string, error) {
		return "", errors.New("fork/exec /usr/bin/bwrap: resource temporarily unavailable")
	}}
	name, why := withProbes(t, p)

	if name != "" || p.bwraps != 4 || why == "" {
		t.Fatalf("name %q why %q after %d calls", name, why, p.bwraps)
	}
	if !transientProbeFailure(why) || !strings.Contains(why, "bwrap: ") || !strings.Contains(why, "landlock: not in this kernel") {
		t.Fatalf("the reason does not name both backends and the want of processes: %q", why)
	}
	// A transient result is probed again once the cooldown has passed, so a
	// long-lived process is not stuck with the one busy minute it probed in.
	Backend()
	if p.bwraps != 4 {
		t.Fatalf("probed again inside the cooldown: %d bwrap calls", p.bwraps)
	}
	probeRetryAfter = 0
	p.bwrap = func(string) (string, error) { return "", nil }
	if name, _ := Backend(); name != "bwrap" || p.bwraps != 5 {
		t.Fatalf("after the machine quietened: name %q, %d bwrap calls", name, p.bwraps)
	}
}

func TestBackendDoesNotRetryABubblewrapThatCannotWork(t *testing.T) {
	p := &probes{landlock: landlockNone, bwrap: func(string) (string, error) {
		return "bwrap: No permissions to create new namespace\n", errors.New("exit status 1")
	}}
	name, why := withProbes(t, p)

	if name != "" || p.bwraps != 1 || why == "" {
		t.Fatalf("name %q why %q after %d calls", name, why, p.bwraps)
	}
	// A hard result stands for the life of the process.
	Backend()
	Backend()
	if p.bwraps != 1 || p.lls != 1 {
		t.Fatalf("a hard failure was probed again: %d bwrap, %d landlock calls", p.bwraps, p.lls)
	}
}

func TestLandlockStandsInWhenBubblewrapCannotWork(t *testing.T) {
	p := &probes{landlock: landlockOK, bwrap: func(string) (string, error) {
		return "bwrap: No permissions to create new namespace\n", errors.New("exit status 1")
	}}
	name, why := withProbes(t, p)

	if name != "landlock" || p.bwraps != 1 || p.lls != 1 {
		t.Fatalf("name %q why %q after %d bwrap and %d landlock calls", name, why, p.bwraps, p.lls)
	}
	if why != "bwrap: bwrap: No permissions to create new namespace exit status 1" {
		t.Fatalf("why = %q", why)
	}
	if d := Describe(); d != "landlock (ABI 4, network deny supported)" {
		t.Fatalf("Describe() = %q", d)
	}
	if !NetworkDenyEnforced() {
		t.Fatal("ABI 4 enforces network deny")
	}
	Backend()
	if p.bwraps != 1 {
		t.Fatal("a hard bwrap failure with Landlock in its place was probed again")
	}
}

func TestLandlockStandsInForABusyMachineUntilBubblewrapIsBack(t *testing.T) {
	p := &probes{landlock: landlockOK, bwrap: func(string) (string, error) {
		return "", errors.New("fork/exec /usr/bin/bwrap: resource temporarily unavailable")
	}}
	name, _ := withProbes(t, p)

	if name != "landlock" || p.bwraps != 4 {
		t.Fatalf("name %q after %d bwrap calls", name, p.bwraps)
	}
	probeRetryAfter = 0
	p.bwrap = func(string) (string, error) { return "", nil }
	if name, _ := Backend(); name != "bwrap" || p.bwraps != 5 {
		t.Fatalf("after the machine quietened: name %q, %d bwrap calls", name, p.bwraps)
	}
}

func TestLandlockIsNotProbedWhenBubblewrapWorks(t *testing.T) {
	p := &probes{landlock: landlockOK, bwrap: func(string) (string, error) { return "", nil }}
	name, _ := withProbes(t, p)

	if name != "bwrap" || p.lls != 0 {
		t.Fatalf("name %q, %d landlock calls", name, p.lls)
	}
	if Describe() != "bwrap" || len(Limits()) != 0 || !NetworkDenyEnforced() {
		t.Fatalf("Describe %q Limits %v", Describe(), Limits())
	}
}

func TestLandlockBelowABI4CannotCutTheNetworkOff(t *testing.T) {
	p := &probes{landlock: func(string) (int, error) { return 3, nil }, bwrap: func(string) (string, error) {
		return "", errors.New("bwrap: not installed")
	}}
	name, _ := withProbes(t, p)

	if name != "landlock" || NetworkDenyEnforced() {
		t.Fatalf("name %q, network deny enforced %v", name, NetworkDenyEnforced())
	}
	if d := Describe(); d != "landlock (ABI 3, network deny needs ABI 4: Linux 6.7+)" {
		t.Fatalf("Describe() = %q", d)
	}
	if l := Limits(); len(l) != 4 || !strings.Contains(l[3], "network deny is not enforced") {
		t.Fatalf("Limits() = %v", l)
	}
}
