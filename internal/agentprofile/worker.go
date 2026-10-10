package agentprofile

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/cron"
	"github.com/vulnetix/belai/internal/kanban"
)

// ModeWorker is a fleet worker: it claims kanban items one at a time and
// works each as a goal (docs/fleet.md).
const ModeWorker = "worker"

// KanbanSpec says which items a worker claims and where they go next.
type KanbanSpec struct {
	// Lists to claim from: backlog and/or review. Empty means backlog.
	Lists []string `json:"lists,omitempty"`
	// Labels an item must all carry to be claimed.
	Labels []string `json:"labels,omitempty"`
	// AssignedOnly claims only items assigned to this profile by name.
	AssignedOnly bool `json:"assigned_only,omitempty"`
	// Project is "current" (default: the repository the worker runs in),
	// "all", or a project name.
	Project string `json:"project,omitempty"`
	// OnSuccess is where a completed item goes; OnFailure where a failed
	// attempt goes (default: back to the list it was claimed from). Once an
	// item has failed MaxAttempts times it goes to blocked.
	OnSuccess Route `json:"on_success"`
	OnFailure Route `json:"on_failure,omitempty"`
	// HandoffTo and HandoffLabels are the profiles and labels KanbanHandoff
	// may route new items to. Both empty: no handoffs.
	HandoffTo     []string `json:"handoff_to,omitempty"`
	HandoffLabels []string `json:"handoff_labels,omitempty"`
	// HandoffRepos gives KanbanHandoff a repo argument that files the task
	// under a repository checked out beneath the worker's directory, chosen
	// from the harness's local repository index. It is for a worker that runs
	// in a plain folder holding several repositories and finds work for each.
	HandoffRepos bool `json:"handoff_repos,omitempty"`
	MaxAttempts  int  `json:"max_attempts,omitempty"`
	// Lease is how long a claim holds without renewal; Poll how often an
	// idle worker looks for work. Go durations.
	Lease string `json:"lease,omitempty"`
	Poll  string `json:"poll,omitempty"`
	// MaxItems stops the worker after this many items; 0 runs until stopped.
	MaxItems int `json:"max_items,omitempty"`
	// Survey lets the worker find its own work when the board has none for
	// it. Nil: the worker only takes items someone filed.
	Survey *SurveySpec `json:"survey,omitempty"`
	// Security turns on the harness's vulnerability duties for the worker:
	// the review sweep, card reconciliation, verdicts and VEX. Nil: none.
	Security *SecuritySpec `json:"security,omitempty"`
	// Quality turns on the harness's test-quality sweep for the worker. Nil:
	// none.
	Quality *QualitySpec `json:"quality,omitempty"`
	// Gates turns on acceptance gates for the worker: the tasks it hands on
	// carry them, and the harness verifies them on the branch. Nil: none.
	Gates *GatesSpec `json:"gates,omitempty"`
}

// QualityLabel marks a card the harness seeded from a test run. Every handoff
// made while working one goes to the quality block's list, whatever the model
// asks for.
const QualityLabel = "quality"

// QualitySpec is a worker's harness-run quality sweep. On each new HEAD with
// no quality record, the harness runs the repository's detected or configured
// test suites, writes what they show to a record for that commit, and files
// seed cards for the worker: failing suites, low coverage, untested packages
// and the standing quality categories. No model runs the suites.
type QualitySpec struct {
	Sweep bool `json:"sweep,omitempty"`
	// List is where the handoffs from a seeded card go: review (default), so a
	// person confirms self-found work, or backlog.
	List string `json:"list,omitempty"`
}

// ListAuto is the list value that has the harness route each handoff by whether
// it is clear and concise (backlog) or needs a person to confirm or split it
// (review), instead of sending every handoff to one list.
const ListAuto = "auto"

// Auto reports whether each handoff is routed by its clarity. The list a
// handoff falls back to, and the one HandoffList reports, is review.
func (q QualitySpec) Auto() bool { return q.List == ListAuto }

// HandoffList is where a seeded card's handoffs go.
func (q QualitySpec) HandoffList() kanban.List {
	if l, ok := kanban.ParseList(q.List); ok {
		return l
	}
	return kanban.Review
}

// MaxRounds bounds kanban.security.rounds.
const MaxRounds = 5

// SecuritySpec is a security worker's harness-run duties. None of them is a
// model's decision: the harness runs the review, reads the artefacts and
// files, reconciles and routes the cards.
type SecuritySpec struct {
	// Sweep makes sure a review ran on the repository's HEAD (running it when
	// no artefact records that commit) and that every finding it reports has
	// a card. It needs the Vulnetix tool.
	Sweep bool `json:"sweep,omitempty"`
	// Reconcile compares the cards with the latest artefacts before each
	// claim: a finding that left the report becomes a gone card for the
	// verifier. It never runs a scan.
	Reconcile bool `json:"reconcile,omitempty"`
	// Verdicts are the verdicts the worker may record with KanbanVerdict.
	// Empty: the worker has no such tool.
	Verdicts []string `json:"verdicts,omitempty"`
	// VEX has the harness write a VEX document for each verdict the worker
	// records, and lets the worker reject a claim.
	VEX bool `json:"vex,omitempty"`
	// Rounds is how many turns one item may take. After each turn the harness
	// scans the worktree and, while the scanner still reports the card's
	// finding, runs another turn with the result attached. 0 or 1: one turn.
	// Needs isolation: worktree.
	Rounds int `json:"rounds,omitempty"`
}

// SurveyLabel marks an item a worker filed for itself under kanban.survey.
// Every handoff made while working one goes to the survey's list, whatever
// the model asks for.
const SurveyLabel = "survey"

// DefaultSurveyEvery is how often a worker may survey the same repository
// on one machine when kanban.survey.every is unset.
const DefaultSurveyEvery = 24 * time.Hour

// SurveySpec is a worker's own work-finding pass. When the worker has
// nothing to claim it files one item titled Title (dated, with {project}
// replaced), labelled with its own claim labels plus survey, claims it and
// works it like any other item. It does so at most once per start and once
// per Every for the same repository on the same machine.
type SurveySpec struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	// List is where the survey's handoffs go: review (default), so a human
	// confirms self-found work before any agent takes it, or backlog.
	List string `json:"list,omitempty"`
	// Every is a Go duration of at least 1h (default 24h).
	Every string `json:"every,omitempty"`
}

// Auto reports whether each handoff is routed by its clarity (see ListAuto).
func (s SurveySpec) Auto() bool { return s.List == ListAuto }

// HandoffList is where the survey's handoffs go.
func (s SurveySpec) HandoffList() kanban.List {
	if l, ok := kanban.ParseList(s.List); ok {
		return l
	}
	return kanban.Review
}

// EveryOr is the survey interval, defaulted.
func (s SurveySpec) EveryOr() time.Duration {
	if d, err := time.ParseDuration(s.Every); err == nil && d > 0 {
		return d
	}
	return DefaultSurveyEvery
}

// SurveyGrace is how early a start may be and still survey. The interval is
// measured from the stamp the previous survey wrote, which is written after the
// item is filed, so a start exactly one interval later (an hourly cron, an
// hourly schedule) lands a fraction of a second short and would be skipped about
// as often as not. The grace is two minutes, and never more than a tenth of the
// interval.
func (s SurveySpec) SurveyGrace() time.Duration {
	return min(2*time.Minute, s.EveryOr()/10)
}

// Route is a destination list plus label edits.
type Route struct {
	List       string   `json:"list,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	DropLabels []string `json:"drop_labels,omitempty"`
}

// WorkspaceSpec says where a worker makes its changes.
type WorkspaceSpec struct {
	// Isolation is "worktree" (a git worktree per item, outside the
	// repository), "shared" (the repository itself), or "none" (the default:
	// the worker does not write files).
	Isolation string `json:"isolation,omitempty"`
	// Base is the commit a new branch starts from (default HEAD).
	Base string `json:"base,omitempty"`
	// Keep leaves the worktree on disk after the item is released.
	Keep bool `json:"keep,omitempty"`
	// ReadOnly is a worker that runs checks (tests, builds) in its worktree
	// but changes nothing: the harness commits nothing the turn leaves
	// behind, such as build output, and the worktree and its branch are
	// discarded after each item. It needs isolation: worktree and no publish.
	ReadOnly bool `json:"read_only,omitempty"`
	// Publish is "none" (default) or "draft_pr": when the item reaches done,
	// push its branch and open a draft pull request.
	Publish string `json:"publish,omitempty"`
	// Sync lists files and directories under .vulnetix/crews that the harness
	// copies into the worktree before each turn and, with write access, merges
	// back after it (see sync.go). It needs isolation: worktree.
	Sync []SyncSpec `json:"sync,omitempty"`
}

// MemorySpec turns on the worker's lessons file.
type MemorySpec struct {
	Enabled  bool `json:"enabled,omitempty"`
	MaxBytes int  `json:"max_bytes,omitempty"`
}

// BudgetSpec bounds one item.
type BudgetSpec struct {
	MaxPassesPerItem int    `json:"max_passes_per_item,omitempty"`
	MaxTokensPerItem int    `json:"max_tokens_per_item,omitempty"`
	MaxWallPerItem   string `json:"max_wall_per_item,omitempty"`
}

// Isolation values.
const (
	IsolationNone     = "none"
	IsolationWorktree = "worktree"
	IsolationShared   = "shared"
)

// Publish values.
const (
	PublishNone    = "none"
	PublishDraftPR = "draft_pr"
	// PublishAgent gives the model the PublishBranch tool: it pushes its
	// branch and opens a draft pull request itself, whenever it judges the
	// work ready. The harness also opens one at done if none exists.
	PublishAgent = "agent"
)

// Defaults.
const (
	DefaultLease       = 20 * time.Minute
	DefaultPoll        = 30 * time.Second
	DefaultMaxAttempts = 3
	DefaultMemoryBytes = 8 << 10
	MaxMemoryBytes     = 64 << 10
)

// writingTools are the tools that change the workspace.
var writingTools = []string{"Write", "Edit", "Bash"}

// Writes reports whether the profile may change files: its allowlist names a
// writing tool, or it has no allowlist (the full surface).
func (p AgentProfile) Writes() bool {
	if len(p.Tools) == 0 {
		return true
	}
	for _, t := range p.Tools {
		if slices.Contains(writingTools, t) {
			return true
		}
	}
	return false
}

// editingTools are the file tools. Bash is not among them here: a worker that
// only runs checks has Bash and still changes nothing it is judging.
var editingTools = []string{"Write", "Edit"}

// Decides reports whether the worker's deliverable is a recorded decision
// rather than a change to the code: its workspace is read-only, it decides
// manual gates, or its allowlist has no file tool. The goal loop then asks it
// for the decision and never tells it to make an edit. An empty allowlist is
// the full surface, which edits.
func (p AgentProfile) Decides() bool {
	if p.ReadOnlyWorkspace() {
		return true
	}
	if p.Kanban != nil && p.Kanban.Gates != nil && p.Kanban.Gates.Review {
		return true
	}
	if len(p.Tools) == 0 {
		return false
	}
	for _, t := range p.Tools {
		if slices.Contains(editingTools, t) {
			return false
		}
	}
	return true
}

// HasTool reports whether the allowlist grants name (an empty allowlist
// grants everything).
func (p AgentProfile) HasTool(name string) bool {
	return len(p.Tools) == 0 || slices.Contains(p.Tools, name)
}

// LeaseDuration is the claim lease.
func (p AgentProfile) LeaseDuration() time.Duration {
	if p.Kanban == nil {
		return DefaultLease
	}
	if d, err := time.ParseDuration(p.Kanban.Lease); err == nil && d > 0 {
		return d
	}
	return DefaultLease
}

// PollInterval is how often an idle worker looks for work.
func (p AgentProfile) PollInterval() time.Duration {
	if p.Kanban != nil {
		if d, err := time.ParseDuration(p.Kanban.Poll); err == nil && d > 0 {
			return d
		}
	}
	return DefaultPoll
}

// MaxAttemptsOr is the failed-attempt limit.
func (p AgentProfile) MaxAttemptsOr() int {
	if p.Kanban == nil || p.Kanban.MaxAttempts <= 0 {
		return DefaultMaxAttempts
	}
	return p.Kanban.MaxAttempts
}

// WallBudget is the per-item wall-clock budget, or 0.
func (p AgentProfile) WallBudget() time.Duration {
	if p.Budget == nil {
		return 0
	}
	d, _ := time.ParseDuration(p.Budget.MaxWallPerItem)
	return d
}

// IsolationMode is the workspace isolation, defaulted.
// ReadOnlyWorkspace reports workspace.read_only.
func (p AgentProfile) ReadOnlyWorkspace() bool {
	return p.Workspace != nil && p.Workspace.ReadOnly
}

func (p AgentProfile) IsolationMode() string {
	if p.Workspace == nil || p.Workspace.Isolation == "" {
		return IsolationNone
	}
	return p.Workspace.Isolation
}

// ClaimLists are the lists to claim from.
func (k KanbanSpec) ClaimLists() []kanban.List {
	if len(k.Lists) == 0 {
		return []kanban.List{kanban.Backlog}
	}
	out := make([]kanban.List, 0, len(k.Lists))
	for _, l := range k.Lists {
		if pl, ok := kanban.ParseList(l); ok {
			out = append(out, pl)
		}
	}
	return out
}

// CronSchedule parses a cron schedule: "cron: <expr>", or a bare five-field
// expression. ok is false for a duration schedule (the older interval form).
func CronSchedule(s string) (cron.Schedule, bool, error) {
	s = strings.TrimSpace(s)
	expr, isCron := strings.CutPrefix(s, "cron:")
	if !isCron && (len(strings.Fields(s)) != 5 && !strings.HasPrefix(s, "@")) {
		return cron.Schedule{}, false, nil
	}
	sched, err := cron.Parse(strings.TrimSpace(expr))
	return sched, true, err
}

func validateRoute(name string, r Route, required bool) error {
	if r.List == "" {
		if required {
			return fmt.Errorf("kanban.%s.list is required", name)
		}
		return nil
	}
	if _, ok := kanban.ParseList(r.List); !ok {
		return fmt.Errorf("kanban.%s.list %q is not a list", name, r.List)
	}
	return nil
}

// validateWorker checks the worker-only blocks. It runs from Validate.
func (p AgentProfile) validateWorker() error {
	if p.Mode != ModeWorker {
		if p.Kanban != nil {
			return errors.New("a kanban block needs mode: worker")
		}
		return nil
	}
	k := p.Kanban
	if k == nil {
		return errors.New("mode worker needs a kanban block (which items to claim, and where they go)")
	}
	for _, l := range k.Lists {
		pl, ok := kanban.ParseList(l)
		if !ok || !slices.Contains(kanban.ClaimableLists, pl) {
			return fmt.Errorf("kanban.lists: a worker claims from backlog or review, not %q", l)
		}
	}
	if err := validateRoute("on_success", k.OnSuccess, true); err != nil {
		return err
	}
	if err := validateRoute("on_failure", k.OnFailure, false); err != nil {
		return err
	}
	for _, a := range k.HandoffTo {
		if _, err := kanban.CleanAssignee(a); err != nil || strings.TrimSpace(a) == "" {
			return fmt.Errorf("kanban.handoff_to: %q is not a profile name", a)
		}
	}
	for _, l := range k.HandoffLabels {
		if n := kanban.NormLabels([]string{l}); len(n) != 1 || n[0] != l {
			return fmt.Errorf("kanban.handoff_labels: %q is not a normalised label (lower-case [a-z0-9:_-])", l)
		}
	}
	if k.HandoffRepos {
		if len(k.HandoffTo) == 0 && len(k.HandoffLabels) == 0 {
			return errors.New("kanban.handoff_repos needs handoff_to or handoff_labels: it files handoffs under other repositories")
		}
		if k.Gates != nil {
			return errors.New("kanban.handoff_repos cannot be combined with kanban.gates: a gate names a test suite of the worker's own repository")
		}
	}
	if k.Lease != "" {
		d, err := time.ParseDuration(k.Lease)
		if err != nil || d < kanban.MinLease || d > kanban.MaxLease {
			return fmt.Errorf("kanban.lease must be a duration from %s to %s", kanban.MinLease, kanban.MaxLease)
		}
	}
	if k.Poll != "" {
		if d, err := time.ParseDuration(k.Poll); err != nil || d < 5*time.Second {
			return errors.New("kanban.poll must be a duration of at least 5s")
		}
	}
	if s := k.Survey; s != nil {
		if strings.TrimSpace(s.Title) == "" {
			return errors.New("kanban.survey.title is required")
		}
		if s.List != "" {
			if l, ok := kanban.ParseList(s.List); s.List != ListAuto && (!ok || (l != kanban.Review && l != kanban.Backlog)) {
				return fmt.Errorf("kanban.survey.list must be review, backlog or auto, not %q", s.List)
			}
		}
		if s.Every != "" {
			if d, err := time.ParseDuration(s.Every); err != nil || d < time.Hour {
				return errors.New("kanban.survey.every must be a duration of at least 1h")
			}
		}
		if len(k.HandoffTo) == 0 && len(k.HandoffLabels) == 0 {
			return errors.New("kanban.survey needs handoff_to or handoff_labels: a survey files what it finds as handoffs")
		}
	}
	if s := k.Security; s != nil {
		if err := s.validate(p.Tools); err != nil {
			return err
		}
		if s.Rounds > 1 && p.IsolationMode() != IsolationWorktree {
			return errors.New("kanban.security.rounds needs workspace.isolation: worktree (a tree to scan after each round)")
		}
	}
	if q := k.Quality; q != nil {
		if q.List != "" {
			if l, ok := kanban.ParseList(q.List); q.List != ListAuto && (!ok || (l != kanban.Review && l != kanban.Backlog)) {
				return fmt.Errorf("kanban.quality.list must be review, backlog or auto, not %q", q.List)
			}
		}
		if q.Sweep && len(k.HandoffTo) == 0 && len(k.HandoffLabels) == 0 {
			return errors.New("kanban.quality.sweep needs handoff_to or handoff_labels: a seeded card is worked by handing tasks on")
		}
	}
	if g := k.Gates; g != nil {
		if err := g.validate(k); err != nil {
			return err
		}
	}
	if k.MaxAttempts < 0 || k.MaxItems < 0 {
		return errors.New("kanban.max_attempts and kanban.max_items must not be negative")
	}
	if p.Schedule != "" {
		if _, _, err := CronSchedule(p.Schedule); err != nil {
			return err
		}
	}
	if w := p.Workspace; w != nil {
		switch w.Isolation {
		case "", IsolationNone, IsolationWorktree, IsolationShared:
		default:
			return fmt.Errorf("workspace.isolation must be worktree, shared or none, not %q", w.Isolation)
		}
		switch w.Publish {
		case "", PublishNone, PublishDraftPR, PublishAgent:
		default:
			return fmt.Errorf("workspace.publish must be none, draft_pr or agent, not %q", w.Publish)
		}
		if (w.Publish == PublishDraftPR || w.Publish == PublishAgent) && w.Isolation != IsolationWorktree {
			return errors.New("workspace.publish needs isolation: worktree (a branch to publish)")
		}
		if w.ReadOnly && (w.Isolation != IsolationWorktree || w.Keep || (w.Publish != "" && w.Publish != PublishNone)) {
			return errors.New("workspace.read_only needs isolation: worktree, and cannot keep the worktree or publish")
		}
		if strings.HasPrefix(strings.TrimSpace(w.Base), "-") {
			return errors.New("workspace.base must be a commit or branch, not an option")
		}
		if err := validateSync(w); err != nil {
			return err
		}
	}
	if b := p.Budget; b != nil {
		if b.MaxPassesPerItem < 0 || b.MaxTokensPerItem < 0 {
			return errors.New("budget values must not be negative")
		}
		if b.MaxWallPerItem != "" {
			if d, err := time.ParseDuration(b.MaxWallPerItem); err != nil || d <= 0 {
				return errors.New("budget.max_wall_per_item must be a positive duration")
			}
		}
	}
	if m := p.Memory; m != nil && (m.MaxBytes < 0 || m.MaxBytes > MaxMemoryBytes) {
		return fmt.Errorf("memory.max_bytes must be at most %d", MaxMemoryBytes)
	}
	// Fail closed on the relaxations an unattended worker could stack.
	if p.Guardrails != nil && !*p.Guardrails {
		return errors.New("a worker cannot turn guardrails off: its item text and every tool result must stay classified")
	}
	if p.Autonomy == AutonomyAutonomous && (p.Budget == nil || p.Budget.MaxPassesPerItem <= 0) {
		return errors.New("an autonomous worker needs budget.max_passes_per_item")
	}
	if p.Writes() && p.IsolationMode() == IsolationNone {
		return errors.New("a worker that can write (Write, Edit or Bash, or no tools allowlist) needs workspace.isolation: worktree or shared")
	}
	return nil
}

// PublishMode is workspace.publish, defaulted to none.
func (p AgentProfile) PublishMode() string {
	if p.Workspace == nil || p.Workspace.Publish == "" {
		return PublishNone
	}
	return p.Workspace.Publish
}

// validate checks the security block against the profile's tool list.
func (s SecuritySpec) validate(tools []string) error {
	if s.Sweep && !slices.Contains(tools, "Vulnetix") {
		return errors.New("kanban.security.sweep needs the Vulnetix tool in tools")
	}
	if s.VEX && len(s.Verdicts) == 0 {
		return errors.New("kanban.security.vex needs verdicts: a VEX documents a verdict")
	}
	if s.Rounds < 0 || s.Rounds > MaxRounds {
		return fmt.Errorf("kanban.security.rounds runs 0 to %d", MaxRounds)
	}
	seen := map[string]bool{}
	for _, v := range s.Verdicts {
		if !kanban.Verdict(v).Valid() {
			return fmt.Errorf("kanban.security.verdicts: %q is not a verdict", v)
		}
		if v == string(kanban.VerdictRejected) && !s.VEX {
			return errors.New("kanban.security.verdicts: only a worker that writes the VEX may reject")
		}
		if seen[v] {
			return fmt.Errorf("kanban.security.verdicts: %q is listed twice", v)
		}
		seen[v] = true
	}
	return nil
}

// Gate verification modes (kanban.gates.verify).
const (
	// VerifyOff leaves the crew as it was: a card is done when a worker says
	// so and the goal evaluator agrees.
	VerifyOff = "off"
	// VerifyRecord has the harness run a card's gates on the branch and write
	// what they show to the card, without changing where the card goes.
	VerifyRecord = "record"
	// VerifyEnforce has the harness run a card's gates on the branch and decide
	// where it goes: a card whose gates are not all met is not done.
	VerifyEnforce = "enforce"
)

// GatesSpec is a worker's acceptance-gate duties. A gate is a reference to a
// detected test suite (see kanban.Gate), never a command, so the harness runs
// it from its own table and decides it by the exit code.
type GatesSpec struct {
	// Require makes KanbanHandoff refuse a handoff that carries no gate: the
	// scout must say which outcome proves each task done.
	Require bool `json:"require,omitempty"`
	// Verify is off (default), record or enforce; see the Verify constants.
	Verify string `json:"verify,omitempty"`
	// Review gives the worker KanbanGate to decide a card's manual gates, and
	// has the harness hold it to them: under enforce, a card is done only when
	// every manual gate is met. It needs verify enforce. The worker that closes
	// cards (the reviewer) sets it.
	Review bool `json:"review,omitempty"`
	// Coverage has the worker record the clauses of a request card it plans
	// (KanbanContract), gives every handoff the clauses it covers, and has the
	// harness file a gap card for a clause no handoff covers. It applies to a
	// request card only, not to one the harness seeded or the worker surveyed.
	Coverage bool `json:"coverage,omitempty"`
	// Draft has the fast model draft manual gates for a card that has none when
	// the worker claims it, so a reviewer has criteria to check it against. The
	// drafted gates are always manual; the harness never drafts a runnable one.
	Draft bool `json:"draft,omitempty"`
}

// VerifyMode returns the effective verification mode.
func (g *GatesSpec) VerifyMode() string {
	if g == nil || g.Verify == "" {
		return VerifyOff
	}
	return g.Verify
}

func (g *GatesSpec) validate(k *KanbanSpec) error {
	switch g.Verify {
	case "", VerifyOff, VerifyRecord, VerifyEnforce:
	default:
		return fmt.Errorf("kanban.gates.verify must be off, record or enforce, not %q", g.Verify)
	}
	if g.Coverage && len(k.HandoffTo) == 0 && len(k.HandoffLabels) == 0 {
		return errors.New("kanban.gates.coverage needs handoff_to or handoff_labels: a request is covered by the tasks a worker hands on")
	}
	if g.Review && g.Verify != VerifyEnforce {
		return errors.New("kanban.gates.review needs kanban.gates.verify enforce: manual gates are held to only when the harness enforces the card's gates")
	}
	if g.Require && len(k.HandoffTo) == 0 && len(k.HandoffLabels) == 0 {
		return errors.New("kanban.gates.require needs handoff_to or handoff_labels: gates are required on the tasks a worker hands on")
	}
	return nil
}
