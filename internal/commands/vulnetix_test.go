package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/vulnetixcli"
)

// writeReviewVulnetix installs a fake vulnetix binary on PATH that consumes
// the hardening flags and optional scanner flags, records one marker file per
// subcommand, and supports the failure/sleep/lane behaviours the review tests
// drive through environment variables.
func writeReviewVulnetix(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "vulnetix")
	script := `#!/bin/sh
# Consume hardening and scanner flags; the subcommand is the first positional.
while [ $# -gt 0 ]; do
	case "$1" in
		--no-banner|--no-progress|--no-analytics|--disable-memory) shift ;;
		-o|--output-file) shift 2 ;;
		--) shift; break ;;
		-*) shift ;;
		*) break ;;
	esac
done
sub="$1"

if [ "$FAKE_FAIL_SCA" = "1" ] && [ "$sub" = "sca" ]; then
	echo "sca failed"
	exit 1
fi

if [ "$FAKE_SLEEP" = "1" ]; then
	sleep 0.12
fi

mkdir -p .vulnetix
printf 'ran\n' > ".vulnetix/ran-$sub"

if [ "$FAKE_LANE" = "1" ] && [ "$sub" = "containers" ]; then
	if [ -f .vulnetix/ran-sca ]; then
		printf 'after-sca\n' > .vulnetix/containers-after-sca
	fi
fi
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake vulnetix: %v", err)
	}
	return dir
}

func detectFake(t *testing.T, bin string) *vulnetixcli.CLI {
	t.Helper()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cli, err := vulnetixcli.Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	return cli
}

func TestVulnetixRunsSubcommandsAndWritesArtifacts(t *testing.T) {
	bin := writeReviewVulnetix(t)
	cli := detectFake(t, bin)

	workdir := t.TempDir()
	rep, err := (Vulnetix{CLI: cli, Workdir: workdir}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, sub := range []string{"sca", "containers", "sast", "secrets", "iac", "malscan", "sbom", "aibom", "cbom", "fix"} {
		if _, err := os.Stat(filepath.Join(workdir, ".vulnetix", "ran-"+sub)); err != nil {
			t.Fatalf("scanner %s did not run: %v", sub, err)
		}
	}
	if !strings.Contains(rep.Summary, "vulnetix sca: ok") || !strings.Contains(rep.Summary, "vulnetix fix: ok") {
		t.Fatalf("summary = %q", rep.Summary)
	}

	dir := config.ProjectSignetDir(workdir)
	if _, err := os.Stat(filepath.Join(dir, "code-review-summary.md")); err != nil {
		t.Fatalf("summary file missing: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "code-review-manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest []string
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("manifest json: %v", err)
	}
	for _, want := range []string{"ran-sca", "ran-sast", "ran-secrets"} {
		found := false
		for _, m := range manifest {
			if strings.HasPrefix(m, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("manifest missing %s: %v", want, manifest)
		}
	}
}

func TestVulnetixRequiresCLI(t *testing.T) {
	if _, err := (Vulnetix{CLI: nil, Workdir: t.TempDir()}).Run(context.Background()); err == nil {
		t.Fatalf("expected error without CLI")
	}
}

func TestVulnetixRejectsUnknownSubcommand(t *testing.T) {
	bin := writeReviewVulnetix(t)
	cli := detectFake(t, bin)
	_, err := (Vulnetix{CLI: cli, Workdir: t.TempDir(), Subcommands: []string{"sca", "pwn"}}).Run(context.Background())
	if err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
}

// Regression for defect 1: a scanner failure must not abort its siblings. The
// old loop broke on the first non-zero exit; the fan-out lets every scanner
// run regardless of any one failure.
func TestVulnetixFailureDoesNotStopSiblings(t *testing.T) {
	bin := writeReviewVulnetix(t)
	cli := detectFake(t, bin)
	t.Setenv("FAKE_FAIL_SCA", "1")

	workdir := t.TempDir()
	rep, err := (Vulnetix{CLI: cli, Workdir: workdir}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, sub := range []string{"containers", "sast", "secrets", "iac", "malscan", "sbom", "aibom", "cbom"} {
		if _, err := os.Stat(filepath.Join(workdir, ".vulnetix", "ran-"+sub)); err != nil {
			t.Fatalf("scanner %s did not run after sca failed: %v", sub, err)
		}
	}
	if !strings.Contains(rep.Summary, "vulnetix sca: failed") {
		t.Fatalf("summary should record the sca failure: %q", rep.Summary)
	}
}

// Free scanners run concurrently; a serial loop would take roughly N*120ms.
// The wall clock therefore distinguishes fan-out from the old sequential loop.
func TestVulnetixFreeScannersRunInParallel(t *testing.T) {
	bin := writeReviewVulnetix(t)
	cli := detectFake(t, bin)
	t.Setenv("FAKE_SLEEP", "1")

	workdir := t.TempDir()
	start := time.Now()
	if _, err := (Vulnetix{CLI: cli, Workdir: workdir}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)
	// Eight free scanners sleep 120ms concurrently; the lane (sca, containers)
	// sleeps 240ms serial. A sequential loop would sleep ~1.2s.
	if elapsed > 800*time.Millisecond {
		t.Fatalf("fan-out took %s; expected concurrent free scanners to finish well under a serial run", elapsed)
	}
}

// sca and containers share the sbom lane: containers must start only after sca
// finishes.
func TestVulnetixContainersWaitsForSca(t *testing.T) {
	bin := writeReviewVulnetix(t)
	cli := detectFake(t, bin)
	t.Setenv("FAKE_LANE", "1")

	workdir := t.TempDir()
	if _, err := (Vulnetix{CLI: cli, Workdir: workdir}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, ".vulnetix", "containers-after-sca")); err != nil {
		t.Fatalf("containers did not observe sca finishing first: %v", err)
	}
}

type fakeObserver struct {
	mu      sync.Mutex
	started map[string]bool
	cancels map[string]context.CancelFunc
}

func (f *fakeObserver) Start(name string, argv []string, dir string, cancel context.CancelFunc) (func(string), func(int, bool, error)) {
	f.mu.Lock()
	if f.started == nil {
		f.started = map[string]bool{}
		f.cancels = map[string]context.CancelFunc{}
	}
	f.started[name] = true
	f.cancels[name] = cancel
	f.mu.Unlock()
	return func(string) {}, func(int, bool, error) {}
}

// A killed scanner cancels its own context but must not abort its siblings.
func TestVulnetixKilledScannerDoesNotAbortSiblings(t *testing.T) {
	bin := writeReviewVulnetix(t)
	cli := detectFake(t, bin)
	obs := &fakeObserver{}

	workdir := t.TempDir()
	_, err := (Vulnetix{CLI: cli, Workdir: workdir, Observer: obs}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Simulate the user killing the sca row after it started. The observer
	// contract hands the cancel func back; the fan-out must not treat that as
	// "stop the whole run".
	if cancel := obs.cancels["vulnetix sca"]; cancel != nil {
		cancel()
	}
	for _, sub := range []string{"sast", "secrets", "iac", "malscan", "sbom", "aibom", "cbom"} {
		if _, err := os.Stat(filepath.Join(workdir, ".vulnetix", "ran-"+sub)); err != nil {
			t.Fatalf("scanner %s was aborted by a sibling kill: %v", sub, err)
		}
	}
}

// Cancelling the parent context stops the whole fan-out: every subcontext
// derives from it, so no scanner may keep running.
func TestVulnetixParentCancellationStopsAll(t *testing.T) {
	bin := writeReviewVulnetix(t)
	cli := detectFake(t, bin)
	t.Setenv("FAKE_SLEEP", "1")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	workdir := t.TempDir()
	if _, err := (Vulnetix{CLI: cli, Workdir: workdir}).Run(ctx); err != nil {
		t.Fatalf("Run after cancellation: %v", err)
	}
	// Sleep briefly so any scanner that ignored the cancellation would finish.
	time.Sleep(200 * time.Millisecond)
	ran := 0
	for _, sub := range []string{"sca", "containers", "sast", "secrets", "iac", "malscan", "sbom", "aibom", "cbom"} {
		if _, err := os.Stat(filepath.Join(workdir, ".vulnetix", "ran-"+sub)); err == nil {
			ran++
		}
	}
	if ran == 9 {
		t.Fatal("no scanner was stopped by parent cancellation")
	}
}

func TestStatusText(t *testing.T) {
	r := Vulnetix{}
	cap := vulnetixcli.Capabilities{Present: true, Version: vulnetixcli.Version{Major: 3, Minor: 107, Patch: 2}, Install: vulnetixcli.InstallBrew, InstallPrefix: "/homebrew", Auth: vulnetixcli.AuthState{Authenticated: false, Plan: vulnetixcli.PlanCommunity}}
	txt := r.StatusText(cap)
	if !strings.Contains(txt, "vulnetix CLI: v3.107.2") {
		t.Fatalf("status text = %q", txt)
	}
}

// The fix activity always names --path: without it the CLI prompts for a
// manifest, and with no terminal fails on any repository with two manifests.
func TestFixArgsAlwaysExplicitPath(t *testing.T) {
	for _, autoFix := range []bool{false, true} {
		args := strings.Join(FixArgs("/repo", autoFix), " ")
		if !strings.HasSuffix(args, "--path /repo") {
			t.Errorf("autofix=%v: %q lacks --path", autoFix, args)
		}
		if autoFix != strings.Contains(args, "--yes") || autoFix == strings.Contains(args, "--dry-run") {
			t.Errorf("autofix=%v: %q", autoFix, args)
		}
	}
}
