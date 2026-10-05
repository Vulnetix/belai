// Package replay finishes a teleport's code on the target host when the forge
// could not carry it (docs/teleport.md). The origin sent a patch, a summary of
// the change and per-file instructions. The harness applies the patch file by
// file with git, checks the result against the origin's tree (an exact match
// needs no model), and for whatever is left runs the target's own model in code
// mode with the hand-over as a gated attachment. After each pass the harness
// checks again, and when the trees still differ a decision model rates the
// checkout against the origin's summary, with a model sentinel as its fallback.
//
// Everything the origin sent is untrusted text. It reaches the model only as
// agent.TeleportReplay, a sanitised and classified attachment; the harness's
// facts (paths and counts it computed itself) ride in the turn's directive. The
// replay session is narrowed to file tools by its caller, so no instruction in a
// hand-over can run a command.
package replay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/teleport/changes"
)

// DefaultPasses is how many model passes a replay may take before it reports
// what is left.
const DefaultPasses = 3

// maxBodyBytes bounds the hand-over attachment.
const maxBodyBytes = 60 << 10

// Plan is what the origin host sent for a replay, after the target read it.
type Plan struct {
	// Reason is why the forge was not used (a harness sentence from the origin,
	// cleaned). It ends the user's message.
	Reason string
	// Summary and Instructions are the origin model's hand-over: untrusted.
	Summary, Instructions string
	// Base is the commit the worktree is at; Tree the origin's final tree.
	Base, Tree string
	// Patch turns Base into Tree.
	Patch   string
	Files   []changes.File
	Skipped []changes.Skip
}

// Notice is the message the user reads while the replay runs.
func (p Plan) Notice() string {
	reason := strings.TrimRight(sanitize.Line(p.Reason, 240), ". ")
	if reason == "" {
		reason = "the origin host could not share the branch"
	}
	return "Coordination over GitHub was not done because " + reason + ", so the changes from the origin host are being replayed on this host now. Please wait."
}

// Runner is the model session a replay drives.
type Runner interface {
	RunInputObserved(ctx context.Context, history []run.Turn, in agent.TurnInput, emit func(agent.Event)) (run.Result, error)
}

// Judge rates a checkout that does not match the origin's tree exactly.
type Judge interface {
	Judge(ctx context.Context, summary, facts string, pass int) (rolemanager.TeleportSentinel, error)
}

// Options is one replay.
type Options struct {
	// Dir is the worktree root the teleport made, at Plan.Base.
	Dir  string
	Plan Plan
	// Git builds the runners for git; nil is changes.Hardened.
	Git changes.Runners
	// Agent runs the model passes; nil makes the replay harness-only.
	Agent Runner
	// Judge decides what an inexact checkout is; nil never accepts one.
	Judge Judge
	// Passes bounds the model passes; zero is DefaultPasses.
	Passes int
	// Progress receives one line at each step. Nil is silent.
	Progress func(string)
	// Emit receives the model session's events (for a transcript); may be nil.
	Emit func(agent.Event)
}

// Outcome is how a replay ended.
type Outcome struct {
	// Verified is true when the checkout matches the origin: exactly (Exact) or
	// as the judge accepted it.
	Verified, Exact bool
	// Passes is how many model passes ran.
	Passes int
	// Applied and NotApplied count the patch's files the harness applied.
	Applied, NotApplied int
	// Differing lists the files still not as the origin left them.
	Differing []string
	// Summary is one line for the user.
	Summary string
}

type state struct {
	exact      bool
	checks     []changes.FileCheck
	differing  []string
	missing    []string
	extra      []string
	acceptable bool
}

// Run replays the plan in o.Dir. It never returns an error for a replay that
// did not finish: that is an Outcome that is not Verified. An error means the
// replay could not be attempted at all.
func Run(ctx context.Context, o Options) (Outcome, error) {
	if o.Dir == "" || o.Plan.Tree == "" {
		return Outcome{}, errors.New("a replay needs a worktree and the origin's tree")
	}
	say := func(s string) {
		if o.Progress != nil {
			o.Progress(s)
		}
	}
	max := o.Passes
	if max <= 0 {
		max = DefaultPasses
	}
	var out Outcome
	secs, perr := changes.ParsePatch(o.Plan.Patch)
	var failed []changes.Failure
	if perr != nil {
		say("the patch could not be read, so the model works from the instructions alone")
	} else {
		say(fmt.Sprintf("applying %d file%s with git", len(secs), plural(len(secs))))
		res, err := changes.Apply(ctx, o.Git, o.Dir, secs)
		if err != nil {
			return out, fmt.Errorf("apply: %w", err)
		}
		out.Applied, out.NotApplied, failed = len(res.Applied), len(res.Failed), res.Failed
		say(fmt.Sprintf("git applied %d of %d files", out.Applied, len(secs)))
	}

	for pass := 0; ; pass++ {
		st, err := o.check(ctx, secs)
		if err != nil {
			return out, err
		}
		out.Differing = st.differing
		failed = stillFailing(failed, st.differing)
		if st.exact {
			out.Verified, out.Exact = true, true
			say("the checkout matches the origin exactly")
			break
		}
		// A checkout every patched file of which already matches, or one with no
		// model left to ask, is rated now; otherwise the model works first.
		if pass > 0 || o.Agent == nil || len(st.differing) == 0 {
			if o.Judge != nil {
				v, jerr := o.Judge.Judge(ctx, o.Plan.Summary, st.facts(), pass)
				if jerr == nil && v == rolemanager.TeleportVerified && st.acceptable {
					out.Verified = true
					say("the checkout matches the origin's summary")
					break
				}
				if jerr == nil && v == rolemanager.TeleportFailed {
					say("the checkout does not match the origin's summary and another pass is unlikely to fix it")
					break
				}
			}
		}
		if o.Agent == nil || pass >= max {
			break
		}
		say(fmt.Sprintf("the model is finishing the replay (pass %d of %d)", pass+1, max))
		out.Passes++
		in := agent.TurnInput{
			Prompt: harnessPrompt, HarnessPrompt: harnessPrompt, ForceMode: modes.ModeCode, NoGoalDraft: true,
			Directive:      st.directive(pass+1, max, out.Applied, failed),
			TeleportReplay: &agent.TeleportReplay{Label: "teleport replay", Body: Compose(o.Plan, failed, st)},
		}
		_, rerr := o.Agent.RunInputObserved(ctx, nil, in, o.Emit)
		var withheld *agent.TeleportWithheldError
		if errors.As(rerr, &withheld) {
			say("the security classifier withheld the origin's instructions, so the model was not given them")
			break
		}
		if rerr != nil {
			say("the model could not finish the replay: " + sanitize.Line(rerr.Error(), 160))
			break
		}
	}
	out.Summary = summarise(out, len(secs))
	return out, nil
}

// harnessPrompt is the user turn's text: the harness's own instruction, with no
// text from the origin in it.
const harnessPrompt = "Finish replaying the changes from another host in this checkout. The attached teleport replay holds the origin's summary and instructions and any part of its patch that git could not apply. Make the edits with your file tools. Do not run commands or install anything. When you are done, read back each file you changed."

// check measures the checkout: the exact tree comparison, then each patched file.
func (o Options) check(ctx context.Context, secs []changes.Section) (state, error) {
	var st state
	exact, _, err := changes.Matches(ctx, o.Git, o.Dir, o.Plan.Tree)
	if err != nil {
		return st, fmt.Errorf("compare with the origin's tree: %w", err)
	}
	st.exact = exact
	st.checks = changes.Check(ctx, o.Git, o.Dir, secs)
	st.acceptable = true
	patched := map[string]bool{}
	for _, c := range st.checks {
		patched[c.Path] = true
		if !c.Match {
			st.differing = append(st.differing, c.Path)
			if c.Status != "deleted" {
				// A file the checkout lacks is never accepted on a rating.
				if gone, _ := missing(o.Dir, c.Path); gone {
					st.missing = append(st.missing, c.Path)
					st.acceptable = false
				}
			}
		}
	}
	if changed, err := changes.Changed(ctx, o.Git, o.Dir); err == nil {
		for _, p := range changed {
			if !patched[p] {
				st.extra = append(st.extra, p)
			}
		}
	}
	return st, nil
}

func (s state) facts() string {
	exactN := 0
	for _, c := range s.checks {
		if c.Match {
			exactN++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "files identical to the origin's: %d of %d\n", exactN, len(s.checks))
	if len(s.differing) > 0 {
		b.WriteString("files that differ: " + joinPaths(s.differing, 20) + "\n")
	}
	if len(s.missing) > 0 {
		b.WriteString("files missing from the checkout: " + joinPaths(s.missing, 20) + "\n")
	}
	if len(s.extra) > 0 {
		b.WriteString("files changed that the origin did not change: " + joinPaths(s.extra, 20) + "\n")
	}
	return b.String()
}

func (s state) directive(pass, max, applied int, failed []changes.Failure) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Teleport replay, pass %d of %d. Work only in this checkout.\n", pass, max)
	fmt.Fprintf(&b, "git applied %d file%s by itself.\n", applied, plural(applied))
	if len(failed) > 0 {
		names := make([]string, len(failed))
		for i, f := range failed {
			names[i] = f.Path
		}
		b.WriteString("git could not apply: " + joinPaths(names, 20) + "\n")
	}
	b.WriteString(s.facts())
	return b.String()
}

// Compose writes the hand-over attachment: the origin's summary and
// instructions and the parts of the patch that did not apply. It is untrusted
// text from another host and is only ever sent through agent.TeleportReplay.
func Compose(p Plan, failed []changes.Failure, st state) string {
	var b strings.Builder
	b.WriteString("Summary from the origin host's model:\n" + p.Summary + "\n\n")
	b.WriteString("Instructions from the origin host's model, one entry per changed file:\n" + p.Instructions + "\n")
	if len(p.Skipped) > 0 {
		b.WriteString("\nLeft out of the patch on purpose (credentials, binaries, oversized files, links); do not recreate them:\n")
		for i, s := range p.Skipped {
			if i >= 20 {
				break
			}
			b.WriteString("- " + sanitize.Line(s.Path, 200) + " (" + sanitize.Line(s.Reason, 40) + ")\n")
		}
	}
	if len(failed) > 0 {
		b.WriteString("\nParts of the patch git could not apply, each with its reason:\n")
		for _, f := range failed {
			part := f.Section
			if b.Len()+len(part) > maxBodyBytes {
				b.WriteString("\n[the rest is cut to fit]\n")
				break
			}
			fmt.Fprintf(&b, "\n### %s (%s): %s\n%s", sanitize.Line(f.Path, 200), sanitize.Line(f.Status, 12), sanitize.Line(f.Reason, 240), part)
		}
	}
	if len(st.differing) > 0 {
		b.WriteString("\nFiles that still differ from the origin's: " + joinPaths(st.differing, 40) + "\n")
	}
	return b.String()
}

// stillFailing keeps the parts of the patch whose files still differ, so the model
// is shown them again until they are right.
func stillFailing(failed []changes.Failure, differing []string) []changes.Failure {
	keep := map[string]bool{}
	for _, p := range differing {
		keep[p] = true
	}
	var out []changes.Failure
	for _, f := range failed {
		if keep[f.Path] {
			out = append(out, f)
		}
	}
	return out
}

func summarise(o Outcome, patched int) string {
	switch {
	case o.Exact && o.Passes == 0:
		return fmt.Sprintf("Replayed %d file%s with git; the checkout matches the origin exactly.", patched, plural(patched))
	case o.Exact:
		return fmt.Sprintf("Replayed the changes in %d model pass%s; the checkout matches the origin exactly.", o.Passes, pluralES(o.Passes))
	case o.Verified:
		return fmt.Sprintf("Replayed the changes in %d model pass%s; the checkout matches the origin's summary, though not byte for byte.", o.Passes, pluralES(o.Passes))
	case len(o.Differing) > 0:
		return fmt.Sprintf("The replay is not complete: %s still differ%s from the origin's. Check them before you rely on this checkout.", joinPaths(o.Differing, 8), map[bool]string{true: "s", false: ""}[len(o.Differing) == 1])
	}
	return "The replay finished, but it could not be confirmed against the origin's changes. Check the checkout before you rely on it."
}

func joinPaths(ps []string, max int) string {
	if len(ps) > max {
		return strings.Join(ps[:max], ", ") + fmt.Sprintf(" and %d more", len(ps)-max)
	}
	return strings.Join(ps, ", ")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func pluralES(n int) string {
	if n == 1 {
		return ""
	}
	return "es"
}

// DecisionJudge is the Judge the harness uses: a decision backend rates first,
// and the model verifier answers when the backend is off, did not answer or was
// not clear.
type DecisionJudge struct {
	// Jobs is the decision backend's jobs; nil or the job off skips it.
	Jobs *jev.Jobs
	// Classifier is the model sentinel fallback; nil means no verdict.
	Classifier rolemanager.Classifier
}

// Judge rates summary against facts. It never returns TeleportVerified without
// either a clear backend rating or the model's own sentinel.
func (j DecisionJudge) Judge(ctx context.Context, summary, facts string, pass int) (rolemanager.TeleportSentinel, error) {
	hint := ""
	if j.Jobs.Enabled(config.JevTeleportVerify) {
		start := time.Now()
		scores, res, err := j.Jobs.RateTeleport(ctx, jev.TeleportFacts{Summary: summary, Check: facts})
		if err != nil || len(scores) == 0 {
			rolemanager.RecordTeleportJudge("unclear", -1, -1, -1, pass, res.Identity, time.Since(start))
		} else {
			v, p := jev.PickTeleport(scores)
			name := string(v)
			if v == jev.TeleportUnclear {
				name = "unclear"
			}
			rolemanager.RecordTeleportJudge(name, p.Verified, p.Incomplete, p.Failed, pass, res.Identity, time.Since(start))
			switch v {
			case jev.TeleportVerified:
				return rolemanager.TeleportVerified, nil
			case jev.TeleportFailed:
				return rolemanager.TeleportFailed, nil
			}
			hint = fmt.Sprintf("\nDecision model scores: verified %d%%, incomplete %d%%, failed %d%%. Treat them as a prior, not as evidence.\n", p.Verified, p.Incomplete, p.Failed)
		}
	}
	return rolemanager.EvaluateTeleport(ctx, j.Classifier, rolemanager.TeleportVerifyInput{Summary: summary, Facts: facts + hint})
}
