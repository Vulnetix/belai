package depwatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/scanartifacts"
	"github.com/vulnetix/signet/internal/vulnetixcli"
)

// ExecResult is one Vulnetix CLI run.
type ExecResult struct {
	Stdout   string
	ExitCode int
}

// Exec runs the Vulnetix CLI in the workdir with a fixed, harness-built argv.
// A non-zero exit is reported through ExitCode; err is for a run that could
// not happen at all.
type Exec func(ctx context.Context, label string, args ...string) (ExecResult, error)

// Checker runs the dependency check for one manifest change.
type Checker struct {
	Workdir string
	// Decide asks the fast-tier evaluator whether the change touched
	// dependencies. Nil means always check.
	Decide func(ctx context.Context, digest string) (rolemanager.DepSentinel, error)
	Exec   Exec
	// Plan is the Vulnetix subscription tier. Pro and Enterprise get a
	// Safe Harbour fix plan as well as the scan.
	Plan vulnetixcli.Plan
}

// Report is the outcome of one check.
type Report struct {
	Change   Change
	Decision rolemanager.DepSentinel
	// Skipped means the evaluator said no dependency changed; nothing ran.
	Skipped bool
	// SCA is the scan's bounded console report: vulnerabilities, exploit
	// maturity, end-of-life and malware, and Safe Harbour recommendations on
	// a plan that has them.
	SCA string
	// ExitCode is the scan's exit status: 1 means a gate (malware, EOL,
	// exploit, severity) tripped.
	ExitCode int
	// Findings are the bounded finding lines from the scan's CycloneDX output.
	Findings []string
	// FixPlan is `vulnetix fix --dry-run` for the manifest, run only on a plan
	// with Safe Harbour.
	FixPlan string
	Plan    vulnetixcli.Plan
	Err     error
}

// Clean reports that the check ran and found nothing to act on.
func (r Report) Clean() bool {
	return !r.Skipped && r.Err == nil && r.ExitCode == 0 && len(r.Findings) == 0
}

// maxReportBytes bounds each console report handed on to the agent.
const maxReportBytes = 24 * 1024

// Check decides, then scans. A transport error from the evaluator checks
// anyway, like a malformed reply: the check is cheap, a miss is not.
func (c Checker) Check(ctx context.Context, ch Change) Report {
	r := Report{Change: ch, Plan: c.Plan, Decision: rolemanager.DepsChanged}
	if c.Decide != nil && !ch.Truncated {
		if d, err := c.Decide(ctx, Digest(ch)); err == nil && d == rolemanager.DepsUnchanged {
			r.Decision = d
			r.Skipped = true
			return r
		}
	}
	if c.Exec == nil {
		r.Err = errors.New("vulnetix CLI not available")
		return r
	}

	dir := path.Dir(ch.Path)
	scanDir := filepath.Join(c.Workdir, filepath.FromSlash(dir))
	out := OutputPath(c.Workdir, ch.Path)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		r.Err = err
		return r
	}
	_ = os.Remove(out) // a stale file must never stand in for this run

	res, err := c.Exec(ctx, "vulnetix sca "+ch.Path, ScanArgs(scanDir, out)...)
	r.ExitCode = res.ExitCode
	r.SCA = bound(res.Stdout)
	if err != nil {
		r.Err = err
		return r
	}
	if res.ExitCode > 1 {
		r.Err = fmt.Errorf("vulnetix sca exited %d", res.ExitCode)
		return r
	}
	if lines, err := scanartifacts.CycloneDXFindingLines(ctx, out, scanartifacts.DefaultMaxBytes); err == nil {
		r.Findings = lines
	}

	if c.Plan.SafeHarbour() && !r.Clean() {
		fres, ferr := c.Exec(ctx, "vulnetix fix --dry-run "+ch.Path, FixArgs(scanDir, ch)...)
		if ferr == nil || fres.Stdout != "" {
			r.FixPlan = bound(fres.Stdout)
		}
	}
	return r
}

// ScanArgs is the fixed `vulnetix sca` argv for one manifest's directory. The
// gates make the exit status say whether anything was found: malware, any
// end-of-life component, any public exploit, any severity. Memory stays
// disabled so memory.yaml keeps a single writer (the full review's sca).
func ScanArgs(scanDir, out string) []string {
	return []string{
		"sca", "--disable-memory",
		"--path", scanDir, "--depth", "1",
		"--block-malware", "--block-eol", "--block-eol-severity", "low",
		"--exploits", "poc", "--severity", "low",
		"--reachability", "off",
		"-o", out,
	}
}

// FixArgs is the fixed dry-run fix argv. A declaring manifest is named so the
// plan is restricted to it; a lockfile is fixed through whichever manifest
// declares the dependency, so it is not.
func FixArgs(scanDir string, ch Change) []string {
	args := []string{"fix", "--dry-run", "--disable-memory", "--path", scanDir, "--depth", "1"}
	if !ch.Info.Lock {
		args = append(args, "--manifest", path.Base(ch.Path))
	}
	return args
}

// OutputPath is where a check's CycloneDX document is written: under
// .vulnetix/signet/, which the review's artifact enumeration treats as
// Signet's own state, so a hook scan never shows up as a review artifact.
func OutputPath(workdir, relPath string) string {
	slug := strings.NewReplacer("/", "__", "\\", "__", ":", "_").Replace(relPath)
	return filepath.Join(config.ProjectSignetDir(workdir), "deps", slug+".cdx.json")
}

func bound(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxReportBytes {
		return s
	}
	cut := s[:maxReportBytes]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i]
	}
	return cut + "\n… (report truncated)"
}

// TaskPrompt is the background agent's task for one report. It is harness
// text: the path, the ecosystem, the decision and the plan tier. The scan
// output rides separately as attachments, classified like any tool result.
func TaskPrompt(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The session just changed %s (%s manifest, ecosystem %s", r.Change.Path, r.Change.Info.Type, r.Change.Info.Ecosystem)
	if r.Change.Info.Lock {
		b.WriteString(", a lockfile")
	}
	b.WriteString("), and the change added or updated dependencies. ")
	b.WriteString("A Vulnetix check of that manifest's directory is attached")
	if r.ExitCode == 1 {
		b.WriteString("; it tripped at least one gate (malware, end-of-life, public exploit or severity)")
	}
	b.WriteString(". ")
	if r.Plan.SafeHarbour() {
		b.WriteString("This account's plan includes Safe Harbour: the attached fix plan names vulnerability-free target versions; prefer them. ")
	} else {
		b.WriteString("This account's plan does not include Safe Harbour recommendations, so choose targets from the scan's fixed versions and say that Pro would name Safe Harbour versions. ")
	}
	b.WriteString("Triage only the dependencies this change introduced or moved, then report as your final reply: " +
		"each finding as `package@version | id | severity, exploit, EOL or malware | verdict`, and for each one the exact manifest edit that remediates it " +
		"in this ecosystem (direct bump, override/resolution/constraint for a transitive, or removal), with the command that regenerates the lockfile. " +
		"Malware is never remediated by a version bump: say to remove it and treat the machine that installed it as exposed. " +
		"Do not install packages or run package-manager commands.")
	return b.String()
}
