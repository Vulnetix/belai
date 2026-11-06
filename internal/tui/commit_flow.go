package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/attribution"
	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/gitsync"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/locate"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/sessionmeasure"
	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/tui/components"
)

// This file is the commit workflow (docs/commit-workflow.md): the harness
// decides which paths are the session's, which are not, and when to commit;
// the model, when commit_draft is on, supplies only the words of the message.
// Auto-commit after a completed goal, the ask that precedes it in ask mode and
// the /commit command all go through runCommitPlan, so there is one path from
// "paths" to "git commit".

// Audit outcomes of the commit workflow. Closed set: pinned by a test.
const (
	commitOutcomeAsk     = "commit_ask"
	commitOutcomeAuto    = "auto_commit"
	commitOutcomeRefused = "refused"
)

// commitModeManual is the mode word of a /commit run in the records.
const commitModeManual = "manual"

// commitBase is the tree as it stood when a turn began, read off the UI loop.
// The goroutine writes the fields and then closes done; a reader checks done
// first, so no lock is needed.
type commitBase struct {
	done  chan struct{}
	head  string
	dirty []string
	err   error
}

// commitTurn is the end of one turn as the commit workflow sees it.
type commitTurn struct {
	// paths are the files the turn's tools reported as changed.
	paths []string
	// before is the dirty set that counts as "already there" for this turn.
	before []string
	// head is HEAD at the turn's start.
	head string
	// tracked is false when the workflow was off for the whole turn.
	tracked bool
	// err is why the tree could not be read at the turn's start.
	err error
}

// commitInputs is everything one commit decision needs, copied off the UI loop.
type commitInputs struct {
	mode      string
	workdir   string
	runner    forge.Runner
	before    []string
	baseHead  string
	checkHead bool
	baseErr   error
	touched   []string
	opts      forge.SplitOptions
	objective string
	userMsg   string
	// askFirst puts the question to a person before a /commit commits.
	askFirst  bool
	sessionID string
	draft     func(ctx context.Context, objective string, paths []string, stat string) (string, error)
}

// commitPlan is a decision a person may still answer.
type commitPlan struct {
	in    commitInputs
	split forge.Split
	head  string
	msg   string
}

// commitAskState is the open question, if any.
type commitAskState struct {
	seq     int
	plan    *commitPlan
	working bool
	err     string
}

// commitWithhold is the part of the withhold rules forge cannot import: Git's
// own files, Belai's state and the scanner's evidence, and credential stores.
func commitWithhold(rel string) bool {
	if agentprofile.ProtectedWrite(rel) {
		return true
	}
	why, ok := locate.EligibleFile(rel, 0)
	return !ok && why == locate.SkipSensitive
}

func (a *App) commitSplitOptions() forge.SplitOptions {
	return forge.SplitOptions{Exclude: a.settings.CommitExcludeGlobs(), Withhold: commitWithhold, Root: a.workdir}
}

// beginCommitTurn starts reading the tree for the turn about to run. It is a
// no-op in commit_mode off, so the default costs nothing. The read happens off
// the UI loop; a path the model edits before it finishes only reads as dirty
// before the turn, which withholds it, so the race can only err toward leaving
// a file out.
func (a *App) beginCommitTurn() {
	a.commitBase = nil
	if a.commitAsk.plan != nil {
		a.closeCommitAsk("commit: a new turn began, so the question was dropped and nothing was committed")
	}
	if a.settings.CommitModeEffective() == config.CommitModeOff {
		return
	}
	b := &commitBase{done: make(chan struct{})}
	a.commitBase = b
	runner, dir := a.forgeRun(), a.workdir
	go func() {
		defer close(b.done)
		ctx, cancel := context.WithTimeout(context.Background(), forge.DirtyTimeout+forge.ReadTimeout)
		defer cancel()
		d, err := forge.DirtyPaths(ctx, runner, dir)
		if err != nil {
			b.err = err
			return
		}
		b.dirty = d
		b.head = forge.HeadCommit(ctx, runner, dir)
	}()
}

// endCommitTurn closes the turn for the commit workflow: it takes the turn's
// paths, settles the session ledger with them and clears the per-turn state.
// It never waits for the tree read; one that has not finished makes the turn's
// paths mixed, which is the safe answer.
func (a *App) endCommitTurn() commitTurn {
	t := commitTurn{paths: append([]string(nil), a.taskPaths...)}
	a.resetTaskPaths()
	b := a.commitBase
	a.commitBase = nil
	if b == nil {
		return t
	}
	t.tracked = true
	select {
	case <-b.done:
	default:
		t.err = errors.New("the tree had not been read when the turn ended")
		a.commitLedger.Settle(nil, t.paths, false)
		return t
	}
	if b.err != nil {
		t.err = b.err
		a.commitLedger.Settle(nil, t.paths, false)
		return t
	}
	t.before = a.commitLedger.Before(b.dirty)
	t.head = b.head
	a.commitLedger.Settle(t.before, t.paths, true)
	return t
}

// resetCommitLedger forgets the session's tracked paths, as a new or resumed
// session does.
func (a *App) resetCommitLedger() {
	a.commitBase = nil
	a.commitLedger.Reset()
	if a.commitAsk.plan != nil {
		a.closeCommitAsk("")
	}
}

// commitInputs copies the settings and session state a decision needs.
func (a *App) newCommitInputs(mode string) commitInputs {
	in := commitInputs{
		mode:      mode,
		workdir:   a.workdir,
		runner:    a.forgeRun(),
		opts:      a.commitSplitOptions(),
		sessionID: a.sessionID,
	}
	if a.lastGoal != nil {
		in.objective = a.lastGoal.Objective
	}
	if a.settings.CommitDraftEnabled() && a.classifier != nil {
		in.draft = a.commitDrafter()
	}
	return in
}

// flushAutoCommit starts one commit decision when the finished turn completed
// a goal with at least one changed file and commit_mode is ask or auto. It
// snapshots every input on the UI loop and runs off it as a tea.Cmd. The path
// set is reset before the command is returned, so a failure cannot leak one
// goal's files into the next.
func (a *App) flushAutoCommit(res run.Result) tea.Cmd {
	turn := a.endCommitTurn()
	mode := a.settings.CommitModeEffective()
	if mode == config.CommitModeOff {
		return nil
	}
	if res.GoalSentinel != rolemanager.GoalComplete || len(turn.paths) == 0 {
		return nil
	}
	if a.lastGoal == nil || strings.TrimSpace(a.lastGoal.Objective) == "" {
		return nil
	}
	in := a.newCommitInputs(mode)
	in.before, in.baseHead, in.checkHead, in.baseErr = turn.before, turn.head, true, turn.err
	in.touched = turn.paths
	if !turn.tracked && turn.err == nil {
		// The mode was switched on during the turn: no start snapshot exists.
		in.baseErr = errors.New("the commit workflow was switched on during the turn")
	}
	return func() tea.Msg { return runCommitPlan(context.Background(), in) }
}

// commitCommand is /commit [message]: commit what the session changed from a
// clean state, on request. In ask mode the same question as after a goal is
// shown first.
func (a *App) commitCommand(arg string) tea.Cmd {
	mode := a.settings.CommitModeEffective()
	switch {
	case mode == config.CommitModeOff:
		a.addSystem("commit: commit_mode is off; set it to ask or auto in /settings (commit mode) first")
		return nil
	case a.working() || a.preSend:
		a.addSystem("commit: wait for the running turn to finish")
		return nil
	case a.commitAsk.plan != nil || a.commitBusy:
		a.addSystem("commit: a commit is already in progress")
		return nil
	}
	msg := strings.TrimSpace(arg)
	if msg != "" {
		clean, err := cleanUserCommitMessage(msg)
		if err != nil {
			a.addSystem("commit: " + err.Error())
			return nil
		}
		msg = clean
	}
	in := a.newCommitInputs(mode)
	in.userMsg = msg
	in.touched = a.commitLedger.Touched()
	in.before = a.commitLedger.Mixed()
	in.mode = commitModeManual
	in.draft = nil
	if msg == "" && a.settings.CommitDraftEnabled() && a.classifier != nil {
		in.draft = a.commitDrafter()
	}
	in.askFirst = mode == config.CommitModeAsk
	a.commitBusy = true
	return func() tea.Msg { return runCommitPlan(context.Background(), in) }
}

// cleanUserCommitMessage sanitises a typed message and requires it to be a
// conventional commit.
func cleanUserCommitMessage(raw string) (string, error) {
	header, body, _ := strings.Cut(sanitize.Text(raw), "\n")
	msg := sanitize.Line(header, 0)
	if b := strings.TrimSpace(body); b != "" {
		msg += "\n\n" + b
	}
	msg = forge.StripTrailers(msg)
	if err := forge.ValidateConventional(msg); err != nil {
		return "", fmt.Errorf("the message is not a conventional commit (%s)", err)
	}
	return msg, nil
}

// runCommitPlan decides, off the UI loop, what to commit and with which
// words. It makes the commit itself unless a person must answer first. Every
// failure is a refusal that leaves the tree and the index as they were.
func runCommitPlan(ctx context.Context, in commitInputs) autoCommitMsg {
	m := autoCommitMsg{mode: in.mode}
	refuse := func(format string, args ...any) autoCommitMsg {
		m.refused = fmt.Sprintf(format, args...)
		return m
	}
	if in.baseErr != nil {
		return refuse("could not read the tree before the turn (%s)", forge.CleanErr(in.baseErr))
	}
	m.head = forge.HeadCommit(ctx, in.runner, in.workdir)
	if in.checkHead && m.head != in.baseHead {
		return refuse("HEAD moved since the turn began")
	}
	if op, err := operationUnderway(ctx, in.runner, in.workdir); err != nil {
		return refuse("could not read the repository state (%s)", forge.CleanErr(err))
	} else if op != "" {
		return refuse("%s is in progress", op)
	}
	now, err := forge.DirtyPaths(ctx, in.runner, in.workdir)
	if err != nil {
		return refuse("could not read the tree (%s)", forge.CleanErr(err))
	}
	m.now, m.nowKnown = now, true
	split := forge.SplitDirty(in.before, now, in.touched, in.opts)
	m.split = split
	if in.mode == config.CommitModeAuto && len(split.Mixed) > 0 {
		return refuse("%s edited before the turn and again in it, so their changes cannot be told apart", pathCount(split.Mixed))
	}
	if len(split.Task) == 0 {
		m.note = "nothing to commit: the session changed no file from a clean state" + leftLine(split)
		return m
	}
	plan := &commitPlan{in: in, split: split, head: m.head}
	plan.msg, m.draftNote = buildCommitMessage(ctx, in, split.Task)
	m.plan = plan
	m.msg = plan.msg
	if in.mode == config.CommitModeAsk || in.askFirst {
		m.ask = plan
		return m
	}
	return applyCommitPlan(ctx, plan, m)
}

// operationUnderway names a rebase, merge, cherry-pick, revert or bisect in
// progress in dir, or "".
func operationUnderway(ctx context.Context, r forge.Runner, dir string) (string, error) {
	out, err := r(ctx, dir, "git", "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	gitDir := strings.TrimSpace(string(out))
	if gitDir == "" {
		return "", errors.New("no git directory")
	}
	return gitsync.InProgress(gitDir), nil
}

// buildCommitMessage returns the message and, when a model draft was wanted
// but not used, a short harness-worded note.
func buildCommitMessage(ctx context.Context, in commitInputs, task []string) (msg, note string) {
	if in.userMsg != "" {
		return attribution.Message(in.userMsg, in.sessionID), ""
	}
	fallback := func() string {
		if strings.TrimSpace(in.objective) != "" {
			return forge.TaskCommitMessage(sanitize.Line(in.objective, 200), task)
		}
		return forge.PathsCommitMessage(task)
	}
	if in.draft != nil {
		stat := forge.DiffStat(ctx, in.runner, in.workdir, task)
		drafted, err := in.draft(ctx, sanitize.Line(in.objective, 300), task, stat)
		if err == nil {
			return attribution.Message(drafted, in.sessionID), ""
		}
		note = "the drafted message was not used (" + sanitize.Line(err.Error(), 120) + ")"
	}
	return attribution.Message(fallback(), in.sessionID), note
}

// applyCommitPlan makes the commit for plan and fills m with the result.
func applyCommitPlan(ctx context.Context, plan *commitPlan, m autoCommitMsg) autoCommitMsg {
	in := plan.in
	if head := forge.HeadCommit(ctx, in.runner, in.workdir); head != plan.head {
		m.refused = "HEAD moved while the commit was being prepared"
		return m
	}
	sha, err := forge.CommitPaths(ctx, in.runner, in.workdir, plan.split.Task, plan.msg)
	m.sha, m.err, m.msg, m.plan, m.split = sha, err, plan.msg, plan, plan.split
	if err != nil || sha == "" {
		return m
	}
	m.full = forge.HeadCommit(ctx, in.runner, in.workdir)
	if c, cerr := sessionmeasure.ReadHead(ctx, in.runner, in.workdir); cerr == nil {
		m.commit = &c
	}
	return m
}

// commitDrafter returns the model step: classify the facts, draft the words,
// check the result and classify it again. Every input is read on the UI loop.
func (a *App) commitDrafter() func(context.Context, string, []string, string) (string, error) {
	c := a.classifier
	caveman := a.settings.ClassifierCavemanEnabled()
	admit := a.summaryAdmitter()
	nonce := nonceHex()
	return func(ctx context.Context, objective string, paths []string, stat string) (string, error) {
		facts := rolemanager.CommitFacts{Nonce: nonce, Objective: objective, Paths: paths, Stat: stat}
		// The facts are repository text, so they are admitted before a model
		// is asked to write from them, and the reply is admitted again.
		classify := tools.KindCommitDraft.NeedsClassifier()
		if classify {
			if err := admitCommitText(ctx, admit, rolemanager.CommitFactsBlock(facts)); err != nil {
				return "", fmt.Errorf("the changed paths were not admitted: %w", err)
			}
		}
		payload := rolemanager.BuildCommitDraftPayload(facts, caveman)
		raw, err := c.Classify(ctx, payload)
		if err != nil {
			return "", err
		}
		msg, err := forge.CleanDraft(raw)
		if err != nil {
			return "", err
		}
		if classify {
			if err := admitCommitText(ctx, admit, msg); err != nil {
				return "", fmt.Errorf("the draft was not admitted: %w", err)
			}
		}
		return msg, nil
	}
}

func admitCommitText(ctx context.Context, admit func(context.Context, string) (rolemanager.Decision, error), text string) error {
	dec, err := admit(ctx, text)
	var refusal *rolemanager.RefusalError
	switch {
	case errors.As(err, &refusal):
		return fmt.Errorf("refused by the security classifier (%s)", refusal.Sentinel.Label())
	case err != nil:
		return err
	case dec.Action != rolemanager.ActionProceed:
		return fmt.Errorf("not admitted (%s)", dec.Sentinel.Label())
	}
	return nil
}

// pathCount renders "1 file" or "N files" with up to three names.
func pathCount(paths []string) string {
	shown := paths
	if len(shown) > 3 {
		shown = shown[:3]
	}
	names := make([]string, len(shown))
	for i, p := range shown {
		names[i] = sanitize.Line(p, 80)
	}
	s := fmt.Sprintf("%d file", len(paths))
	if len(paths) != 1 {
		s += "s"
	}
	s += " (" + strings.Join(names, ", ")
	if len(paths) > len(shown) {
		s += ", ..."
	}
	return s + ")"
}

// leftLine says what was left alone, or "" when nothing was.
func leftLine(s forge.Split) string {
	var parts []string
	if len(s.Mixed) > 0 {
		parts = append(parts, "edited before the turn and again in it: "+pathCount(s.Mixed))
	}
	if len(s.Withheld) > 0 {
		parts = append(parts, "withheld by a rule: "+pathCount(s.Withheld))
	}
	if len(s.PreExisting) > 0 {
		parts = append(parts, "already changed before: "+pathCount(s.PreExisting))
	}
	if len(s.Unrelated) > 0 {
		parts = append(parts, "changed outside the session's tools: "+pathCount(s.Unrelated))
	}
	if len(parts) == 0 {
		return ""
	}
	return "; left alone: " + strings.Join(parts, "; ")
}

// handleAutoCommit reports the commit, the refusal or the question, and
// refreshes the git tab so its HEAD subject reflects a new commit.
func (a *App) handleAutoCommit(m autoCommitMsg) tea.Cmd {
	a.commitBusy = false
	prefix := "commit"
	if m.mode == config.CommitModeAuto {
		prefix = "auto-commit"
	}
	if m.nowKnown {
		a.commitLedger.Prune(m.now)
	}
	switch {
	case m.refused != "":
		a.addSystem(prefix + ": " + sanitize.Line(m.refused, 240) + "; nothing was committed" + leftLine(m.split))
		a.recordCommit(m, "refused")
	case m.err != nil:
		a.addSystem(prefix + ": " + forge.CleanErr(m.err))
		m.refused = "git refused the commit"
		a.recordCommit(m, "refused")
	case m.ask != nil:
		a.openCommitAsk(m.ask)
	case m.sha != "":
		a.commitLedger.Committed(m.split.Task)
		a.addSystem(fmt.Sprintf("%s %s %s (%d files)%s", commitVerb(m.mode), m.sha, firstLine(m.msg), len(m.split.Task), leftLine(m.split)))
		if m.draftNote != "" {
			a.addSystem("commit: " + m.draftNote)
		}
		if m.commit != nil {
			a.appendEntry(m.commit.Entry())
		}
		a.recordCommit(m, "committed")
	case m.note != "":
		a.addSystem("commit: " + m.note)
	}
	return a.refreshForge(true)
}

func commitVerb(mode string) string {
	if mode == config.CommitModeAuto {
		return "auto-commit"
	}
	return "commit"
}

// recordCommit writes the audit event and the per-session record. Neither
// holds the message, and a failure to write is never shown as a commit failure.
func (a *App) recordCommit(m autoCommitMsg, outcome string) {
	auditOutcome := commitOutcomeRefused
	if outcome == "committed" {
		auditOutcome = commitOutcomeAsk
		if m.mode == config.CommitModeAuto {
			auditOutcome = commitOutcomeAuto
		}
	}
	project, _ := kanban.ProjectFor(a.workdir)
	full := ""
	if outcome == "committed" {
		full = m.full
	}
	audit.Emit(audit.Fact{Kind: audit.RepoCommit, ActorKind: audit.ActorAgent, Actor: "belai",
		SessionID: a.sessionID, Repo: project, Commit: full, Outcome: auditOutcome})
	if a.workdir == "" {
		return
	}
	rec := forge.CommitRecord{Mode: m.mode, Outcome: outcome, Commit: m.full, Reason: m.refused,
		Committed: len(m.split.Task), Left: forge.LeftOf(m.split)}
	if outcome != "committed" {
		rec.Committed = 0
	}
	if m.commit != nil {
		rec.Branch = m.commit.Branch
	}
	_ = forge.AppendCommitRecord(config.ProjectCommitsDir(a.workdir), a.sessionID, rec)
}

// openCommitAsk shows the question. A view already on screen keeps the screen;
// the question is then dropped, which commits nothing.
func (a *App) openCommitAsk(p *commitPlan) {
	if a.view != viewChat || a.working() {
		a.addSystem("commit: a question was waiting but another screen is open; nothing was committed (run /commit when ready)")
		return
	}
	a.commitAsk = commitAskState{seq: a.commitAsk.seq + 1, plan: p}
	a.push(viewCommit)
}

// closeCommitAsk drops the open question and denies it.
func (a *App) closeCommitAsk(reason string) {
	open := a.viewOpen(viewCommit)
	a.commitAsk = commitAskState{seq: a.commitAsk.seq + 1}
	if open {
		a.dropView(viewCommit)
	}
	if reason != "" {
		a.addSystem(reason)
	}
}

func (a *App) commitAskView() string {
	w := a.contentWidth()
	st := a.commitAsk
	var b strings.Builder
	b.WriteString(components.SectionHeader("Commit", "esc deny", w))
	b.WriteString("\n")
	if st.plan == nil {
		return lipgloss.NewStyle().Padding(1).Render(b.String())
	}
	sp := st.plan.split
	b.WriteString(components.EmphStyle.Render(fmt.Sprintf("%d files will be committed", len(sp.Task))) + "\n")
	for i, p := range sp.Task {
		if i == 12 {
			b.WriteString(components.MutedStyle.Render(fmt.Sprintf("  ... and %d more", len(sp.Task)-12)) + "\n")
			break
		}
		b.WriteString("  " + sanitize.Line(p, max(20, w-6)) + "\n")
	}
	if left := leftLine(sp); left != "" {
		b.WriteString("\n" + components.MutedStyle.Render(strings.TrimPrefix(left, "; ")) + "\n")
	}
	b.WriteString("\n" + components.EmphStyle.Render("message") + "\n")
	for _, l := range strings.Split(st.plan.msg, "\n") {
		b.WriteString("  " + sanitize.Line(l, max(20, w-6)) + "\n")
	}
	b.WriteString("\n" + components.MutedStyle.Render("nothing is pushed; the hooks of this repository run") + "\n")
	if st.err != "" {
		b.WriteString("\n" + components.DangerStyle.Render("x "+oneLine(st.err, w)) + "\n")
	}
	b.WriteString("\n" + components.HelpBar("y", "commit", "n", "deny", "esc", "deny") + "\n")
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func (a *App) handleCommitAskKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.commitAsk
	if st.plan == nil {
		a.pop()
		return a, nil
	}
	switch m.String() {
	case "esc", "n", "q":
		a.closeCommitAsk("commit: denied; nothing was committed")
	case "y":
		if st.working {
			return a, nil
		}
		st.working = true
		plan := st.plan
		seq := st.seq
		return a, func() tea.Msg { return commitAnsweredMsg{seq: seq, msg: confirmCommitPlan(context.Background(), plan)} }
	}
	return a, nil
}

// commitAnsweredMsg carries the result of a confirmed question.
type commitAnsweredMsg struct {
	seq int
	msg autoCommitMsg
}

// confirmCommitPlan re-reads the tree after a yes and commits only when it
// still holds exactly the paths the person was shown.
func confirmCommitPlan(ctx context.Context, p *commitPlan) autoCommitMsg {
	in := p.in
	m := autoCommitMsg{mode: in.mode, split: p.split, head: p.head}
	if op, err := operationUnderway(ctx, in.runner, in.workdir); err != nil {
		m.refused = "could not read the repository state (" + forge.CleanErr(err) + ")"
		return m
	} else if op != "" {
		m.refused = op + " is in progress"
		return m
	}
	now, err := forge.DirtyPaths(ctx, in.runner, in.workdir)
	if err != nil {
		m.refused = "could not read the tree (" + forge.CleanErr(err) + ")"
		return m
	}
	m.now, m.nowKnown = now, true
	again := forge.SplitDirty(in.before, now, in.touched, in.opts)
	if !slices.Equal(again.Task, p.split.Task) {
		m.refused = "the tree changed while the question was open"
		m.split = again
		return m
	}
	return applyCommitPlan(ctx, p, m)
}

// handleCommitAnswered applies the result of a confirmed question.
func (a *App) handleCommitAnswered(m commitAnsweredMsg) tea.Cmd {
	if m.seq != a.commitAsk.seq {
		return nil
	}
	a.closeCommitAsk("")
	return a.handleAutoCommit(m.msg)
}
