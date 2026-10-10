package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tools"
)

// AskDecider answers an ask in the user's place when its confidence is above the
// user's threshold (the clef decision server's engine, docs/mcp.md). It is not a
// Jev job: its switch and threshold are the user's own mcp.builtin.clef settings.
// Anything but a confident answer is an error or an unconfident one, and the ask
// goes to the user exactly as it would have.
type AskDecider interface {
	// AskPolicy returns the confidence at or above which a decision is used,
	// and whether decisions answer asks at all.
	AskPolicy() (threshold float64, on bool)
	// Choose scores options for question, one weight per option in order,
	// summing to 1.
	Choose(ctx context.Context, question, context string, options []string, descriptions map[string]string) ([]float64, error)
	// Judge returns P(true) of a proposition.
	Judge(ctx context.Context, proposition, context string) (float64, error)
}

// askDecideTimeout bounds one ask decision. A slow backend must not hold a turn
// that the user would have answered: past it the user is asked.
const askDecideTimeout = 8 * time.Second

// Ask kinds carried by EventAskDecidedKind.
const (
	AskKindPermission = "permission"
	AskKindClarify    = "clarify"
	AskKindModeChoice = "mode_choice"
	AskKindPlanReview = "plan_review"
)

// AskDecided reports an ask the decision model answered instead of the user. It
// holds harness facts only: the kind, the outcome word, the confidence and a
// count, never the question, the subject or an option.
type AskDecided struct {
	// Name is the tool of a permission ask (a harness identifier).
	Name string
	Kind string
	// Outcome is "allow" or "deny" for a permission ask and "answered" for a
	// questionnaire.
	Outcome string
	// Confidence is the lowest confidence among the decisions taken.
	Confidence float64
	// Decided and Groups say how many of a questionnaire's groups were
	// answered and how many it had (both 0 for a permission ask).
	Decided, Groups int
}

func (s *Session) askPolicy() (float64, bool) {
	if s.askDecider == nil {
		return 0, false
	}
	return s.askDecider.AskPolicy()
}

// decidePermissionAsk puts a tool-permission ask to the decision model. It
// reports decided when the model is confident enough that the call should (allow
// true) or should not (allow false) run. The model sees the tool name, the
// call's subject (a command line, a path, a URL) and the user's request, never
// the call's arguments or a diff preview. A decision never writes an allow rule.
func (s *Session) decidePermissionAsk(ctx context.Context, name, subject string, emit func(Event)) (allow, decided bool) {
	th, on := s.askPolicy()
	if !on || ctx.Err() != nil {
		return false, false
	}
	ctx, cancel := context.WithTimeout(ctx, askDecideTimeout)
	defer cancel()
	prop := fmt.Sprintf("The agent wants to run the tool %q on this target: %s. Is it appropriate to let it proceed without asking the user?",
		name, sanitize.Line(subject, 400))
	p, err := s.askDecider.Judge(ctx, prop, s.askContext()+s.approvedPlanFact(subject))
	if err != nil {
		return false, false
	}
	conf := p
	if 1-p > conf {
		conf = 1 - p
	}
	if conf < th {
		return false, false
	}
	allow = p >= 0.5
	out := "deny"
	if allow {
		out = "allow"
	}
	emit(Event{Kind: EventAskDecidedKind, Decided: &AskDecided{Kind: AskKindPermission, Name: name, Outcome: out, Confidence: conf}})
	return allow, true
}

// askContext is the context a decision sees: the user's request this turn and
// the mode, both already cleaned. It never carries a file, a tool result or an
// attachment.
func (s *Session) askContext() string {
	var b strings.Builder
	if s.turnPrompt != "" {
		b.WriteString("The user's request: " + sanitize.Line(s.turnPrompt, 1500) + "\n")
	}
	if s.planMode {
		b.WriteString("The agent is in plan mode: it may read but not change anything.\n")
	}
	return b.String()
}

// decideQuestions puts each single-select group of q to the decision model and
// returns the answers it is confident about, by group index. A group with
// several selections, fewer than two options, a failed decision or a weight
// below the threshold is left to the user.
func (s *Session) decideQuestions(ctx context.Context, q clarify.Questionnaire) (map[int]clarify.Answer, float64) {
	th, on := s.askPolicy()
	if !on || ctx.Err() != nil {
		return nil, 0
	}
	ctx, cancel := context.WithTimeout(ctx, askDecideTimeout)
	defer cancel()
	out := map[int]clarify.Answer{}
	min := 1.0
	for gi, g := range q.Groups {
		if g.Multi || len(g.Options) < 2 {
			continue
		}
		labels := make([]string, len(g.Options))
		desc := map[string]string{}
		for i, o := range g.Options {
			labels[i] = strings.TrimSuffix(o.Label, clarify.RecommendedSuffix)
			if o.Description != "" {
				desc[labels[i]] = o.Description
			}
		}
		w, err := s.askDecider.Choose(ctx, g.Context, s.askContext(), labels, desc)
		if err != nil || len(w) != len(labels) {
			continue
		}
		best := 0
		for i := range w {
			if w[i] > w[best] {
				best = i
			}
		}
		if w[best] < th {
			continue
		}
		if w[best] < min {
			min = w[best]
		}
		out[gi] = clarify.Answer{GroupIndex: gi, Chosen: []int{best}, Decided: true, Confidence: w[best]}
	}
	return out, min
}

// askQuestions is the questionnaire ask with the decision model in front: the
// groups it is confident about are answered at once, the rest go to the user (one
// questionnaire of the remaining groups) and the answers are merged back under
// their original group indexes.
func (s *Session) askQuestions(ctx context.Context, q clarify.Questionnaire, modeChoice bool, emit func(Event)) (clarify.Answers, bool) {
	decided, conf := s.decideQuestions(ctx, q)
	if len(decided) == 0 {
		return s.askUserDirect(ctx, q, modeChoice, emit)
	}
	kind := AskKindClarify
	if modeChoice {
		kind = AskKindModeChoice
	}
	emit(Event{Kind: EventAskDecidedKind, Decided: &AskDecided{Kind: kind, Outcome: "answered", Confidence: conf, Decided: len(decided), Groups: len(q.Groups)}})

	var rest clarify.Questionnaire
	var restIdx []int
	for gi, g := range q.Groups {
		if _, ok := decided[gi]; !ok {
			rest.Groups = append(rest.Groups, g)
			restIdx = append(restIdx, gi)
		}
	}
	items := make([]clarify.Answer, 0, len(q.Groups))
	for _, a := range decided {
		items = append(items, a)
	}
	if len(rest.Groups) > 0 {
		ans, ok := s.askUserDirect(ctx, rest, modeChoice, emit)
		if !ok {
			return clarify.Answers{}, false
		}
		for _, a := range ans.Items {
			if a.GroupIndex >= 0 && a.GroupIndex < len(restIdx) {
				a.GroupIndex = restIdx[a.GroupIndex]
				items = append(items, a)
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].GroupIndex < items[j].GroupIndex })
	return clarify.Answers{Items: items}, true
}

// compile-time check that the decision tools' kind is the one the surface
// treats as read-only (the engine itself lives in internal/clefmcp).
var _ = tools.KindDecision

// Line is the harness-composed line that says an ask was answered by the
// decision model. It holds the kind, the outcome, the tool name and numbers.
func (d *AskDecided) Line() string {
	what := "answered a question"
	switch d.Kind {
	case AskKindPermission:
		verb := map[string]string{"allow": "allowed", "deny": "denied"}[d.Outcome]
		if verb == "" {
			verb = "decided"
		}
		what = fmt.Sprintf("%s the %s tool", verb, sanitize.Line(d.Name, 64))
	case AskKindModeChoice:
		what = "chose the mode"
	case AskKindPlanReview:
		what = "decided the plan review: " + sanitize.Line(d.Outcome, 40)
	default:
		if d.Groups > 1 {
			what = fmt.Sprintf("answered %d of %d questions", d.Decided, d.Groups)
		}
	}
	return fmt.Sprintf("Clef %s (confidence %.2f), so the user was not asked", what, d.Confidence)
}

// approvedPlanFact is the one sentence a permission decision gets about an
// approved plan: the call's target is a file the plan lists. It is computed by
// the harness from the plan the user reviewed, never copied from model prose,
// and says nothing for a target the plan does not list, so a plan cannot talk
// the decision into allowing anything it did not name. Without it the decision
// saw only "Execute the approved plan." and denied the plan's own edits
// (measured in the TUI: Clef denied an Edit of a file the approved plan listed).
func (s *Session) approvedPlanFact(subject string) string {
	if !s.turnExecutePlan || len(s.turnPlanPaths) == 0 || strings.TrimSpace(subject) == "" {
		return ""
	}
	target := filepath.Clean(subject)
	for _, p := range s.turnPlanPaths {
		pp := filepath.Clean(p)
		if target == pp || (filepath.IsAbs(target) && !filepath.IsAbs(pp) && target == filepath.Join(s.workdir, pp)) {
			return "The user reviewed and approved a plan for this turn, and this call's target is one of the files that plan lists.\n"
		}
	}
	return ""
}
