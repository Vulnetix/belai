// Package commands holds harness commands backed by the Vulnetix CLI.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/scanartifacts"
	"github.com/vulnetix/belai/internal/vulnetixcli"
)

// Scanner is one fixed review activity. Name is the Vulnetix subcommand, Args
// the fixed, allowlisted arguments (no user-supplied flag ever reaches
// exec.Command), Lane the serialisation key, and Artifacts the output files
// the scanner is expected to write.
type Scanner struct {
	Name      string
	Args      []string
	Lane      string
	Artifacts []string
}

// reviewScanners is the fixed /vulnetix review fan-out. Eight of the nine
// scanners start concurrently; `sca` and `containers` share the `sbom` lane
// because both write sbom.cdx.json, so `containers` waits for `sca`. Every
// scanner except `sca` passes --disable-memory so memory.yaml has a single
// writer and finding-history/auto-resolve keeps working for SCA. `sbom` is
// redirected to inventory.cdx.json to avoid contending with `sca`. `secrets`
// passes --ignore-git: the CLI otherwise walks up to 500 commits of history,
// which made it the scanner every review waited on; the review covers the
// working tree.
var reviewScanners = []Scanner{
	{Name: "sca", Args: []string{"sca"}, Lane: "sbom", Artifacts: []string{"sbom.cdx.json"}},
	{Name: "containers", Args: []string{"containers", "--disable-memory", "-o", ".vulnetix/containers.cdx.json"}, Lane: "sbom", Artifacts: []string{"containers.sarif", "containers.cdx.json"}},
	{Name: "sast", Args: []string{"sast", "--disable-memory"}, Artifacts: []string{"sast.sarif"}},
	{Name: "secrets", Args: []string{"secrets", "--disable-memory", "--ignore-git"}, Artifacts: []string{"secrets.sarif"}},
	{Name: "iac", Args: []string{"iac", "--disable-memory"}, Artifacts: []string{"iac.sarif"}},
	{Name: "malscan", Args: []string{"malscan", "--disable-memory"}, Artifacts: []string{"malscan.sarif"}},
	{Name: "sbom", Args: []string{"sbom", "--output-file", ".vulnetix/inventory.cdx.json"}, Artifacts: []string{"inventory.cdx.json"}},
	{Name: "aibom", Args: []string{"aibom", "--disable-memory"}, Artifacts: []string{"ai-bom.cdx.json"}},
	{Name: "cbom", Args: []string{"cbom", "--disable-memory"}, Artifacts: []string{"cbom.cdx.json"}},
}

// ActivityNames lists the activities Run reports through OnScanDone, in
// table order: the scanners to run, then the post-scan fix.
func (r Vulnetix) ActivityNames() ([]string, error) {
	scanners, err := r.scannerList()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(scanners)+1)
	for _, sc := range scanners {
		names = append(names, sc.Name)
	}
	return append(names, "fix"), nil
}

// AllowedSubcommands is the hard allowlist for configured subcommands, derived
// from the fixed scanner table plus the post-scan fix activity. Only these
// names may be persisted or executed.
var AllowedSubcommands = func() map[string]bool {
	m := map[string]bool{"fix": true}
	for _, sc := range reviewScanners {
		m[sc.Name] = true
	}
	return m
}()

// Report is the result of a /vulnetix run or status query.
type Report struct {
	Summary  string
	Manifest []string
	// Status is a plain-text rendering of CLI capabilities for /vulnetix status.
	Status string
	// TriageBlocks are the bounded, model-facing report blocks rendered from
	// the scan artifacts. The TUI classifies each before sending it.
	TriageBlocks []TriageBlock
}

// TriageBlock is one bounded report block handed to the model for triage.
type TriageBlock struct {
	Scanner string
	Label   string
	Body    string
}

// SubcommandResult captures one subcommand outcome.
type SubcommandResult struct {
	Name   string
	Output string
	Err    error
}

// RunObserver is an optional activity-register seam. commands must not import
// the TUI, so the TUI injects this to register each subcommand run and stream
// its live output.
type RunObserver interface {
	// Start registers one run and returns its live-output sink and a terminal
	// callback. The returned context is not exposed; the caller cancels it by
	// calling cancel.
	Start(name string, argv []string, dir string, cancel context.CancelFunc) (sink func(string), done func(exitCode int, timedOut bool, err error))
}

// Vulnetix runs the Vulnetix CLI review subcommands for a workdir.
type Vulnetix struct {
	CLI     *vulnetixcli.CLI
	Workdir string
	// Subcommands overrides the default scanner list. Every name must be in
	// AllowedSubcommands; flags are still taken from the fixed table.
	Subcommands []string
	// Timeout overrides the CLI's default timeout for scans.
	Timeout time.Duration
	// AutoFix opts into `vulnetix fix --yes`; the default is --dry-run, whose
	// plan is attached to the triage turn instead of mutating the tree.
	AutoFix bool
	// Observer, when non-nil, receives per-subcommand activity registration.
	Observer RunObserver
	// OnScanDone, when non-nil, is called once per scanner (and once for the
	// post-scan fix) as soon as it finishes, on that scanner's goroutine, so
	// a caller can report each one without waiting for the slowest.
	OnScanDone func(ScanOutcome)
}

// ScanOutcome is one finished review activity. Blocks are that scanner's own
// triage blocks, built from its artifacts only; they are repository-derived
// bytes and are classified by the caller like every other tool result.
type ScanOutcome struct {
	Name     string
	ExitCode int
	TimedOut bool
	Err      error
	Duration time.Duration
	// Artifact is the scanner's first artifact present on disk (or its
	// expected one), and Findings the total count parsed from its artifacts.
	Artifact string
	Findings int
	// Counts is Findings broken down by severity.
	Counts scanartifacts.Counts
	// SARIF and BOM are what the scanner's own artifacts say about the scan:
	// rules fired, files and indicators for a SARIF report; packages,
	// algorithms, models and their status for a CycloneDX inventory. Either
	// is nil when the scanner wrote no such artifact. They feed the result
	// card, which is display-only.
	SARIF  *scanartifacts.RunFacts
	BOM    *scanartifacts.BOMFacts
	Blocks []TriageBlock
}

// scannerByName returns the fixed scanner entry, or false for the post-scan
// fix activity (which is not a scanner).
func scannerByName(name string) (Scanner, bool) {
	for _, sc := range reviewScanners {
		if sc.Name == name {
			return sc, true
		}
	}
	return Scanner{}, false
}

// scannerList returns the scanners to run: the configured subset when one was
// given, otherwise the full fixed table. Configured scanners keep the fixed
// table order so the `sca` → `containers` lane survives any user ordering.
func (r Vulnetix) scannerList() ([]Scanner, error) {
	if len(r.Subcommands) == 0 {
		return reviewScanners, nil
	}
	wanted := map[string]bool{}
	for _, name := range r.Subcommands {
		if !AllowedSubcommands[name] {
			return nil, fmt.Errorf("subcommand %q is not in the allowlist", name)
		}
		wanted[name] = true
	}
	var out []Scanner
	for _, sc := range reviewScanners {
		if wanted[sc.Name] {
			out = append(out, sc)
		}
	}
	return out, nil
}

// Run fans the fixed scanner table out with bounded concurrency, never
// promotes arbitrary repository bytes to the model, and writes a summary plus
// a manifest under .vulnetix/belai/.
func (r Vulnetix) Run(ctx context.Context) (Report, error) {
	if r.CLI == nil {
		return Report{}, fmt.Errorf("vulnetix CLI not available")
	}
	if r.Workdir == "" {
		return Report{}, fmt.Errorf("workdir is required")
	}
	scanners, err := r.scannerList()
	if err != nil {
		return Report{}, err
	}

	timeout := r.CLI.Timeout
	if r.Timeout > 0 {
		timeout = r.Timeout
	}
	// Scans run until they exit or the parent context is cancelled. Probes keep
	// the 15s default; review scans do not.
	cli := *r.CLI
	cli.Timeout = vulnetixcli.NoTimeout
	if timeout > 0 {
		cli.Timeout = timeout
	}

	results := r.fanOut(ctx, cli, scanners)

	summary := r.buildSummary(ctx, results)
	manifest, err := r.collectManifest()
	if err != nil {
		return Report{}, err
	}
	if err := r.writeArtifacts(summary, manifest); err != nil {
		return Report{}, err
	}
	blocks := BuildTriageBlocks(ctx, r.Workdir)
	return Report{Summary: summary, Manifest: manifest, TriageBlocks: blocks}, nil
}

// fanOut runs each scanner concurrently except for same-lane scanners, which
// run in table order, and appends the post-scan `vulnetix fix` activity after
// `sca` completes. Results are preallocated and indexed by position, so no
// goroutine ever appends to a shared slice.
func (r Vulnetix) fanOut(ctx context.Context, cli vulnetixcli.CLI, scanners []Scanner) []SubcommandResult {
	// result slot layout: one per scanner, then the post-scan fix slot.
	results := make([]SubcommandResult, len(scanners)+1)
	done := make([]chan struct{}, len(scanners))
	for i := range done {
		done[i] = make(chan struct{})
	}
	var wg sync.WaitGroup

	runOne := func(i int, sc Scanner) {
		defer wg.Done()
		started := time.Now()
		var res vulnetixcli.Result
		var err error
		if r.Observer == nil {
			res, err = cli.ExecIn(ctx, r.Workdir, sc.Args...)
		} else {
			subCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			argv := append([]string{cli.Path}, vulnetixcli.HardenedArgs(sc.Args...)...)
			sink, finish := r.Observer.Start("vulnetix "+sc.Name, argv, r.Workdir, cancel)
			res, err = cli.ExecStreamIn(subCtx, r.Workdir, sink, sc.Args...)
			finish(res.ExitCode, res.TimedOut, err)
		}
		results[i] = SubcommandResult{Name: sc.Name, Output: res.Stdout, Err: err}
		// Report before releasing the lane, so a lane successor (containers)
		// cannot overwrite a shared artifact while this scanner's blocks are
		// being read.
		r.reportScan(ctx, sc, res, err, time.Since(started))
		close(done[i])
	}

	// Free scanners start immediately; same-lane scanners wait for every
	// earlier member of the lane, so `containers` starts only after `sca`.
	for i, sc := range scanners {
		if sc.Lane == "" {
			wg.Add(1)
			go runOne(i, sc)
			continue
		}
		wg.Add(1)
		go func(i int, sc Scanner) {
			members := laneMembers(scanners, sc.Lane)
			idx := laneIndex(scanners, sc.Lane, i)
			for j := 0; j < idx; j++ {
				<-done[members[j]]
			}
			runOne(i, sc)
		}(i, sc)
	}

	// Post-scan dependency remediation: run `vulnetix fix` after `sca`. It is
	// dry-run by default so a review never mutates the tree without an opt-in.
	scaIdx := -1
	for i, sc := range scanners {
		if sc.Name == "sca" {
			scaIdx = i
			break
		}
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if scaIdx >= 0 {
			<-done[scaIdx]
		}
		args := FixArgs(r.Workdir, r.AutoFix)
		started := time.Now()
		var res vulnetixcli.Result
		var err error
		if r.Observer == nil {
			res, err = cli.ExecIn(ctx, r.Workdir, args...)
		} else {
			subCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			argv := append([]string{cli.Path}, vulnetixcli.HardenedArgs(args...)...)
			sink, finish := r.Observer.Start("vulnetix fix", argv, r.Workdir, cancel)
			res, err = cli.ExecStreamIn(subCtx, r.Workdir, sink, args...)
			finish(res.ExitCode, res.TimedOut, err)
		}
		results[len(scanners)] = SubcommandResult{Name: "fix", Output: res.Stdout, Err: err}
		if r.OnScanDone != nil {
			r.OnScanDone(ScanOutcome{Name: "fix", ExitCode: res.ExitCode, TimedOut: res.TimedOut, Err: err, Duration: time.Since(started)})
		}
	}()

	wg.Wait()
	return results
}

// FixArgs is the post-scan fix argv. --path is always explicit: without it the
// CLI asks which manifest to fix when several have candidates, and with no
// terminal it fails with "multiple manifests have autofix candidates" — which
// is what every review of a repository with two manifests reported.
func FixArgs(workdir string, autoFix bool) []string {
	if autoFix {
		return []string{"fix", "--yes", "--path", workdir}
	}
	return []string{"fix", "--dry-run", "--path", workdir}
}

func laneMembers(scanners []Scanner, lane string) []int {
	var out []int
	for i, sc := range scanners {
		if sc.Lane == lane {
			out = append(out, i)
		}
	}
	return out
}

func laneIndex(scanners []Scanner, lane string, target int) int {
	for i, idx := range laneMembers(scanners, lane) {
		if idx == target {
			return i
		}
	}
	return 0
}

// reportScan hands one finished scanner's outcome, with the triage blocks and
// finding count of its own artifacts, to OnScanDone.
func (r Vulnetix) reportScan(ctx context.Context, sc Scanner, res vulnetixcli.Result, err error, took time.Duration) {
	if r.OnScanDone == nil {
		return
	}
	dir := config.ProjectDir(r.Workdir)
	arts, aerr := scanartifacts.Enumerate(dir)
	if aerr != nil {
		arts = nil
	}
	arts = artifactsIn(arts, sc.Artifacts)
	summary := scanartifacts.Summarize(ctx, dir, arts)
	art, findings := scannerFindings(summary, sc)
	var counts scanartifacts.Counts
	for _, rel := range sc.Artifacts {
		if fs, ok := summary.PerFile[rel]; ok {
			counts = counts.Merge(fs.Counts)
		}
	}
	sarif, bom := scanFacts(ctx, arts)
	r.OnScanDone(ScanOutcome{
		Name:     sc.Name,
		ExitCode: res.ExitCode,
		TimedOut: res.TimedOut,
		Err:      err,
		Duration: took,
		Artifact: art,
		Findings: findings,
		Counts:   counts,
		Blocks:   BuildTriageBlocksFor(ctx, r.Workdir, sc.Artifacts),
		SARIF:    sarif,
		BOM:      bom,
	})
}

// scanFacts reads the first SARIF and the first CycloneDX artifact of a
// scanner. A file that fails to parse contributes nothing.
func scanFacts(ctx context.Context, arts []scanartifacts.Artifact) (*scanartifacts.RunFacts, *scanartifacts.BOMFacts) {
	var sarif *scanartifacts.RunFacts
	var bom *scanartifacts.BOMFacts
	for _, a := range arts {
		switch a.Kind {
		case scanartifacts.KindSARIF:
			if sarif == nil {
				if f, err := scanartifacts.SARIFFacts(ctx, a.Path, scanartifacts.DefaultMaxBytes); err == nil {
					sarif = &f
				}
			}
		case scanartifacts.KindCycloneDXSBOM, scanartifacts.KindCycloneDXCBOM, scanartifacts.KindCycloneDXAIBOM:
			if bom == nil {
				if f, err := scanartifacts.CycloneDXFacts(ctx, a.Path, scanartifacts.DefaultMaxBytes); err == nil {
					bom = &f
				}
			}
		}
	}
	return sarif, bom
}

// scannerFindings returns a scanner's first artifact present in the summary
// (its first expected one when none is) and the findings across all of them.
func scannerFindings(summary scanartifacts.Summary, sc Scanner) (art string, findings int) {
	for _, rel := range sc.Artifacts {
		if fs, present := summary.PerFile[rel]; present {
			if art == "" {
				art = rel
			}
			findings += fs.Counts.Total()
		}
	}
	if art == "" && len(sc.Artifacts) > 0 {
		art = sc.Artifacts[0]
	}
	return art, findings
}

// buildSummary renders per-scanner status, exit code, artifact and finding
// count from the parsed artifacts, then writes the legacy belai summary file.
func (r Vulnetix) buildSummary(ctx context.Context, results []SubcommandResult) string {
	dir := config.ProjectDir(r.Workdir)
	arts, err := scanartifacts.Enumerate(dir)
	if err != nil {
		arts = nil
	}
	summary := scanartifacts.Summarize(ctx, dir, arts)

	var b strings.Builder
	for _, res := range results {
		if res.Name == "" {
			continue
		}
		status := "ok"
		if res.Err != nil {
			status = "failed"
		}
		sc, ok := scannerByName(res.Name)
		art := ""
		findings := 0
		if ok {
			art, findings = scannerFindings(summary, sc)
		}
		line := fmt.Sprintf("vulnetix %s: %s", res.Name, status)
		if res.Err != nil {
			line += fmt.Sprintf(" (%v)", res.Err)
		}
		if art != "" {
			line += fmt.Sprintf(" · %s · %d findings", art, findings)
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimSpace(b.String())
}

// collectManifest lists artifacts produced by the CLI, excluding belai's own state.
func (r Vulnetix) collectManifest() ([]string, error) {
	dir := config.ProjectDir(r.Workdir)
	arts, err := scanartifacts.Enumerate(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range arts {
		if a.Kind == scanartifacts.KindBelai {
			continue
		}
		out = append(out, a.Rel)
	}
	sort.Strings(out)
	return out, nil
}

func (r Vulnetix) writeArtifacts(summary string, manifest []string) error {
	dir := config.ProjectBelaiDir(r.Workdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "code-review-summary.md"), []byte(summary), 0o600); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "code-review-manifest.json"), data, 0o600)
}

// StatusText returns a plain-text rendering of CLI capabilities.
func (r Vulnetix) StatusText(cap vulnetixcli.Capabilities) string {
	var b strings.Builder
	if !cap.Present {
		b.WriteString("vulnetix CLI: not installed\n")
		return b.String()
	}
	fmt.Fprintf(&b, "vulnetix CLI: %s\n", cap.Version)
	fmt.Fprintf(&b, "  path:      %s\n", cap.Path)
	if cap.Install != "" {
		fmt.Fprintf(&b, "  install:   %s (%s)\n", cap.Install, cap.InstallPrefix)
	}
	fmt.Fprintf(&b, "  auth:      %v\n", cap.Auth.Authenticated)
	fmt.Fprintf(&b, "  plan:      %s\n", cap.Auth.Plan)
	return b.String()
}
