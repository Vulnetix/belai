package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// withProbe runs Backend against a stand-in bwrap on PATH and the given probe,
// with a fresh probe state and no waiting between tries.
func withProbe(t *testing.T, probe func(path string) (string, error)) (name, why string, calls int) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the bwrap probe is Linux only")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bwrap"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	oldRun, oldTries, oldBackoff := runBwrapProbe, probeTries, probeBackoff
	t.Cleanup(func() {
		runBwrapProbe, probeTries, probeBackoff = oldRun, oldTries, oldBackoff
		probeOnce, probeName, probePath, probeWhy = sync.Once{}, "", "", ""
	})
	probeOnce, probeName, probePath, probeWhy = sync.Once{}, "", "", ""
	probeTries, probeBackoff = 4, time.Microsecond
	runBwrapProbe = func(path string) (string, error) {
		calls++
		return probe(path)
	}
	name, _ = Backend()
	return name, BackendProblem(), calls
}

func TestBackendRetriesAProbeThatFailedForWantOfProcesses(t *testing.T) {
	n := 0
	name, why, calls := withProbe(t, func(string) (string, error) {
		n++
		if n < 3 {
			return "bwrap: Creating new namespace failed: Resource temporarily unavailable\n", errors.New("exit status 1")
		}
		return "", nil
	})

	if name != "bwrap" || why != "" || calls != 3 {
		t.Fatalf("name %q why %q after %d calls", name, why, calls)
	}
}

func TestBackendSaysWhyWhenEveryTryRanOutOfProcesses(t *testing.T) {
	name, why, calls := withProbe(t, func(string) (string, error) {
		return "", errors.New("fork/exec /usr/bin/bwrap: resource temporarily unavailable")
	})

	if name != "" || calls != 4 || why == "" {
		t.Fatalf("name %q why %q after %d calls", name, why, calls)
	}
	if !transientProbeFailure(why) {
		t.Fatalf("the reason does not say the machine was out of processes: %q", why)
	}
}

func TestBackendDoesNotRetryABubblewrapThatCannotWork(t *testing.T) {
	name, why, calls := withProbe(t, func(string) (string, error) {
		return "bwrap: No permissions to create new namespace\n", errors.New("exit status 1")
	})

	if name != "" || calls != 1 || why == "" {
		t.Fatalf("name %q why %q after %d calls", name, why, calls)
	}
}
