// Package testpass is the post-end test pass shared by the TUI, headless and
// ACP surfaces: when a goal or plan finishes (or the session ends) the harness
// runs the repository's test suites, and then either has a fast model write a
// short report (pass) or hands the failure to a main-model diagnose-and-fix
// loop (fail).
//
// The harness owns every decision that matters. Which command runs comes from
// the fixed detection table or the user's override; pass or fail is the exit
// code; whether the pass runs at all is the user's `tests.post_end` setting.
// The failing output is arbitrary process text, so it reaches a model only
// through the injected Gate (sanitise, then classify as process output), and
// only as an attachment. A surface supplies the pieces that differ (the gate,
// the fast classifier, the fix loop) so this package stays free of any
// session or UI type.
package testpass

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/repomap"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/testdetect"
	"github.com/vulnetix/belai/internal/testrun"
)

// Trigger is the moment that starts a pass.
type Trigger string

const (
	// TriggerGoal is a goal that ended GOAL_COMPLETE.
	TriggerGoal Trigger = "goal"
	// TriggerPlan is an approved plan that ended GOAL_COMPLETE.
	TriggerPlan Trigger = "plan"
	// TriggerSession is the end of the session.
	TriggerSession Trigger = "session"
)

// Should reports whether the user's `tests.post_end` level runs the pass at
// trigger t. "goal" covers a completed goal, "goal_plan" adds a completed
// plan, and "session" adds session end on top of both.
func Should(s config.Settings, t Trigger) bool {
	switch s.TestsPostEnd() {
	case "goal":
		return t == TriggerGoal
	case "goal_plan":
		return t == TriggerGoal || t == TriggerPlan
	case "session":
		return t == TriggerGoal || t == TriggerPlan || t == TriggerSession
	}
	return false
}

// Gate turns raw process output into text that may ride on a model turn. It
// sanitises and classifies the text as process output under the effective
// posture. ok is false when the text is withheld or the gate failed, in which
// case nothing of the output may be used.
type Gate func(ctx context.Context, output string) (text string, ok bool)

// FixRequest is what the surface's fix loop receives for one pass.
type FixRequest struct {
	// Mode is "diagnose" (read-only) or "fix" (full surface).
	Mode string
	// Pass is the 1-based pass number and MaxPasses the ceiling.
	Pass, MaxPasses int
	// Facts is harness-composed: the failing suites, statuses and exit codes.
	Facts string
	// Output is the gated failing output. It is empty when the gate withheld
	// it, and the loop must then run the command itself under its own rules.
	Output string
	// OutputWithheld is true when Output is empty because the gate refused it.
	OutputWithheld bool
	// Commands are the suite commands, for the loop to re-run.
	Commands []string
}

// Fixer runs the main-model agentic loop for one request. It returns when the
// loop's turn ends. An error stops the fail branch.
type Fixer func(ctx context.Context, req FixRequest) error

// Config wires one pass. Every field but Notify and Report and Fix is
// required for a run.
type Config struct {
	Settings config.Settings
	// Dir is the trusted repository root the suites run in.
	Dir string
	// Suites is the detected suite table (repomap.Map.TestSuites).
	Suites []testdetect.Suite
	// Changed returns the paths changed so far, read before each run so a fix
	// pass's own edits scope the re-run.
	Changed func() []string
	// Perms is the user's permission configuration.
	Perms permissions.Settings
	// Sandbox is the policy the suites run under.
	Sandbox sandbox.Policy
	// Gate gates failing output before any model sees it. Nil withholds it.
	Gate Gate
	// Report is the fast-tier classifier for the pass report. Nil uses the
	// harness-composed line.
	Report rolemanager.Classifier
	// Fix is the surface's fix loop. Nil disables the fail branch.
	Fix Fixer
	// Notify receives one harness-composed line per step. Optional.
	Notify func(string)
}

// Outcome is the harness result of one pass.
type Outcome struct {
	Trigger Trigger
	// Skipped says why no suite ran ("" when one did).
	Skipped string
	Results []testrun.Result
	Passed  bool
	// FixPasses counts the fail-branch loops that ran.
	FixPasses int
	// Report is the pass report ("" on a failing outcome).
	Report string
	// ReportByModel is true when the fast model wrote Report.
	ReportByModel bool
}

// Summary is the volatile repo-map fact for the outcome.
func (o Outcome) Summary() *repomap.TestRunSummary {
	if o.Skipped != "" || len(o.Results) == 0 {
		return nil
	}
	return &repomap.TestRunSummary{Passed: o.Passed, Suites: len(o.Results)}
}

// Line is the one-line harness summary of the outcome, facts only.
func (o Outcome) Line() string {
	if o.Skipped != "" {
		return "tests: " + o.Skipped
	}
	var parts []string
	for _, r := range o.Results {
		parts = append(parts, fmt.Sprintf("%s %s", r.Suite, r.Status))
	}
	verdict := "pass"
	if !o.Passed {
		verdict = "fail"
	}
	line := fmt.Sprintf("tests %s (%s)", verdict, strings.Join(parts, ", "))
	if o.FixPasses > 0 {
		line += fmt.Sprintf(" after %d fix pass(es)", o.FixPasses)
	}
	return line
}

// maxDigestBytes caps the passing-output digest the report role is shown.
const maxDigestBytes = 8 * 1024

// Step is one run of the suites and what should happen next.
type Step struct {
	Outcome Outcome
	// Fix is the fail-branch request the surface should act on, or nil when
	// the run passed, the branch is off, the budget is spent, or nothing
	// actually ran (a refusal or a start error leaves nothing to fix).
	Fix *FixRequest
}

// RunOnce runs the suites once. fixPasses is how many fail-branch passes
// already ran, which sets the budget check and is carried on the outcome.
// It is the primitive both Run (a blocking loop) and the TUI (a turn-driven
// loop across the visible session) build on.
func RunOnce(ctx context.Context, cfg Config, trigger Trigger, fixPasses int) Step {
	out := Outcome{Trigger: trigger, FixPasses: fixPasses}
	if !cfg.Settings.TestsEnabled() {
		out.Skipped = "the post-end test pass is off"
		return Step{Outcome: out}
	}
	var changed []string
	if cfg.Changed != nil {
		changed = cfg.Changed()
	}
	plan := testrun.BuildPlan(cfg.Suites, cfg.Settings.TestsCommand(), cfg.Settings.TestsScope(), changed)
	if len(plan.Argvs) == 0 {
		out.Skipped = "no test suite detected"
		return Step{Outcome: out}
	}
	opts := testrun.Options{
		Dir:     cfg.Dir,
		Timeout: time.Duration(cfg.Settings.TestsTimeoutSeconds()) * time.Second,
		Perms:   cfg.Perms,
	}
	if cfg.Notify != nil {
		cfg.Notify(fmt.Sprintf("running tests: %s", strings.Join(plan.Names, ", ")))
	}
	out.Results = testrun.RunPlan(sandbox.WithPolicy(ctx, cfg.Sandbox), opts, plan)
	out.Passed = testrun.AllPassed(out.Results)

	if out.Passed {
		in := reportInput(ctx, cfg, trigger, out)
		if cfg.Settings.TestsReportEnabled() && cfg.Report != nil {
			out.Report, out.ReportByModel = rolemanager.DecideTestReport(ctx, cfg.Report, in)
		} else {
			out.Report = rolemanager.ComposeTestReport(in)
		}
		return Step{Outcome: out}
	}
	onFail := cfg.Settings.TestsOnFail()
	max := cfg.Settings.TestsMaxFixPasses()
	if onFail == "off" || fixPasses >= max || !anyRan(out.Results) || ctx.Err() != nil {
		return Step{Outcome: out}
	}
	req := failureRequest(ctx, cfg, out.Results, onFail, fixPasses+1, max)
	return Step{Outcome: out, Fix: &req}
}

// Run executes one pass to its end: run the suites, then report on a pass or
// loop on a failure through cfg.Fix. It never panics on a missing piece;
// every gap is a Skipped reason or a harness-composed fallback.
func Run(ctx context.Context, cfg Config, trigger Trigger) Outcome {
	fixPasses := 0
	for {
		step := RunOnce(ctx, cfg, trigger, fixPasses)
		if step.Fix == nil || cfg.Fix == nil {
			return step.Outcome
		}
		fixPasses++
		if cfg.Notify != nil {
			cfg.Notify(fmt.Sprintf("tests failed: %s pass %d of %d", step.Fix.Mode, step.Fix.Pass, step.Fix.MaxPasses))
		}
		err := cfg.Fix(ctx, *step.Fix)
		// A read-only loop changes nothing, so a re-run would repeat itself.
		if err != nil || step.Fix.Mode == "diagnose" {
			if err != nil && cfg.Notify != nil {
				cfg.Notify("tests: the " + step.Fix.Mode + " loop stopped: " + errWord(ctx))
			}
			out := step.Outcome
			out.FixPasses = fixPasses
			return out
		}
	}
}

// anyRan reports whether at least one result is a real pass or fail, as
// opposed to a refusal or a start error.
func anyRan(rs []testrun.Result) bool {
	for _, r := range rs {
		switch r.Status {
		case testrun.Passed, testrun.Failed, testrun.TimedOut:
			return true
		}
	}
	return false
}

func errWord(ctx context.Context) string {
	if ctx.Err() != nil {
		return "cancelled"
	}
	return "error"
}

// failureRequest builds the fail branch's request from the failing results.
func failureRequest(ctx context.Context, cfg Config, rs []testrun.Result, mode string, pass, max int) FixRequest {
	req := FixRequest{Mode: mode, Pass: pass, MaxPasses: max}
	var facts, raw []string
	for _, r := range rs {
		if r.OK() {
			continue
		}
		facts = append(facts, fmt.Sprintf("suite %s: %s (exit %d) via `%s`", r.Suite, r.Status, r.ExitCode, testrun.Subject(r.Command)))
		req.Commands = append(req.Commands, testrun.Subject(r.Command))
		if r.Output != "" {
			raw = append(raw, "== "+r.Suite+" ==\n"+r.Output)
		}
	}
	req.Facts = strings.Join(facts, "\n")
	joined := strings.Join(raw, "\n")
	if joined == "" {
		return req
	}
	if cfg.Gate != nil {
		if text, ok := cfg.Gate(ctx, joined); ok {
			req.Output = text
			return req
		}
	}
	req.OutputWithheld = true
	return req
}

// reportInput builds the report role's input. Passing output rides only when
// the gate admits it.
func reportInput(ctx context.Context, cfg Config, trigger Trigger, o Outcome) rolemanager.TestReportInput {
	in := rolemanager.TestReportInput{Trigger: string(trigger), FixPasses: o.FixPasses}
	var raw []string
	for _, r := range o.Results {
		in.Suites = append(in.Suites, rolemanager.TestReportSuite{
			Name:       r.Suite,
			Status:     string(r.Status),
			ExitCode:   r.ExitCode,
			DurationMS: r.Duration.Milliseconds(),
		})
		if r.Output != "" {
			raw = append(raw, "== "+r.Suite+" ==\n"+r.Output)
		}
	}
	joined := strings.Join(raw, "\n")
	if len(joined) > maxDigestBytes {
		joined = joined[len(joined)-maxDigestBytes:]
	}
	if joined != "" && cfg.Gate != nil {
		if text, ok := cfg.Gate(ctx, joined); ok {
			in.OutputDigest = text
		}
	}
	return in
}
