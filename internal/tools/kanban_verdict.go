package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/vex"
)

// KanbanVerdictName is the verdict tool for findings.
const KanbanVerdictName = "KanbanVerdict"

// Limits on what a verdict carries. Each string is cleaned to one line and
// capped, so a verdict is a small record, never a report.
const (
	MaxVerdictItems     = 5
	maxVerdictJustify   = 400
	maxVerdictItemBytes = 160
)

// VerdictRecord is the verdict a worker recorded on its claim. The harness
// reads it after the turn to route the item and, for the verifier, to write
// the VEX. Every string is already cleaned.
type VerdictRecord struct {
	Verdict       kanban.Verdict
	Justification string
	Evidence      []string
	Tried         []string
	VEXReason     vex.Justification // false_positive: the OpenVEX justification
}

// Recorded returns the verdict the worker recorded this claim, if any. A
// later call replaces an earlier one.
func (c *WorkerClaim) Recorded() (VerdictRecord, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.verdict == nil {
		return VerdictRecord{}, false
	}
	r := *c.verdict
	r.Evidence, r.Tried = slices.Clone(r.Evidence), slices.Clone(r.Tried)
	return r, true
}

func (c *WorkerClaim) record(r VerdictRecord) {
	c.mu.Lock()
	c.verdict = &r
	c.mu.Unlock()
}

// KanbanVerdict records a security worker's conclusion about the finding on
// the item it holds. It is bound to the claim: it takes no item id, sets no
// list and writes only the claimed item. The harness routes the card from the
// recorded verdict when the turn ends.
type KanbanVerdict struct{ KanbanBase }

// Definition describes the tool for the verdicts this claim may record.
func (t KanbanVerdict) Definition() Definition {
	var verdicts []string
	if t.Claim != nil {
		verdicts = slices.Clone(t.Claim.Verdicts)
	}
	desc := "Record your verdict on the finding of the item you hold. " +
		"fixed: the finding is remediated and the scanner no longer reports it. " +
		"false_positive: the finding does not apply here; give evidence anyone can check independently (file:line, a command and its result, a link). " +
		"no_fix: no fix is known; list what you tried. " +
		"needs_human: a person must decide. " +
		"The harness moves the card and files the VEX from your verdict; you never move it yourself."
	if slices.Contains(verdicts, string(kanban.VerdictRejected)) {
		desc += " rejected: the earlier claim does not hold up; say what you found."
	}
	item := &Property{Type: "string"}
	props := map[string]Property{
		"verdict":       {Type: "string", Enum: verdicts, Description: "Your verdict."},
		"justification": {Type: "string", Description: "Why, in a few sentences."},
		"evidence":      {Type: "array", Items: item, Description: "Up to five checkable items: file:line, a command and what it printed, or a link."},
		"tried":         {Type: "array", Items: item, Description: "Up to five things you tried, for no_fix."},
		"vex_reason": {Type: "string", Enum: vex.Justifications(),
			Description: "For false_positive: the OpenVEX justification that fits."},
	}
	return Definition{Name: KanbanVerdictName, Description: desc, Properties: props, Required: []string{"verdict", "justification"}}
}

// Kind is the harness-composed confirmation.
func (KanbanVerdict) Kind() Kind { return KindKanbanWrite }

// Execute records the verdict.
func (t KanbanVerdict) Execute(ctx context.Context, args map[string]any) (Result, error) {
	c := t.Claim
	if c == nil || len(c.Verdicts) == 0 {
		return Result{}, errors.New("KanbanVerdict is only available to a worker that holds a claim and lists kanban.security.verdicts")
	}
	raw, _ := argString(args, "verdict")
	v := kanban.Verdict(strings.TrimSpace(raw))
	if !slices.Contains(c.Verdicts, string(v)) {
		return Result{}, fmt.Errorf("verdict must be one of: %s", strings.Join(c.Verdicts, ", "))
	}
	just, _ := argString(args, "justification")
	just = oneLine(just, maxVerdictJustify)
	if just == "" {
		return Result{}, errors.New("a verdict needs a justification")
	}
	evidence, err := verdictList(args, "evidence")
	if err != nil {
		return Result{}, err
	}
	tried, err := verdictList(args, "tried")
	if err != nil {
		return Result{}, err
	}
	rec := VerdictRecord{Verdict: v, Justification: just, Evidence: evidence, Tried: tried}
	switch v {
	case kanban.VerdictFalsePositive:
		if len(evidence) == 0 {
			return Result{}, errors.New("a false positive needs evidence anyone can check independently")
		}
		reason, _ := argString(args, "vex_reason")
		rec.VEXReason = vex.Justification(strings.TrimSpace(reason))
		if c.VEX && !rec.VEXReason.Valid() {
			return Result{}, fmt.Errorf("a false positive needs vex_reason, one of: %s", strings.Join(vex.Justifications(), ", "))
		}
		if !rec.VEXReason.Valid() {
			rec.VEXReason = ""
		}
	case kanban.VerdictNoFix:
		if len(tried) == 0 {
			return Result{}, errors.New("no_fix needs the list of what you tried")
		}
	}
	if err := t.checkWorkerTarget(c.Item); err != nil {
		return Result{}, err
	}
	it, err := t.Store.SetVerdict(c.Item, c.Worker, v, verdictNote(rec), t.prov().SessionID)
	if err != nil {
		return Result{}, kanbanErr(err, c.Item)
	}
	c.record(rec)
	return kanbanWrite("verdict", it, fmt.Sprintf("%s verdict recorded: %s. The harness routes the card when you finish.", it.Short(), v)), nil
}

// verdictNote is the history note a verdict leaves for the next worker.
func verdictNote(r VerdictRecord) string {
	var b strings.Builder
	b.WriteString("verdict " + string(r.Verdict) + ": " + r.Justification)
	if len(r.Evidence) > 0 {
		b.WriteString(" | evidence: " + strings.Join(r.Evidence, "; "))
	}
	if len(r.Tried) > 0 {
		b.WriteString(" | tried: " + strings.Join(r.Tried, "; "))
	}
	return b.String()
}

// oneLine cleans model text to one line of at most max bytes.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(kanban.CleanBody(s, max*2)), " ")
	if len(s) > max {
		s = strings.TrimSpace(kanban.CleanBody(s, max))
	}
	return s
}

func verdictList(args map[string]any, key string) ([]string, error) {
	items, err := argStrings(args, key)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, s := range items {
		if s = oneLine(s, maxVerdictItemBytes); s != "" {
			out = append(out, s)
		}
	}
	if len(out) > MaxVerdictItems {
		return nil, fmt.Errorf("%s: at most %d items", key, MaxVerdictItems)
	}
	return out, nil
}

var _ Tool = KanbanVerdict{}
