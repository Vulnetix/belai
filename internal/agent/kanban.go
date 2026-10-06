package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/todos"
	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/wire"
)

// The global kanban board (internal/kanban) reaches the model at three
// points, each with its own surface:
//
//   - every main-session call carries KanbanSearch and KanbanUpdate (the
//     registry's own tools, added by tools.Registry.WithKanban);
//   - an agent, goal or plan-execute turn adds KanbanMove to in_progress,
//     blocked or done, and its user turn carries a line of board facts —
//     counts and ids, never item text — so the model can keep the items it
//     touches current while it works;
//   - after the report of a work turn, one wrap-up pass is offered the turn's
//     own tool surface plus KanbanAdd (to review) and KanbanMove (to done),
//     and asked to file what the report left open and close what the turn
//     finished. Nothing is withheld from it: a model that stopped before the
//     work was done can still finish it. Its reply is discarded: the report
//     stays the reply.
//
// Explore, Task and recovery subagents are built without KanbanUpdate and get
// none of this: they may search the board, never write it.

// KanbanTriggers is the catalogue of what counts as open work in a report or
// conversation. It is rendered into the wrap-up directive; a test pins it.
var KanbanTriggers = []struct{ Category, Signals string }{
	{"unfinished", "not done, incomplete, partial, remaining, left over, still to do, next steps, not yet, pending, half-done, work in progress, stubbed, placeholder, mock or fake left in, not implemented, panic(\"todo\"), commented-out code, skipped or disabled tests"},
	{"markers", "TODO, FIXME, XXX, HACK or BUG markers added or found, notes left for later, @deprecated without a migration"},
	{"deferred", "out of scope, follow-up, later, future work, phase 2, v2, nice-to-have, could also, recommend, suggest, consider, should eventually, worth doing, left as an exercise"},
	{"unverified", "not tested, untested, tests not run or could not run, failing or flaky tests, build not run, lint or vet warnings, needs manual QA, not verified on a platform, CI pending or red, coverage gaps"},
	{"blocked", "needs a user decision, input, credentials or access; waiting on upstream, review, a PR, a release or another team; needs a migration, deploy, config or secret; permission denied; content withheld by the classifier; a tool unavailable; the goal stopped or stalled; the turn budget ran out"},
	{"found-not-fixed", "bugs, pre-existing failures, security issues or vulnerabilities (including scanner and dependency findings not remediated), outdated or vulnerable dependencies, deprecations, performance concerns, race or concurrency risks, missing error handling, unhandled edge cases, known limitations, caveats, regression risk"},
	{"temporary", "workarounds, temporary fixes, hard-coded values, debug logging or code to remove, feature flags to clean up, pending reverts, shims or compat layers to delete later"},
	{"tech-debt", "duplication, refactor opportunities, dead code, naming, the same change needed at other call sites, review suggestions not taken"},
	{"docs", "docs, README, CHANGELOG, comments or examples to update; version bump; release notes; announcements"},
	{"operational", "deploy, rollout, backfill or data migration, monitoring or alerts, secret rotation, environment variables, infrastructure changes"},
	{"question", "assumptions that need confirming, questions left unanswered or declined, ambiguities resolved by guessing, parts of the request not addressed"},
}

// kanbanSurface is one pre-rendered kanban variant of a tool surface.
type kanbanSurface struct {
	reg       *tools.Registry
	openAI    []wire.OpenAITool
	anthropic []wire.AnthropicToolDef
}

// kanbanState is the session's kanban wiring. The zero value is "no board".
type kanbanState struct {
	base tools.KanbanBase
	// on is true for a main session: its registry carries KanbanUpdate.
	// worker is true for a fleet worker's session: its kanban tools carry a
	// tools.WorkerClaim. It gets no loop KanbanMove and no wrap-up; the
	// harness releases the claimed item.
	on       bool
	worker   bool
	loopMove tools.KanbanMove
	// wrapUp is what the wrap-up pass adds to the turn's own surface.
	wrapUp []tools.Tool

	mu       sync.Mutex
	surfaces map[string]kanbanSurface
}

func newKanbanState(reg *tools.Registry) *kanbanState {
	ks := &kanbanState{}
	base, ok := tools.KanbanOf(reg)
	if !ok {
		return ks
	}
	if _, write := reg.Find(tools.KanbanUpdateName); !write {
		return ks
	}
	ks.base, ks.on, ks.worker = base, true, base.Claim != nil
	ks.loopMove = tools.KanbanMove{KanbanBase: base, Allowed: tools.KanbanLoopLists}
	ks.wrapUp = []tools.Tool{
		tools.KanbanAdd{KanbanBase: base},
		tools.KanbanMove{KanbanBase: base, Allowed: tools.KanbanWrapUpLists},
	}
	return ks
}

// loopSurface returns base plus the loop's KanbanMove, rendered once per
// named surface so the request bytes stay stable across calls.
func (ks *kanbanState) loopSurface(name string, base *tools.Registry) kanbanSurface {
	return ks.surfaceWith(name, base, ks.loopMove)
}

// wrapUpSurface returns base plus the wrap-up's KanbanAdd and KanbanMove.
func (ks *kanbanState) wrapUpSurface(name string, base *tools.Registry) kanbanSurface {
	return ks.surfaceWith("wrap-up:"+name, base, ks.wrapUp...)
}

// surfaceWith renders base plus extra once per cache key.
func (ks *kanbanState) surfaceWith(name string, base *tools.Registry, extra ...tools.Tool) kanbanSurface {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	if s, ok := ks.surfaces[name]; ok {
		return s
	}
	reg := base.With(extra...)
	oa, an := wireTools(reg)
	s := kanbanSurface{reg: reg, openAI: oa, anthropic: an}
	if ks.surfaces == nil {
		ks.surfaces = map[string]kanbanSurface{}
	}
	ks.surfaces[name] = s
	return s
}

// withKanbanLoop is toolSurface's last step: while the loop latch is on, the
// chosen surface gains KanbanMove; during the wrap-up pass it gains KanbanAdd
// and the wrap-up's KanbanMove instead.
func (s *Session) withKanbanLoop(name string, reg *tools.Registry, oa []wire.OpenAITool, an []wire.AnthropicToolDef) (*tools.Registry, []wire.OpenAITool, []wire.AnthropicToolDef) {
	if s.kanbanWrapUpPass && s.kanban != nil && s.kanban.on {
		ks := s.kanban.wrapUpSurface(name, reg)
		return ks.reg, ks.openAI, ks.anthropic
	}
	if !s.turnKanbanLoop || s.kanban == nil || !s.kanban.on {
		return reg, oa, an
	}
	ks := s.kanban.loopSurface(name, reg)
	return ks.reg, ks.openAI, ks.anthropic
}

// kanbanExecTool resolves the kanban tools that live only on a phase's
// surface. handled is false for every other name.
func (s *Session) kanbanExecTool(name string) (t tools.Tool, refusal string, handled bool) {
	if s.kanban == nil || !s.kanban.on {
		return nil, "", false
	}
	if s.kanbanWrapUpPass {
		for _, t := range s.kanban.wrapUp {
			if strings.EqualFold(name, t.Definition().Name) {
				return t, "", true
			}
		}
		return nil, "", false
	}
	switch {
	case strings.EqualFold(name, tools.KanbanAddName):
		return nil, fmt.Sprintf("tool result withheld: %q is only offered after the final report", name), true
	case strings.EqualFold(name, tools.KanbanMoveName):
		if s.planMode {
			return nil, fmt.Sprintf("tool result withheld: %q is not available in plan mode", name), true
		}
		if !s.turnKanbanLoop {
			return nil, fmt.Sprintf("tool result withheld: %q is not available on this turn", name), true
		}
		return s.kanban.loopMove, "", true
	}
	return nil, "", false
}

// findCallable resolves a call name for pass's up-front checks: the phase's
// kanban tools first, then the registry.
func (s *Session) findCallable(name string) (tools.Tool, bool) {
	if t, _, handled := s.kanbanExecTool(name); handled {
		return t, t != nil
	}
	if s.turnCode && s.codeRegistry != nil {
		if t, ok := s.codeRegistry.Find(name); ok {
			return t, true
		}
	}
	return s.registry.Find(name)
}

// callableNames is every name a call may carry without tripping the
// mismatch check: the registry plus the phase's kanban tools.
func (s *Session) callableNames() []string {
	names := s.registry.Names()
	if s.turnCode && s.codeRegistry != nil {
		names = append(names, tools.CodeName)
	}
	if s.kanban != nil && s.kanban.on {
		names = append(names, tools.KanbanAddName, tools.KanbanMoveName)
	}
	return names
}

// kanbanDirective is the loop-start line of board facts: counts and ids for
// this project, never item text, which was written by other sessions and
// must not ride in a sealed directive.
func (s *Session) kanbanDirective(prompt string) string {
	if s.kanban != nil && s.kanban.worker {
		return workerDirective(s.kanban.base.Claim)
	}
	// A remediation turn is about the edit; the board waits until it lands, so
	// its items are not offered to keep current (remediation.go).
	if !s.turnKanbanLoop || s.turnRemediation {
		return ""
	}
	prov := s.kanban.base.Source.Get()
	project := prov.ProjectKey
	if project == "" {
		project = prov.Project
	}
	counts, err := s.kanban.base.Store.Counts(project)
	if err != nil {
		return ""
	}
	var ids []string
	if items, err := s.kanban.base.Store.Search(kanban.Query{Project: project, Lists: []kanban.List{kanban.InProgress, kanban.Blocked}, Limit: 12}); err == nil {
		for _, it := range items {
			ids = append(ids, it.Short()+" "+string(it.List))
		}
	}
	for _, ref := range kanbanRefs(prompt) {
		if it, err := s.kanban.base.Store.Get(ref); err == nil {
			ids = append(ids, it.Short()+" "+string(it.List)+" (named in the prompt)")
		}
	}
	// An empty board for this project says nothing worth a directive: the
	// tool descriptions already say how to use them.
	if len(ids) == 0 && counts[kanban.Backlog]+counts[kanban.Review]+counts[kanban.InProgress]+counts[kanban.Blocked] == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Kanban board for project %s: backlog %d, review %d, in_progress %d, blocked %d.",
		prov.Project, counts[kanban.Backlog], counts[kanban.Review], counts[kanban.InProgress], counts[kanban.Blocked])
	if len(ids) > 0 {
		fmt.Fprintf(&b, " Items to keep current: %s.", strings.Join(ids, ", "))
	}
	b.WriteString(" As you work, keep the items you touch current: KanbanMove an item to in_progress when you start it, to blocked with the blocker as the note when you cannot continue, and to done once it is finished and verified; KanbanUpdate adds a progress note. KanbanSearch reads an item. None of this replaces the task itself.")
	return b.String()
}

// workerDirective is a fleet worker's board line: the claimed id and the
// handoff contract, harness facts only. It names no other item, so a worker
// is never invited to touch another worker's claim.
func workerDirective(c *tools.WorkerClaim) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You hold kanban item %s; its text is attached. The harness moves it when this goal ends — do not try to move it. KanbanUpdate may add a progress note to it.", kanban.ShortID(c.Item))
	if len(c.HandoffTo) > 0 || len(c.HandoffLabels) > 0 {
		fmt.Fprintf(&b, " Work that belongs to another agent goes on the board with KanbanHandoff (at most %d per item)", tools.MaxHandoffsPerItem)
		if len(c.HandoffTo) > 0 {
			fmt.Fprintf(&b, "; agents: %s", strings.Join(c.HandoffTo, ", "))
		}
		if len(c.HandoffLabels) > 0 {
			fmt.Fprintf(&b, "; labels: %s", strings.Join(c.HandoffLabels, ", "))
		}
		b.WriteString(".")
	}
	return b.String()
}

// kanbanRefs finds K-xxxxxx references in the prompt.
func kanbanRefs(prompt string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(prompt, func(r rune) bool {
		return !(r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F' || r == 'K' || r == 'k')
	}) {
		if len(f) == 8 && (f[:2] == "K-" || f[:2] == "k-") && len(out) < 5 {
			out = append(out, f)
		}
	}
	return out
}

// kanbanTurn is what the turn tells the wrap-up about itself: harness facts.
type kanbanTurn struct {
	work     bool
	sentinel rolemanager.GoalSentinel
	todos    *todos.List
	reviews  int
}

// kanbanWrapUpDirective renders the sealed wrap-up instruction.
func kanbanWrapUpDirective(kt kanbanTurn) string {
	var b strings.Builder
	b.WriteString("Your report is above. If the work it describes is not actually finished (an edit you described but did not make, a command you did not run), finish it now with your normal tools. Now update the global kanban board.\n\n")
	b.WriteString("1. File open work to review. For every distinct piece of work the report or this conversation leaves open, call KanbanAdd once: an imperative title and a body with the context a fresh session needs (files, symbols, why). Search first (KanbanSearch) so you do not add what is already there; add a KanbanUpdate note to an existing item instead. Open work includes anything in these categories:\n")
	for _, t := range KanbanTriggers {
		fmt.Fprintf(&b, "   - %s: %s\n", t.Category, t.Signals)
	}
	b.WriteString("2. Close finished work. KanbanSearch this project's backlog, review, in_progress and blocked items, and KanbanMove to done every item this turn completed or showed was already resolved, with a note of what did it. A duplicate or superseded item moves to done with a note naming the item that replaces it.\n")
	b.WriteString("3. Do not rewrite or repeat the report. When the work is done and the board is current, reply exactly KANBAN_DONE.")
	var facts []string
	if kt.sentinel != "" {
		facts = append(facts, "the loop ended as "+kt.sentinel.Label())
	}
	if kt.todos != nil && len(kt.todos.Items) > 0 {
		var open []string
		for _, it := range kt.todos.Items {
			if it.Status != todos.StatusDone {
				open = append(open, fmt.Sprint(it.N))
			}
		}
		if len(open) > 0 {
			facts = append(facts, fmt.Sprintf("%d of %d update_plan steps are not done (steps %s)", len(open), len(kt.todos.Items), strings.Join(open, ", ")))
		}
	}
	if kt.reviews > 0 {
		facts = append(facts, fmt.Sprintf("%d scanner reports were reviewed; every finding not remediated is open work", kt.reviews))
	}
	if len(facts) > 0 {
		b.WriteString("\n\nHarness facts: " + strings.Join(facts, "; ") + ".")
	}
	return b.String()
}

// kanbanWrapUp runs the wrap-up pass after a work turn's report. It never
// changes res and never fails the turn: a failure is a warning.
func (s *Session) kanbanWrapUp(ctx context.Context, pipe *rolemanager.Pipeline, system string, turns []run.Turn, res run.Result, kt kanbanTurn, streaming bool, emit func(Event)) {
	if !kt.work || s.kanban == nil || !s.kanban.on || !s.allowPassLoop || s.exploreSubagent || ctx.Err() != nil {
		return
	}
	if !s.kanbanNeeded(res.Reply, kt) {
		s.traceRecord("kanban_wrap_up", "skipped", "", "no open work and no open items", 0)
		return
	}
	s.kanbanWrapUpPass = true
	defer func() { s.kanbanWrapUpPass = false }()
	emit(Event{Kind: EventKanbanKind, Phase: KanbanPhaseStart})
	s.traceRecord("kanban_wrap_up", "", "", "", 0)
	summary := &KanbanSummary{}
	var mu sync.Mutex
	quiet := func(e Event) {
		switch e.Kind {
		case EventTextKind, EventReasoningKind, EventToolCallDeltaKind:
			// The wrap-up's own words are bookkeeping chatter; the report
			// is the reply. Tool calls still show.
			return
		case EventToolResultKind:
			if !strings.HasPrefix(e.ToolResult, "tool result withheld:") {
				mu.Lock()
				switch {
				case strings.EqualFold(e.ToolName, tools.KanbanAddName) && strings.Contains(e.ToolResult, "added to review"):
					summary.Added++
				case strings.EqualFold(e.ToolName, tools.KanbanMoveName):
					summary.Moved++
				case strings.EqualFold(e.ToolName, tools.KanbanUpdateName):
					summary.Updated++
				}
				mu.Unlock()
			}
		}
		emit(e)
	}
	turns = withReply(turns, res.Reply)
	turns = append(turns, directiveTurns(kanbanWrapUpDirective(kt))...)
	_, _, err := s.pass(ctx, pipe, system, turns, streaming, quiet, modes.ModeAgent)
	if err != nil && ctx.Err() == nil {
		emit(Event{Kind: EventWarningKind, Warning: "kanban update failed: " + err.Error()})
	}
	emit(Event{Kind: EventKanbanKind, Phase: KanbanPhaseDone, Kanban: summary})
}

// Kanban phases carried by EventKanbanKind.
const (
	KanbanPhaseStart = "kanban-start"
	KanbanPhaseDone  = "kanban-done"
)

// KanbanSummary counts what the wrap-up changed.
type KanbanSummary struct {
	Added, Moved, Updated int
}

// openWorkPattern matches the wording of open work in a report: the trigger
// catalogue's signals, as phrases specific enough that a clean "created X,
// tests pass" report does not match. It gates the wrap-up, which costs one or
// more full-context model calls, so a turn that plainly finished everything
// on a board with nothing open for this project skips it.
var openWorkPattern = regexp.MustCompile(`(?i)\b(` +
	`todo|fixme|xxx|hack|not (yet )?(done|implemented|tested|run|verified|handled|supported|covered|addressed)|` +
	`unfinished|incomplete|partial(ly)?|remaining (work|tasks?|steps?|items?|issues?|failures?|errors?|todos?|gaps?)|remains to|left (over|to do|open|in place|as is)|still (need|needs|to|pending|open|fail)|` +
	`next steps?|follow[- ]?ups?|out of scope|future work|phase 2|nice[- ]to[- ]have|you (could|may|might) (also|want)|` +
	`recommend\w*|consider (adding|using|a|an|the)|should (eventually|also|be)|untested|not been (tested|run|verified)|` +
	`failing|fails|failed|flaky|could ?n[o'’]t|cannot|can ?not|unable to|blocked|waiting (on|for)|requires? (a|an|manual|user|credentials|access)|` +
	`manual(ly)? (qa|test|check|verif\w*)|workaround|temporar(y|ily)|hard[- ]?coded|debug (code|log|print)|deprecat\w*|limitations?|caveats?|` +
	`edge cases?|known issues?|tech(nical)? debt|refactor\w*|duplicat\w*|(update|updating) (the )?(docs?|documentation|readme|changelog)|` +
	`release notes|deploy\w*|migrations?|backfill|assum(e|ed|ption)s?|unclear|open questions?|stubs?|stubbed|placeholders?|skipped|warnings?` +
	`)\b`)

// openWorkHeading matches a report line that opens a list of open work
// ("Remaining:", "- Not done", "## Known issues").
var openWorkHeading = regexp.MustCompile(`(?im)^\s*[-*#>]*\s*\**(remaining|still to do|not done|open (items|issues|questions)|known issues|limitations|caveats)\**\s*[:—-]`)

// negatedLead matches a negation just before an open-work phrase: "nothing
// is left open", "no known issues", "there are no failing tests", "0 failed".
var negatedLead = regexp.MustCompile(`(?i)\b(no|nothing|none|never|without|zero|nor|0)\b[^.!?\n]{0,24}$`)

// reportHasOpenWork reports whether a report names open work: an open-work
// heading, or an open-work phrase that is not negated.
func reportHasOpenWork(report string) bool {
	for _, m := range openWorkHeading.FindAllStringIndex(report, -1) {
		if !negatedTail.MatchString(report[m[1]:min(len(report), m[1]+40)]) {
			return true
		}
	}
	for _, m := range openWorkPattern.FindAllStringIndex(report, -1) {
		lead := report[max(0, m[0]-40):m[0]]
		tail := report[m[1]:min(len(report), m[1]+40)]
		if !negatedLead.MatchString(lead) && !negatedTail.MatchString(tail) {
			return true
		}
	}
	return false
}

// negatedTail matches a label answered with a negation: "Follow-up: none",
// "**Remaining work:** nothing", "Known issues — n/a".
var negatedTail = regexp.MustCompile(`(?i)^[\s*_:—–-]*(none|nothing|n/a|no\b)`)

// kanbanNeeded reports whether a work turn has anything for the wrap-up to
// do: a harness fact saying work is open, an open-work phrase in the turn's
// report, or open items on this project's board that the turn may
// have finished. Only when all of them are absent is the wrap-up skipped.
func (s *Session) kanbanNeeded(reply string, kt kanbanTurn) bool {
	if kt.reviews > 0 || (kt.sentinel != "" && kt.sentinel != rolemanager.GoalComplete) {
		return true
	}
	if kt.todos != nil && len(kt.todos.Items) > 0 && !kt.todos.Complete() {
		return true
	}
	// The report is where a turn says what it left open. Mid-turn narration
	// ("that failed, retrying") is not, and scanning it ran the wrap-up after
	// turns that ended clean.
	if reportHasOpenWork(reply) {
		return true
	}
	prov := s.kanban.base.Source.Get()
	project := prov.ProjectKey
	if project == "" {
		project = prov.Project
	}
	counts, err := s.kanban.base.Store.Counts(project)
	if err != nil {
		return true // an unreadable board is reported by the tools, not hidden here
	}
	return counts[kanban.Backlog]+counts[kanban.Review]+counts[kanban.InProgress]+counts[kanban.Blocked] > 0
}
