package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/bgagent"
	"github.com/vulnetix/belai/internal/commands"
	"github.com/vulnetix/belai/internal/explore"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/scanartifacts"
	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/tui/components"
	"github.com/vulnetix/belai/internal/vulnetixcli"
)

// reviewScannerProfile is the read-only background agent a /vulnetix review
// starts for each scanner as soon as that scanner finishes, so its grounded
// report reaches the main thread while the slower scanners still run.
const reviewScannerProfile = "belai:vulnetix-scanner"

// reviewRun is one /vulnetix review in flight. It lives on the UI loop only:
// the scanner goroutines hand their outcomes over through events.
type reviewRun struct {
	seq     int
	started time.Time
	autoFix bool
	// names are the review's activities in table order (scanners, then fix);
	// done marks the finished ones.
	names []string
	done  map[string]bool
	// agents maps a running scanner agent's key to its scanner.
	agents map[string]string
	// reports and atts are the admitted scanner blocks, findings the admitted
	// scanner agent reports; all of it goes to the triage turn.
	reports   []explore.ReviewReport
	atts      []run.Attachment
	findings  []explore.ReviewFinding
	total     int
	scansDone bool
	waitNoted bool
	events    chan tea.Msg
	// cancel stops the scans; cancelled marks a review the user stopped with
	// esc, which ends without a triage turn.
	cancel    context.CancelFunc
	cancelled bool
	// steer is what the user typed into the composer while the review ran.
	// It is the user's own direction, so it rides on the triage prompt.
	steer []string
}

// reviewScanMsg is one finished scanner with its blocks already sanitized
// and, under guardrails, classified off the UI loop.
type reviewScanMsg struct {
	outcome  commands.ScanOutcome
	reports  []explore.ReviewReport
	atts     []run.Attachment
	withheld []string
}

// reviewScansDoneMsg closes a review's event stream: every scanner and the
// post-scan fix have finished and the summary and manifest are written.
type reviewScansDoneMsg struct {
	report commands.Report
	err    error
}

// reviewReportMsg is a scanner agent's admitted report (or the verdict that
// withheld it, or neither when the agent produced nothing).
type reviewReportMsg struct {
	key, scanner, body string
	withheld           rolemanager.Sentinel
}

// startReview launches a /vulnetix review on the UI loop. The scanners run in
// the background and report one by one; each finished scanner gets a card in
// the main thread and, when it found something, its own scanner agent.
func (a *App) startReview() tea.Cmd {
	if a.review != nil {
		a.addSystem("a vulnetix review is already running · f9 for output")
		return nil
	}
	cli, err := vulnetixcli.Detect()
	if err != nil {
		a.addSystem("vulnetix failed: " + err.Error())
		return nil
	}
	autoFix := false
	if a.settings.Vulnetix != nil {
		autoFix = a.settings.Vulnetix.AutoFixEnabled()
	}
	// The scan rows are shown in the runs panel, but their stdout is not
	// round-tripped: the triage turn carries structured report attachments
	// instead of nine pretty-printed terminal tables.
	v := commands.Vulnetix{CLI: cli, Workdir: a.workdir, Observer: quietObserver{a}, AutoFix: autoFix}
	names, err := v.ActivityNames()
	if err != nil {
		a.addSystem("vulnetix failed: " + err.Error())
		return nil
	}
	// Snapshots taken on the UI loop: the scanner goroutines never read App
	// fields. The gate level is decided here, before any classifier call.
	guarded := a.effectivePosture().Level(posture.ToolResultUnsafe) != posture.Ignore
	pipe := run.NewPipeline(a.cfg, a.client, a.cache)
	events := make(chan tea.Msg, len(names)+1)
	v.OnScanDone = func(o commands.ScanOutcome) {
		reports, atts, withheld := classifyTriageBlocks(context.Background(), o.Blocks, guarded, pipe)
		for i := range reports {
			// Key every report by the scanner that produced it, so the
			// triage turn can tell which scanners' agents already ran.
			reports[i].Scanner = o.Name
		}
		events <- reviewScanMsg{outcome: o, reports: reports, atts: atts, withheld: withheld}
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.reviewSeq++
	a.review = &reviewRun{
		cancel:  cancel,
		seq:     a.reviewSeq,
		started: time.Now(),
		autoFix: autoFix,
		names:   names,
		done:    map[string]bool{},
		agents:  map[string]string{},
		events:  events,
	}
	a.addSystem(fmt.Sprintf("▸ vulnetix review started · %d scanners + fix · f9 for output", len(names)-1))
	a.refreshFooter()
	scan := func() tea.Msg {
		rep, err := v.Run(ctx)
		events <- reviewScansDoneMsg{report: rep, err: err}
		return nil
	}
	// The composer shows the review as work in progress, so its spinner
	// runs; a turn already in flight keeps the one it started.
	var spin tea.Cmd
	if a.phase == phaseIdle {
		spin = a.workSpin.Tick
	}
	return tea.Batch(scan, watchReviewEvents(events), a.armAgentPulse(), spin)
}

// watchReviewEvents delivers a review's next event. Every scanner outcome and
// the closing done message travel on the one channel, so they arrive in the
// order they happened.
func watchReviewEvents(events chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-events }
}

// classifyTriageBlocks turns report blocks into triage attachments and
// subagent reports. Report bodies are repository-derived bytes (SARIF carries
// code snippets and paths), so each one is sanitized always and classified
// whenever the guardrails gate is on — the same round trip a Read result
// takes, and the same fail-closed rule as AGENTS.md.
func classifyTriageBlocks(ctx context.Context, blocks []commands.TriageBlock, guarded bool, pipe *rolemanager.Pipeline) (reports []explore.ReviewReport, atts []run.Attachment, withheld []string) {
	for _, block := range blocks {
		raw := block.Body
		body := sanitize.Sanitize(raw)
		if guarded {
			dec, err := pipe.Process(ctx, tools.Result{Kind: tools.KindRead, Content: raw})
			if err != nil || dec.Action != rolemanager.ActionProceed {
				withheld = append(withheld, fmt.Sprintf("%s (%s)", block.Label, dec.Sentinel.Label()))
				continue
			}
			body = dec.Content
		}
		atts = append(atts, run.Attachment{Kind: "file", Label: block.Label, Body: body})
		reports = append(reports, explore.ReviewReport{Scanner: block.Scanner, Label: block.Label, Body: body})
	}
	return reports, atts, withheld
}

// handleReviewScan shows one finished scanner in the main thread and starts
// its scanner agent when it found something.
func (a *App) handleReviewScan(m reviewScanMsg) tea.Cmd {
	r := a.review
	if r == nil {
		return nil
	}
	o := m.outcome
	r.done[o.Name] = true
	next := watchReviewEvents(r.events)
	if r.cancelled {
		// A scanner killed by the cancel: no card, no agent. The stream is
		// still drained so the closing done message arrives.
		return next
	}
	if o.Name == "fix" {
		a.addSystem(reviewFixLine(o, r.autoFix))
		a.refreshFooter()
		return next
	}
	r.reports = append(r.reports, m.reports...)
	r.atts = append(r.atts, m.atts...)
	card := cardFor(o)
	r.total += card.issues

	var agentCmd tea.Cmd
	key := ""
	if len(m.reports) > 0 {
		key, agentCmd = a.startScannerAgent(r, o.Name, m.reports)
	}
	status := reviewScanStatus(o)
	if status == "" && card.attention {
		status = components.ReportAttention
	}
	a.messages = append(a.messages, components.Message{
		Role:     components.ReportRole,
		Content:  reviewScanCard(o, card, m.withheld, key),
		ToolName: "vulnetix " + o.Name,
		ToolArgs: compactDuration(o.Duration),
		Status:   status,
	})
	a.refreshFooter()
	return tea.Batch(next, agentCmd)
}

// startScannerAgent starts the read-only background agent that grounds one
// scanner's findings in the repository. Its task is the same one the triage
// turn's per-scanner subagent gets, so the report contract is unchanged. It
// returns the agent's key, or "" when none started (the triage turn then runs
// that scanner's subagent itself).
func (a *App) startScannerAgent(r *reviewRun, scanner string, reports []explore.ReviewReport) (string, tea.Cmd) {
	if a.bgManager == nil {
		return "", nil
	}
	profile, err := agentprofile.Load(reviewScannerProfile)
	if err != nil {
		a.addSystem("vulnetix review: " + err.Error())
		return "", nil
	}
	merged := explore.ReviewReport{Scanner: scanner, Label: reports[0].Label}
	var bodies []string
	for _, rep := range reports {
		bodies = append(bodies, rep.Body)
	}
	if len(reports) > 1 {
		merged.Label = scanner + " report"
	}
	merged.Body = strings.Join(bodies, "\n\n")
	tasks := explore.PlanReview(reviewPrompt, []explore.ReviewReport{merged})
	if len(tasks) == 0 {
		return "", nil
	}
	t := tasks[0]
	key := fmt.Sprintf("%s@%s#%d", reviewScannerProfile, scanner, r.seq)
	task := bgagent.Task{Prompt: t.Prompt, Attachments: []run.Attachment{{Kind: "file", Label: t.EvidenceLabel, Body: t.Evidence}}}
	if err := a.bgManager.StartTask(a.workdir, key, profile, task); err != nil {
		a.addSystem("vulnetix review: " + err.Error())
		return "", nil
	}
	r.agents[key] = scanner
	a.registerAgentActivity(key, a.workdir)
	return key, a.noteAgentStarted(key)
}

// reviewAgentFinished routes a scanner agent's report back to the review. The
// report is model output, so it is capped, sanitized and, under the
// guardrails gate, classified off the UI loop, like a dependency agent's.
func (a *App) reviewAgentFinished(name string) tea.Cmd {
	r := a.review
	if r == nil {
		return nil
	}
	scanner, ok := r.agents[name]
	if !ok {
		return nil
	}
	raw := ""
	if a.bgManager != nil {
		if inst, ok := a.bgManager.Lookup(name); ok {
			raw = strings.TrimSpace(inst.LastOutput())
		}
	}
	raw = capReviewReport(raw)
	if raw == "" {
		return func() tea.Msg { return reviewReportMsg{key: name, scanner: scanner} }
	}
	guarded := a.effectivePosture().Level(posture.ToolResultUnsafe) != posture.Ignore
	pipe := run.NewPipeline(a.cfg, a.client, a.cache)
	return func() tea.Msg {
		if !guarded {
			return reviewReportMsg{key: name, scanner: scanner, body: sanitize.Sanitize(raw)}
		}
		dec, err := pipe.Process(context.Background(), tools.Result{Kind: tools.KindProcess, Content: raw})
		if err != nil || dec.Action != rolemanager.ActionProceed {
			return reviewReportMsg{key: name, scanner: scanner, withheld: dec.Sentinel}
		}
		return reviewReportMsg{key: name, scanner: scanner, body: dec.Content}
	}
}

// capReviewReport bounds a scanner agent's report at a line boundary, like
// the triage turn's own subagents.
func capReviewReport(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= explore.ReviewReportBytes {
		return s
	}
	cut := s[:explore.ReviewReportBytes]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i]
	}
	return cut + "\n… (report truncated)"
}

// handleReviewReport shows a scanner agent's report in the main thread and
// keeps it for the triage turn.
func (a *App) handleReviewReport(m reviewReportMsg) tea.Cmd {
	r := a.review
	if r == nil {
		return nil
	}
	if _, ok := r.agents[m.key]; !ok {
		return nil // an agent the user stopped with the review
	}
	delete(r.agents, m.key)
	switch {
	case m.body != "":
		a.messages = append(a.messages, components.Message{
			Role:     components.ReportRole,
			Content:  m.body,
			ToolName: "vulnetix " + m.scanner + " review",
			ToolArgs: "f8 for the agent's work",
		})
		r.findings = append(r.findings, explore.ReviewFinding{Scanner: m.scanner, Label: m.scanner + " review", Body: m.body})
	case m.withheld != "":
		a.addSystem(fmt.Sprintf("vulnetix %s review classified: %s", m.scanner, m.withheld.Label()))
		// The classifier refused the report: the triage turn must not run
		// the same review again to get round it.
		r.findings = append(r.findings, explore.ReviewFinding{Scanner: m.scanner})
	default:
		// No output: the triage turn runs this scanner's subagent itself.
		a.addSystem(fmt.Sprintf("vulnetix %s review produced no report; triage will review it", m.scanner))
	}
	a.refreshFooter()
	return a.maybeSendReview()
}

// handleReviewScansDone records that every scan finished, shows the summary,
// and starts the triage turn once no scanner agent is still running.
func (a *App) handleReviewScansDone(m reviewScansDoneMsg) tea.Cmd {
	r := a.review
	if r == nil {
		return nil
	}
	r.scansDone = true
	var view tea.Cmd
	if r.cancelled {
		// The scans were killed: their summary and artifacts are partial.
	} else if m.err != nil {
		a.addSystem("vulnetix failed: " + m.err.Error())
	} else {
		if m.report.Summary != "" {
			a.addSystem("vulnetix:\n" + m.report.Summary)
		}
		view = a.push(viewVulnetixArtifacts)
	}
	return tea.Batch(view, a.maybeSendReview())
}

// maybeSendReview starts the triage turn when the scans are done and every
// scanner agent has reported, queueing it behind an in-flight turn.
func (a *App) maybeSendReview() tea.Cmd {
	r := a.review
	if r == nil || !r.scansDone {
		return nil
	}
	if len(r.agents) > 0 {
		if !r.waitNoted {
			r.waitNoted = true
			var names []string
			for _, s := range r.agents {
				names = append(names, s)
			}
			sort.Strings(names)
			a.addSystem("vulnetix review: scans done · waiting on the " + strings.Join(names, ", ") + " review")
		}
		return nil
	}
	a.review = nil
	a.refreshFooter()
	if r.cancelled {
		return nil
	}
	elapsed := compactDuration(time.Since(r.started))
	if len(r.atts) == 0 {
		a.addSystem("■ vulnetix review done · nothing to triage · " + elapsed)
		return nil
	}
	a.addSystem(fmt.Sprintf("■ vulnetix review done · %s · %s · triage starting", nounCount(r.total, "issue", "issues"), elapsed))
	rs := &reviewSend{atts: r.atts, reports: r.reports, findings: r.findings, steer: r.steer}
	if a.working() || a.preSend {
		a.pendingReview = rs
		return nil
	}
	return a.sendReview(rs)
}

// reviewActive reports whether a review is running that the user has not
// cancelled. It drives the composer's working state, the footer line and the
// spinner.
func (a *App) reviewActive() bool {
	return a.review != nil && !a.review.cancelled
}

// reviewSteerable reports whether enter in the composer steers the review:
// a review is running and no turn is in flight, which would take the steer
// itself.
func (a *App) reviewSteerable() bool {
	return a.reviewActive() && !a.working() && !a.preSend
}

// steerReview records the user's direction for the running review. The scans
// and scanner agents cannot be redirected mid-run, so it rides on the triage
// turn's prompt as the user's own words.
func (a *App) steerReview(input string) tea.Cmd {
	a.messages = append(a.messages, components.Message{Role: "user", Content: input, Steering: true})
	a.review.steer = append(a.review.steer, input)
	a.editor.Reset()
	a.clearLoadedPrompt()
	a.clearAutocomplete()
	a.addSystem("vulnetix review: noted for the triage turn")
	return nil
}

// cancelReview stops the running review: the scans are killed, the scanner
// agents stopped, and no triage turn runs. The stream still drains, so the
// review clears once the scans have exited.
func (a *App) cancelReview() {
	r := a.review
	if r == nil || r.cancelled {
		return
	}
	r.cancelled = true
	if r.cancel != nil {
		r.cancel()
	}
	keys := make([]string, 0, len(r.agents))
	for key := range r.agents {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if a.bgManager != nil && a.bgManager.Stop(key) == nil {
			a.noteAgentStopped(key)
		}
		delete(r.agents, key)
	}
	a.addSystem("vulnetix review cancelled")
	a.refreshFooter()
}

// reviewComposerTitle is the composer's working title while a review runs:
// the spinner, a review pill and the progress. ok is false when no review is
// steerable.
func (a *App) reviewComposerTitle() (title string, ok bool) {
	if !a.reviewSteerable() {
		return "", false
	}
	p := a.reviewProgress()
	progress := fmt.Sprintf("%d/%d", p.Done, p.Total)
	if len(p.Pending) > 0 {
		shown := p.Pending
		if len(shown) > 2 {
			shown = shown[:2]
		}
		progress += " · " + strings.Join(shown, ", ")
		if n := len(p.Pending) - len(shown); n > 0 {
			progress += fmt.Sprintf(" +%d", n)
		}
	}
	if p.Agents > 0 {
		progress += " · " + countOf(p.Agents, "agent")
	}
	progress += " · " + p.Elapsed
	pill := components.Chip("vulnetix review", components.ColorTeal)
	return a.spinMark() + " " + pill + " " + components.MutedStyle.Render(progress), true
}

// reviewProgress is the footer's view of the running review.
func (a *App) reviewProgress() *components.ReviewProgress {
	r := a.review
	if r == nil || r.cancelled {
		return nil
	}
	p := &components.ReviewProgress{
		Glyph:   a.pulseGlyph("running"),
		Done:    len(r.done),
		Total:   len(r.names),
		Agents:  len(r.agents),
		Elapsed: compactDuration(time.Since(r.started)),
	}
	for _, n := range r.names {
		if !r.done[n] {
			p.Pending = append(p.Pending, n)
		}
	}
	return p
}

// reviewScanStatus is "failed" for a scanner that did not produce a verdict.
// Exit status 1 is a gate that found something, not a failure.
func reviewScanStatus(o commands.ScanOutcome) string {
	if o.TimedOut || (o.Err != nil && o.ExitCode != 1) {
		return components.ReportFailed
	}
	return ""
}

// reviewScanCard renders a finished scanner's card. Every line is a harness
// observation — status, counts, the artifact path, the agent's key — and no
// finding text, which stays on the classified triage path.
func reviewScanCard(o commands.ScanOutcome, card reviewCard, withheld []string, agentKey string) string {
	var lines []string
	switch {
	case o.TimedOut:
		lines = append(lines, "**timed out**")
	case reviewScanStatus(o) == components.ReportFailed:
		line := fmt.Sprintf("**failed** · exit %d", o.ExitCode)
		if o.Err != nil {
			line += " · " + auditLine(o.Err.Error())
		}
		lines = append(lines, line)
	}
	lines = append(lines, card.headline)
	lines = append(lines, card.lines...)
	if o.Artifact != "" {
		lines = append(lines, "`.vulnetix/"+o.Artifact+"`")
	}
	for _, w := range withheld {
		lines = append(lines, "withheld from the model: "+w)
	}
	if agentKey != "" {
		lines = append(lines, "reviewing in the background as `"+agentKey+"` · f8 to follow")
	}
	return "- " + strings.Join(lines, "\n- ")
}

// severityBreakdown lists the non-zero severity buckets, most severe first.
func severityBreakdown(c scanartifacts.Counts) string {
	var parts []string
	for _, b := range []struct {
		n    int
		name string
	}{{c.Critical, "critical"}, {c.High, "high"}, {c.Medium, "medium"}, {c.Low, "low"}, {c.Info, "info"}} {
		if b.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", b.n, b.name))
		}
	}
	return strings.Join(parts, " · ")
}

// reviewFixLine is the post-scan fix's one line in the main thread.
func reviewFixLine(o commands.ScanOutcome, autoFix bool) string {
	label := "vulnetix fix --dry-run"
	if autoFix {
		label = "vulnetix fix --yes"
	}
	switch {
	case o.TimedOut:
		return "■ " + label + " timed out"
	case o.Err != nil && o.ExitCode != 1:
		return fmt.Sprintf("■ %s failed (exit %d)", label, o.ExitCode)
	}
	return "■ " + label + " done · " + compactDuration(o.Duration)
}
