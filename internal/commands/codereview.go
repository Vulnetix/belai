// Package commands holds harness commands backed by the Vulnetix CLI.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/scanartifacts"
	"github.com/vulnetix/signet/internal/vulnetixcli"
)

// DefaultSubcommands are the Vulnetix CLI subcommands run by bare /code-review.
var DefaultSubcommands = []string{"scan", "malscan", "license", "bom", "package-firewall", "ai-firewall"}

// AllowedSubcommands is the hard allowlist for configured subcommands. It is
// the same set as the default; only these names may be persisted or executed.
var AllowedSubcommands = map[string]bool{
	"scan": true, "malscan": true, "license": true, "bom": true,
	"package-firewall": true, "ai-firewall": true,
}

// Report is the result of a code review run or status query.
type Report struct {
	Summary  string
	Manifest []string
	// Status is a plain-text rendering of CodeReviewStatus for /code-review status.
	Status string
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

// CodeReview runs the Vulnetix CLI review subcommands for a workdir.
type CodeReview struct {
	CLI     *vulnetixcli.CLI
	Workdir string
	// Subcommands overrides the default list.
	Subcommands []string
	// Timeout overrides the CLI's default timeout for scans.
	Timeout time.Duration
	// Observer, when non-nil, receives per-subcommand activity registration.
	Observer RunObserver
}

// Run executes each configured subcommand, never promotes arbitrary repository
// bytes to the model, and writes a summary plus a manifest under
// .vulnetix/signet/.
func (r CodeReview) Run(ctx context.Context) (Report, error) {
	if r.CLI == nil {
		return Report{}, fmt.Errorf("vulnetix CLI not available")
	}
	if r.Workdir == "" {
		return Report{}, fmt.Errorf("workdir is required")
	}
	subs := r.Subcommands
	if len(subs) == 0 {
		subs = DefaultSubcommands
	}

	timeout := r.CLI.Timeout
	if r.Timeout > 0 {
		timeout = r.Timeout
	}
	cli := *r.CLI
	cli.Timeout = timeout

	for _, sub := range subs {
		if !AllowedSubcommands[sub] {
			return Report{}, fmt.Errorf("subcommand %q is not in the allowlist", sub)
		}
	}

	var results []SubcommandResult
	for _, sub := range subs {
		if r.Observer == nil {
			res, err := cli.ExecIn(ctx, r.Workdir, sub)
			results = append(results, SubcommandResult{Name: sub, Output: res.Stdout, Err: err})
			continue
		}

		subCtx, cancel := context.WithCancel(ctx)
		argv := append([]string{cli.Path}, vulnetixcli.HardenedArgs(sub)...)
		sink, done := r.Observer.Start("vulnetix "+sub, argv, r.Workdir, cancel)
		res, err := cli.ExecStreamIn(subCtx, r.Workdir, sink, sub)
		done(res.ExitCode, res.TimedOut, err)
		cancel()
		results = append(results, SubcommandResult{Name: sub, Output: res.Stdout, Err: err})

		if err != nil && subCtx.Err() == context.Canceled {
			// A killed subcommand stops the run: the remainder never executes.
			break
		}
	}

	summary := buildSummary(results)
	manifest, err := r.collectManifest()
	if err != nil {
		return Report{}, err
	}
	if err := r.writeArtifacts(summary, manifest); err != nil {
		return Report{}, err
	}
	return Report{Summary: summary, Manifest: manifest}, nil
}

func buildSummary(results []SubcommandResult) string {
	var b strings.Builder
	for _, r := range results {
		status := "ok"
		if r.Err != nil {
			status = "failed"
		}
		fmt.Fprintf(&b, "vulnetix %s: %s\n", r.Name, status)
	}
	return strings.TrimSpace(b.String())
}

// collectManifest lists artifacts produced by the CLI, excluding signet's own state.
func (r CodeReview) collectManifest() ([]string, error) {
	dir := config.ProjectDir(r.Workdir)
	arts, err := scanartifacts.Enumerate(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range arts {
		if a.Kind == scanartifacts.KindSignet {
			continue
		}
		out = append(out, a.Rel)
	}
	return out, nil
}

func (r CodeReview) writeArtifacts(summary string, manifest []string) error {
	dir := config.ProjectSignetDir(r.Workdir)
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
func (r CodeReview) StatusText(cap vulnetixcli.Capabilities) string {
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
