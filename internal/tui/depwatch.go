package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/bgagent"
	"github.com/vulnetix/belai/internal/depwatch"
	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/vulnetixcli"
)

// depCheckMsg carries one finished dependency check back to the UI loop,
// with its report attachments already sanitized and, under guardrails,
// classified.
type depCheckMsg struct {
	report   depwatch.Report
	atts     []run.Attachment
	withheld []string
}

// depState is the dependency hook's per-App state. It is a pointer so the
// check goroutines share one plan-tier cache without touching App fields.
type depState struct {
	watch depwatch.Coalescer
	seq   int
	// agents maps a running background agent's name to the manifest it
	// triages, so its report can be routed back when it finishes.
	agents map[string]string

	planOnce sync.Once
	plan     vulnetixcli.Plan
}

func (a *App) depWatchEnabled() bool {
	return a.settings.Vulnetix.DepWatchEnabled()
}

// observeDepDiff feeds one tool's observed file changes to the hook. It is
// deterministic: the path and post-change content decide, nothing else.
func (a *App) observeDepDiff(ch *filediff.Change) {
	if ch == nil || !a.depWatchEnabled() {
		return
	}
	for _, f := range ch.Files {
		a.depsState().watch.Observe(filepath.ToSlash(f.Path), f.Old, f.New, f.Created, f.Deleted, f.Truncated || f.Binary)
	}
}

// flushDepWatch starts one check per manifest the finished turn changed. The
// checks run off the UI loop; each reports back as a depCheckMsg.
func (a *App) flushDepWatch() tea.Cmd {
	changes := a.depsState().watch.Drain()
	if len(changes) == 0 || !a.depWatchEnabled() {
		return nil
	}
	cli, err := vulnetixcli.Detect()
	if err != nil {
		a.addSystem(fmt.Sprintf("deps: %d manifest change(s) not checked: the vulnetix CLI is not installed", len(changes)))
		return nil
	}
	// Snapshots taken on the UI loop: the goroutines never read App fields.
	classifier := a.classifier
	guarded := a.effectivePosture().Level(posture.ToolResultUnsafe) != posture.Ignore
	pipe := run.NewPipeline(a.cfg, a.client, a.cache)
	workdir, obs, deps := a.workdir, quietObserver{a}, a.depsState()

	cmds := make([]tea.Cmd, 0, len(changes))
	for _, ch := range changes {
		cmds = append(cmds, func() tea.Msg {
			ctx := context.Background()
			c := depwatch.Checker{
				Workdir: workdir,
				Exec:    depExec(*cli, workdir, obs),
				Plan:    deps.planTier(ctx, *cli),
			}
			if classifier != nil {
				c.Decide = func(ctx context.Context, digest string) (rolemanager.DepSentinel, error) {
					return rolemanager.DecideDepChange(ctx, classifier, digest)
				}
			}
			r := c.Check(ctx, ch)
			atts, withheld := depAttachments(ctx, r, guarded, pipe)
			return depCheckMsg{report: r, atts: atts, withheld: withheld}
		})
	}
	return tea.Batch(cmds...)
}

// planTier resolves the Vulnetix plan once per session.
func (d *depState) planTier(ctx context.Context, cli vulnetixcli.CLI) vulnetixcli.Plan {
	d.planOnce.Do(func() { d.plan = cli.AuthStatus(ctx).Plan })
	return d.plan
}

// depExec runs the CLI with its hardened argv, registered quietly in the runs
// panel like the review's scanners. An exit status is a result, not an error:
// the scan's gates exit 1 when they find something.
func depExec(cli vulnetixcli.CLI, workdir string, obs quietObserver) depwatch.Exec {
	cli.Timeout = vulnetixcli.NoTimeout
	return func(ctx context.Context, label string, args ...string) (depwatch.ExecResult, error) {
		sub, cancel := context.WithCancel(ctx)
		defer cancel()
		argv := append([]string{cli.Path}, vulnetixcli.HardenedArgs(args...)...)
		sink, finish := obs.Start(label, argv, workdir, cancel)
		res, err := cli.ExecStreamIn(sub, workdir, sink, args...)
		finish(res.ExitCode, res.TimedOut, err)
		if err != nil && res.ExitCode > 0 {
			err = nil // the gates' verdict, read from ExitCode
		}
		return depwatch.ExecResult{Stdout: res.Stdout, ExitCode: res.ExitCode}, err
	}
}

// depAttachments turns a report's CLI output into attachments for the
// background agent. The output carries third-party advisory text and
// repository paths, so it is sanitized always and classified whenever the
// guardrails gate is on, like every other remote result.
func depAttachments(ctx context.Context, r depwatch.Report, guarded bool, pipe *rolemanager.Pipeline) (atts []run.Attachment, withheld []string) {
	add := func(label, raw string) {
		if strings.TrimSpace(raw) == "" {
			return
		}
		body := sanitize.Sanitize(raw)
		if guarded {
			dec, err := pipe.Process(ctx, tools.Result{Kind: tools.KindRemote, Content: raw})
			if err != nil || dec.Action != rolemanager.ActionProceed {
				withheld = append(withheld, fmt.Sprintf("%s (%s)", label, dec.Sentinel.Label()))
				return
			}
			body = dec.Content
		}
		atts = append(atts, run.Attachment{Kind: "file", Label: label, Body: body})
	}
	add("vulnetix sca "+r.Change.Path, r.SCA)
	add("vulnetix findings "+r.Change.Path, strings.Join(r.Findings, "\n"))
	add("vulnetix fix --dry-run "+r.Change.Path, r.FixPlan)
	return atts, withheld
}

// handleDepCheck reports a finished check and, when it found something,
// hands it to the ecosystem's background agent. A clean or skipped check is
// one quiet line: a hook that interrupts on every manifest edit gets
// switched off.
func (a *App) handleDepCheck(m depCheckMsg) tea.Cmd {
	r := m.report
	path := r.Change.Path
	switch {
	case r.Skipped:
		a.addSystem("deps: " + path + " changed no dependency")
		return nil
	case r.Err != nil:
		a.addSystem("deps: " + path + " check failed: " + auditLine(r.Err.Error()))
		return nil
	case r.Clean():
		a.addSystem("deps: " + path + " checked, nothing found")
		return nil
	}
	for _, w := range m.withheld {
		a.addSystem("deps: " + w + " withheld")
	}
	if len(m.atts) == 0 {
		a.addSystem("deps: " + path + " has findings, but every report was withheld")
		return nil
	}
	profileName := depwatch.ProfileFor(r.Change.Info.Ecosystem)
	if a.bgManager == nil {
		a.addSystem(fmt.Sprintf("deps: %s has findings (%d); no background agents to triage them", path, len(r.Findings)))
		return nil
	}
	profile, err := agentprofile.Load(profileName)
	if err != nil {
		a.addSystem("deps: " + err.Error())
		return nil
	}
	a.depsState().seq++
	key := fmt.Sprintf("%s@%s#%d", profileName, path, a.deps.seq)
	task := bgagent.Task{Prompt: depwatch.TaskPrompt(r), Attachments: m.atts}
	if err := a.bgManager.StartTask(a.workdir, key, profile, task); err != nil {
		a.addSystem("deps: " + err.Error())
		return nil
	}
	if a.deps.agents == nil {
		a.deps.agents = map[string]string{}
	}
	a.deps.agents[key] = path
	a.registerAgentActivity(key, a.workdir)
	return a.noteAgentStarted(key)
}

// depReportMsg carries a dependency agent's admitted report (or the verdict
// that withheld it) back to the UI loop.
type depReportMsg struct {
	path, body string
	withheld   rolemanager.Sentinel
}

// depAgentFinished routes a dependency agent's report back to the main
// session, like any finished activity. The report is model output, so it is
// classified under the guardrails gate, off the UI loop, and it is sent when
// the session is idle.
func (a *App) depAgentFinished(name string) tea.Cmd {
	path, ok := a.depsState().agents[name]
	if !ok || a.bgManager == nil {
		return nil
	}
	delete(a.deps.agents, name)
	inst, ok := a.bgManager.Lookup(name)
	if !ok {
		return nil
	}
	raw := strings.TrimSpace(inst.LastOutput())
	if raw == "" {
		return nil
	}
	guarded := a.effectivePosture().Level(posture.ToolResultUnsafe) != posture.Ignore
	pipe := run.NewPipeline(a.cfg, a.client, a.cache)
	return func() tea.Msg {
		if !guarded {
			return depReportMsg{path: path, body: sanitize.Sanitize(raw)}
		}
		dec, err := pipe.Process(context.Background(), tools.Result{Kind: tools.KindProcess, Content: raw})
		if err != nil || dec.Action != rolemanager.ActionProceed {
			return depReportMsg{path: path, withheld: dec.Sentinel}
		}
		return depReportMsg{path: path, body: dec.Content}
	}
}

// handleDepReport queues an admitted report for the main session.
func (a *App) handleDepReport(m depReportMsg) tea.Cmd {
	if m.body == "" {
		a.addSystem(fmt.Sprintf("deps: report for %s classified: %s", m.path, m.withheld.Label()))
		return nil
	}
	label := "dependency check " + m.path
	atts := []run.Attachment{{Kind: "file", Label: label, Body: m.body}}
	a.pendingActivitySends = append(a.pendingActivitySends, activitySend{label: label, atts: atts})
	return a.flushPendingActivitySends()
}

// depsState returns the hook state, creating it for an App built without New.
func (a *App) depsState() *depState {
	if a.deps == nil {
		a.deps = &depState{}
	}
	return a.deps
}
