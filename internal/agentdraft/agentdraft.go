// Package agentdraft drafts an agent profile from a one-line premise: the
// model proposes a value for each field, with a reason, and the user takes,
// edits or drops each offer. It backs the website's agent builder (a web
// request the host claims through its session-sync inbox) and
// `belai agent draft`.
//
// The draft runs on the role-manager classifier: a tool-less call with no
// skills and no agent block, like every classifier turn. What reaches the
// model is the cleaned premise plus harness facts (tool names, worker profile
// names and the labels they claim, board labels). Every value in the reply is
// shape-checked against the profile schema and cleaned before it is offered;
// anything malformed is dropped rather than coerced. The draft never offers a
// protection switch (guardrails, ask_permission): relaxing one stays a
// deliberate choice the user makes by hand.
//
// Crew offers are not drafted by the model. They are computed from the
// drafted routes and the host's own worker profiles and crews, so they only
// ever name workers and crews that exist.
package agentdraft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/sanitize"
)

// Fields is every profile path a draft may fill, in the order the builder
// shows them. It is the same list the website accepts (DRAFT_FIELDS in
// website src/composables/useBelaiAgentBuilder.ts); a test pins it.
var Fields = []string{
	"name", "description", "identity", "system_prompt", "mode", "autonomy", "max_iterations", "reflection",
	"schedule", "monitor_condition", "effort", "tools", "kanban.lists", "kanban.labels", "kanban.on_success",
	"kanban.on_failure", "kanban.handoff_to", "kanban.handoff_labels", "kanban.max_attempts", "kanban.lease",
	"workspace.isolation", "workspace.publish", "budget.max_passes_per_item", "budget.max_wall_per_item",
	"memory.enabled",
}

// Required fields: a reply without them is sent back once more.
var required = []string{"name", "description", "system_prompt", "mode"}

// Limits on what a request and a reply may carry.
const (
	MaxPremiseChars = 2000
	maxWorkers      = 64
	maxLabels       = 128
	maxWhyChars     = 240
	maxCrewOffers   = 4
	defaultAttempts = 2
)

// Request is what to draft from. Workers and Labels are facts from the
// caller (the website's view of the board); the host adds its own workers.
type Request struct {
	Premise string   `json:"premise"`
	Workers []string `json:"workers,omitempty"`
	Labels  []string `json:"labels,omitempty"`
}

// Field is one offer: the proposed value and why.
type Field struct {
	Value any    `json:"value"`
	Why   string `json:"why,omitempty"`
}

// Route is a kanban destination, as the profile's on_success/on_failure.
type Route struct {
	List       string   `json:"list"`
	Labels     []string `json:"labels"`
	DropLabels []string `json:"drop_labels"`
}

// CrewMember and Crew mirror agentprofile.Member and Crew for the wire.
type CrewMember struct {
	Profile  string `json:"profile"`
	Replicas int    `json:"replicas"`
}

// Crew is a proposed crew.
type Crew struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Members     []CrewMember `json:"members"`
}

// Crew offer kinds.
const (
	CrewNew     = "new_crew"
	CrewFillGap = "fill_gap"
)

// CrewOffer proposes a crew for the drafted agent, or points at a label that
// nothing claims.
type CrewOffer struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Why   string `json:"why,omitempty"`
	Crew  *Crew  `json:"crew,omitempty"`
}

// Result is a finished draft.
type Result struct {
	Fields map[string]Field `json:"fields"`
	Crews  []CrewOffer      `json:"crews"`
}

// Worker is what the drafter knows about an existing worker profile: its
// name and the labels it claims and routes to. Harness facts only.
type Worker struct {
	Name   string
	Lists  []string
	Labels []string
	// Sends is every label its routes and handoffs put on an item.
	Sends []string
}

// WorkersFrom turns profiles into the facts the drafter uses; non-workers
// are skipped.
func WorkersFrom(ps []agentprofile.AgentProfile) []Worker {
	var out []Worker
	for _, p := range ps {
		if p.Mode != agentprofile.ModeWorker || p.Kanban == nil {
			continue
		}
		k := p.Kanban
		var sends []string
		if k.OnSuccess.List != "done" {
			sends = append(sends, k.OnSuccess.Labels...)
		}
		if k.OnFailure.List != "done" {
			sends = append(sends, k.OnFailure.Labels...)
		}
		sends = append(sends, k.HandoffLabels...)
		lists := k.Lists
		if len(lists) == 0 {
			lists = []string{string(kanban.Backlog)}
		}
		out = append(out, Worker{Name: p.Name, Lists: lists, Labels: kanban.NormLabels(k.Labels), Sends: kanban.NormLabels(sends)})
	}
	return out
}

// Drafter drafts profiles. Workers and Crews, when set, return the host's
// worker profiles and crews for crew offers; nil means none.
type Drafter struct {
	Classifier  rolemanager.Classifier
	MaxAttempts int
	Caveman     bool
	Workers     func() []Worker
	Crews       func() []agentprofile.Crew
}

// ErrEmptyPremise is returned for a premise that is empty after cleaning.
var ErrEmptyPremise = errors.New("the premise is empty")

// ErrPremiseTooLong is returned for a premise over MaxPremiseChars.
var ErrPremiseTooLong = fmt.Errorf("the premise is over %d characters", MaxPremiseChars)

// ErrNoClassifier means the host has no classifier model to draft with.
var ErrNoClassifier = errors.New("no classifier model is configured on this host")

// ErrModelCall wraps a failed model call.
var ErrModelCall = errors.New("the draft model call failed")

// ErrUnusable wraps the last reason no attempt produced a usable draft.
var ErrUnusable = errors.New("no usable draft")

// Draft asks the classifier for field offers and computes crew offers. It
// fails only when no attempt produced the required fields.
func (d *Drafter) Draft(ctx context.Context, req Request) (Result, error) {
	if d.Classifier == nil {
		return Result{}, ErrNoClassifier
	}
	premise := CleanText(req.Premise, MaxPremiseChars+1)
	if premise == "" {
		return Result{}, ErrEmptyPremise
	}
	if utf8.RuneCountInString(premise) > MaxPremiseChars {
		return Result{}, ErrPremiseTooLong
	}
	var workers []Worker
	if d.Workers != nil {
		workers = d.Workers()
	}
	names := identifiers(append(req.Workers, workerNames(workers)...), maxWorkers)
	labels := identifiers(req.Labels, maxLabels)

	attempts := d.MaxAttempts
	if attempts <= 0 {
		attempts = defaultAttempts
	}
	system := rolemanager.CavemanProse(SystemPrompt(names, labels), d.Caveman)
	user := "Premise: " + sanitize.Sanitize(premise)
	var last error
	for attempt := 1; attempt <= attempts; attempt++ {
		raw, err := d.Classifier.Classify(ctx, rolemanager.ClassifierPayload{
			System:    system,
			User:      user,
			MaxTokens: rolemanager.ClassifierStructuredMaxTokens,
		})
		if err != nil {
			return Result{}, fmt.Errorf("%w: %w", ErrModelCall, err)
		}
		fields, err := ParseReply(raw)
		if err == nil {
			if missing := missingRequired(fields); len(missing) > 0 {
				err = fmt.Errorf("missing or invalid required fields: %s", strings.Join(missing, ", "))
			}
		}
		if err == nil {
			res := Result{Fields: fields}
			var crews []agentprofile.Crew
			if d.Crews != nil {
				crews = d.Crews()
			}
			res.Crews = CrewOffers(fields, workers, labels, crews)
			return res, nil
		}
		last = err
		user = "Premise: " + sanitize.Sanitize(premise) + "\n\nYour last reply was not usable: " +
			sanitize.Sanitize(err.Error()) + ". Reply with only the JSON object."
	}
	return Result{}, fmt.Errorf("%w after %d attempts: %w", ErrUnusable, attempts, last)
}

func workerNames(ws []Worker) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.Name)
	}
	return out
}

// SystemPrompt is the drafter's instruction. It carries harness facts only.
func SystemPrompt(workers, labels []string) string {
	var b strings.Builder
	b.WriteString(`You design agent profiles for Belai, a secure coding harness. From the user's premise, propose a value for each field below and a short reason for each.

Reply with ONLY a JSON object, no prose and no Markdown fences:
{"fields": {"<field>": {"value": <value>, "why": "<one short sentence>"}, ...}}

Fields (omit any you have no good reason to set):
- name: string, [a-zA-Z0-9._-], must not start with "belai:"
- description: one sentence saying what the agent does
- identity: one or two sentences: who the agent is and how it works
- system_prompt: the agent's full instructions, specific to the premise
- mode: "single" | "loop" | "scheduled" | "monitor" | "worker". Use "worker" when the agent picks up kanban items and hands them on.
- autonomy: "supervised" | "autonomous"
- max_iterations: integer 0-1000
- reflection: true | false
- schedule: a 5-field cron string or an interval such as "1h", only for scheduled (or a worker that should only look for work then)
- monitor_condition: string, only for monitor
- effort: "low" | "medium" | "high" | "none"
- tools: array of tool names from the list below; give the fewest the premise needs. Write, Edit and Bash change things: only grant them when the agent must.
- kanban.lists: array of "backlog" and/or "review" (workers only)
- kanban.labels: array of lower-case labels an item must carry (workers only). Pick a label no existing worker claims unless the agent should share that work.
- kanban.on_success / kanban.on_failure: {"list": "backlog"|"review"|"in_progress"|"blocked"|"done", "labels": [...], "drop_labels": [...]} (workers only)
- kanban.handoff_to: array of worker profile names this agent may file new items for
- kanban.handoff_labels: array of labels those items carry
- kanban.max_attempts: integer
- kanban.lease: Go duration from 1m to 2h, e.g. "20m"
- workspace.isolation: "worktree" | "shared" | "none". A worker that writes (Write, Edit or Bash) needs worktree or shared.
- workspace.publish: "none" | "draft_pr" | "agent" (needs worktree)
- budget.max_passes_per_item: integer; an autonomous worker needs one
- budget.max_wall_per_item: Go duration, e.g. "30m"
- memory.enabled: true | false

Never propose guardrails or ask_permission: those are the user's to change.
`)
	b.WriteString("\nKnown tools: ")
	b.WriteString(strings.Join(agentprofile.KnownTools(), ", "))
	if len(workers) > 0 {
		b.WriteString("\nExisting worker profiles: ")
		b.WriteString(strings.Join(workers, ", "))
	}
	if len(labels) > 0 {
		b.WriteString("\nLabels on the board: ")
		b.WriteString(strings.Join(labels, ", "))
	}
	b.WriteString("\n")
	return b.String()
}

var thinking = regexp.MustCompile(`(?s)<thinking>.*?</thinking>`)

// ParseReply reads the model's reply into cleaned field offers. Fields the
// schema does not know, and values that fail their field's shape, are dropped.
func ParseReply(raw string) (map[string]Field, error) {
	s := thinking.ReplaceAllString(raw, "")
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	if i := strings.LastIndex(s, "}"); i >= 0 && i < len(s)-1 {
		s = s[:i+1]
	}
	var reply struct {
		Fields map[string]struct {
			Value json.RawMessage `json:"value"`
			Why   string          `json:"why"`
		} `json:"fields"`
	}
	if err := json.Unmarshal([]byte(s), &reply); err != nil {
		return nil, errors.New("the reply was not a JSON object")
	}
	if reply.Fields == nil {
		return nil, errors.New(`the reply had no "fields" object`)
	}
	out := map[string]Field{}
	for _, name := range Fields {
		f, ok := reply.Fields[name]
		if !ok {
			continue
		}
		v, ok := CleanValue(name, f.Value)
		if !ok {
			continue
		}
		out[name] = Field{Value: v, Why: CleanText(f.Why, maxWhyChars)}
	}
	return out, nil
}

func missingRequired(fields map[string]Field) []string {
	var out []string
	for _, r := range required {
		if _, ok := fields[r]; !ok {
			out = append(out, r)
		}
	}
	return out
}

var nameShape = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,64}$`)

// CleanValue checks one field's value against the profile schema and returns
// the cleaned value. ok is false when it does not fit.
func CleanValue(field string, raw json.RawMessage) (any, bool) {
	str := func(max int) (string, bool) {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", false
		}
		s = CleanText(s, max)
		return s, s != ""
	}
	oneOf := func(allowed ...string) (any, bool) {
		s, ok := str(32)
		return s, ok && slices.Contains(allowed, s)
	}
	integer := func(max int) (any, bool) {
		var n int
		if json.Unmarshal(raw, &n) != nil || n < 0 || n > max {
			return nil, false
		}
		return n, true
	}
	boolean := func() (any, bool) {
		var b bool
		if json.Unmarshal(raw, &b) != nil {
			return nil, false
		}
		return b, true
	}
	labels := func() (any, bool) {
		var in []string
		if json.Unmarshal(raw, &in) != nil {
			return nil, false
		}
		return normLabels(in), true
	}
	duration := func(min, max time.Duration) (any, bool) {
		s, ok := str(20)
		if !ok {
			return nil, false
		}
		d, err := time.ParseDuration(s)
		return s, err == nil && d >= min && (max == 0 || d <= max)
	}

	switch field {
	case "name":
		s, ok := str(64)
		return s, ok && nameShape.MatchString(s) && !agentprofile.IsBuiltin(s)
	case "description":
		return str(1000)
	case "identity":
		return str(1000)
	case "system_prompt":
		return str(8000)
	case "monitor_condition":
		return str(1000)
	case "schedule":
		s, ok := str(120)
		if !ok {
			return nil, false
		}
		// A cron expression, or the older interval form (a Go duration).
		if _, isCron, err := agentprofile.CronSchedule(s); isCron {
			return s, err == nil
		}
		d, err := time.ParseDuration(s)
		return s, err == nil && d > 0
	case "mode":
		return oneOf(agentprofile.ModeSingle, agentprofile.ModeLoop, agentprofile.ModeScheduled, agentprofile.ModeMonitor, agentprofile.ModeWorker)
	case "autonomy":
		return oneOf(agentprofile.AutonomySupervised, agentprofile.AutonomyAutonomous)
	case "effort":
		return oneOf(agentprofile.ValidEfforts()...)
	case "workspace.isolation":
		return oneOf(agentprofile.IsolationWorktree, agentprofile.IsolationShared, agentprofile.IsolationNone)
	case "workspace.publish":
		return oneOf(agentprofile.PublishNone, agentprofile.PublishDraftPR, agentprofile.PublishAgent)
	case "max_iterations", "kanban.max_attempts", "budget.max_passes_per_item":
		return integer(1000)
	case "reflection", "memory.enabled":
		return boolean()
	case "tools":
		var in []string
		if json.Unmarshal(raw, &in) != nil {
			return nil, false
		}
		out := []string{}
		for _, t := range in {
			t = strings.TrimSpace(t)
			if agentprofile.KnownTool(t) && !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
		return out, true
	case "kanban.lists":
		var in []string
		if json.Unmarshal(raw, &in) != nil {
			return nil, false
		}
		out := []string{}
		for _, l := range in {
			if pl, ok := kanban.ParseList(l); ok && slices.Contains(kanban.ClaimableLists, pl) && !slices.Contains(out, string(pl)) {
				out = append(out, string(pl))
			}
		}
		return out, len(out) > 0
	case "kanban.labels", "kanban.handoff_labels":
		return labels()
	case "kanban.handoff_to":
		var in []string
		if json.Unmarshal(raw, &in) != nil {
			return nil, false
		}
		out := []string{}
		for _, a := range in {
			if c, err := kanban.CleanAssignee(a); err == nil && c != "" && !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
		return out, true
	case "kanban.on_success", "kanban.on_failure":
		var r struct {
			List       string   `json:"list"`
			Labels     []string `json:"labels"`
			DropLabels []string `json:"drop_labels"`
		}
		if json.Unmarshal(raw, &r) != nil {
			return nil, false
		}
		pl, ok := kanban.ParseList(r.List)
		if !ok {
			return nil, false
		}
		return Route{List: string(pl), Labels: normLabels(r.Labels), DropLabels: normLabels(r.DropLabels)}, true
	case "kanban.lease":
		return duration(kanban.MinLease, kanban.MaxLease)
	case "budget.max_wall_per_item":
		return duration(time.Second, 0)
	}
	return nil, false
}

func normLabels(in []string) []string {
	out := kanban.NormLabels(in)
	if out == nil {
		out = []string{}
	}
	return out
}

// CleanText strips delimiter markup, ANSI escapes and control and bidi runes
// (newlines and tabs stay), trims, and caps at max runes.
func CleanText(s string, max int) string {
	s = sanitize.Sanitize(s)
	s = ansi.ReplaceAllString(s, "")
	var b strings.Builder
	n := 0
	for _, r := range s {
		if r != '\n' && r != '\t' && (unicode.IsControl(r) || isBidi(r)) {
			continue
		}
		if n == max {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func isBidi(r rune) bool {
	return r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

// identifiers keeps well-formed names and labels, deduplicated, capped.
func identifiers(in []string, max int) []string {
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if !identShape.MatchString(s) || slices.Contains(out, s) {
			continue
		}
		out = append(out, s)
		if len(out) == max {
			break
		}
	}
	return out
}

var identShape = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// ── Crew offers ──────────────────────────────────────────────────────────

func stringsOf(fields map[string]Field, key string) []string {
	if v, ok := fields[key].Value.([]string); ok {
		return v
	}
	return nil
}

func routeOf(fields map[string]Field, key string) Route {
	r, _ := fields[key].Value.(Route)
	return r
}

// CrewOffers proposes crews for a drafted worker, computed from its routes
// and the host's workers and crews:
//   - a copy of each crew one of whose members feeds this agent (routes or
//     hands off a label it claims) or is fed by it, with this agent added;
//   - a gap for each label this agent sends, or that sits on the board,
//     which no worker claims.
//
// It proposes nothing for an agent that is not a worker.
func CrewOffers(fields map[string]Field, workers []Worker, boardLabels []string, crews []agentprofile.Crew) []CrewOffer {
	if fields["mode"].Value != agentprofile.ModeWorker {
		return []CrewOffer{}
	}
	name, _ := fields["name"].Value.(string)
	claims := stringsOf(fields, "kanban.labels")
	var sends []string
	for _, key := range []string{"kanban.on_success", "kanban.on_failure"} {
		if r := routeOf(fields, key); r.List != "" && r.List != string(kanban.Done) {
			sends = append(sends, r.Labels...)
		}
	}
	sends = append(sends, stringsOf(fields, "kanban.handoff_labels")...)

	byName := map[string]Worker{}
	for _, w := range workers {
		byName[w.Name] = w
	}
	related := func(w Worker) (string, bool) {
		if l := firstShared(w.Sends, claims); l != "" {
			return fmt.Sprintf("%s sends items labelled %s, which %s claims", w.Name, l, name), true
		}
		if l := firstShared(sends, w.Labels); l != "" {
			return fmt.Sprintf("%s sends items labelled %s, which %s claims", name, l, w.Name), true
		}
		return "", false
	}

	out := []CrewOffer{}
	for _, c := range crews {
		if len(out) == maxCrewOffers || len(c.Members) >= agentprofile.MaxCrewMembers {
			continue
		}
		for _, m := range c.Members {
			w, ok := byName[m.Profile]
			if !ok || m.Profile == name {
				continue
			}
			why, ok := related(w)
			if !ok {
				continue
			}
			crew := Crew{
				Name:        crewName(c.Name, name),
				Description: CleanText(c.Description+", with "+name, 240),
			}
			for _, cm := range c.Members {
				crew.Members = append(crew.Members, CrewMember{Profile: cm.Profile, Replicas: cm.Count()})
			}
			crew.Members = append(crew.Members, CrewMember{Profile: name, Replicas: 1})
			out = append(out, CrewOffer{Kind: CrewNew, Title: "New crew: " + crew.Name, Why: CleanText(why, maxWhyChars), Crew: &crew})
			break
		}
	}

	claimed := map[string]bool{}
	for _, w := range workers {
		for _, l := range w.Labels {
			claimed[l] = true
		}
	}
	for _, l := range claims {
		claimed[l] = true
	}
	gaps := map[string]string{}
	for _, l := range sends {
		if !claimed[l] {
			gaps[l] = fmt.Sprintf("%s sends items labelled %s, and no worker claims them", name, l)
		}
	}
	for _, l := range normLabels(boardLabels) {
		if _, seen := gaps[l]; !seen && !claimed[l] {
			gaps[l] = fmt.Sprintf("items labelled %s are on the board, and no worker claims them", l)
		}
	}
	keys := make([]string, 0, len(gaps))
	for l := range gaps {
		keys = append(keys, l)
	}
	sort.Strings(keys)
	for _, l := range keys {
		if len(out) == maxCrewOffers {
			break
		}
		out = append(out, CrewOffer{Kind: CrewFillGap, Title: "Nothing claims " + l, Why: CleanText(gaps[l], maxWhyChars)})
	}
	return out
}

func firstShared(a, b []string) string {
	for _, x := range a {
		if slices.Contains(b, x) {
			return x
		}
	}
	return ""
}

// crewName is "<crew>-<agent>", without the built-in prefix, in the
// characters a crew name may use.
func crewName(crew, agent string) string {
	n := strings.TrimPrefix(crew, agentprofile.BuiltinPrefix) + "-" + agent
	n = strings.Map(func(r rune) rune {
		if r < utf8.RuneSelf && (r == '.' || r == '_' || r == '-' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return r
		}
		return '-'
	}, n)
	if len(n) > 64 {
		n = n[:64]
	}
	return strings.Trim(n, ".-_")
}

// ── Applying a draft ─────────────────────────────────────────────────────

// Apply builds a profile with every offer taken. It is what
// `belai agent draft` writes; the website does the same per offer.
func Apply(fields map[string]Field) agentprofile.AgentProfile {
	p := agentprofile.AgentProfile{Mode: agentprofile.ModeSingle, Autonomy: agentprofile.AutonomySupervised}
	str := func(k string) string { s, _ := fields[k].Value.(string); return s }
	num := func(k string) int { n, _ := fields[k].Value.(int); return n }
	flag := func(k string) bool { b, _ := fields[k].Value.(bool); return b }
	p.Name, p.Description, p.Identity, p.SystemPrompt = str("name"), str("description"), str("identity"), str("system_prompt")
	if m := str("mode"); m != "" {
		p.Mode = m
	}
	if a := str("autonomy"); a != "" {
		p.Autonomy = a
	}
	p.MaxIterations, p.Reflection, p.Effort = num("max_iterations"), flag("reflection"), str("effort")
	p.Schedule, p.MonitorCondition = str("schedule"), str("monitor_condition")
	if _, ok := fields["tools"]; ok {
		p.Tools = stringsOf(fields, "tools")
	}
	if p.Mode != agentprofile.ModeWorker {
		return p
	}
	k := &agentprofile.KanbanSpec{
		Lists: stringsOf(fields, "kanban.lists"), Labels: stringsOf(fields, "kanban.labels"),
		HandoffTo: stringsOf(fields, "kanban.handoff_to"), HandoffLabels: stringsOf(fields, "kanban.handoff_labels"),
		MaxAttempts: num("kanban.max_attempts"), Lease: str("kanban.lease"),
	}
	for key, dst := range map[string]*agentprofile.Route{"kanban.on_success": &k.OnSuccess, "kanban.on_failure": &k.OnFailure} {
		if r := routeOf(fields, key); r.List != "" {
			*dst = agentprofile.Route{List: r.List, Labels: r.Labels, DropLabels: r.DropLabels}
		}
	}
	p.Kanban = k
	if iso, pub := str("workspace.isolation"), str("workspace.publish"); iso != "" || pub != "" {
		p.Workspace = &agentprofile.WorkspaceSpec{Isolation: iso, Publish: pub}
	}
	if passes, wall := num("budget.max_passes_per_item"), str("budget.max_wall_per_item"); passes > 0 || wall != "" {
		p.Budget = &agentprofile.BudgetSpec{MaxPassesPerItem: passes, MaxWallPerItem: wall}
	}
	if flag("memory.enabled") {
		p.Memory = &agentprofile.MemorySpec{Enabled: true}
	}
	return p
}
