package agent

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/sanitize"
)

// recommendedSuffix marks the option the harness expects the user to pick.
const recommendedSuffix = clarify.RecommendedSuffix

// maxOptionLabel is clarify's label cap, in runes.
const maxOptionLabel = 80

// recommendedMarkRe finds a "recommended" marker the model wrote in a label,
// in any case, in parentheses or brackets.
var recommendedMarkRe = regexp.MustCompile(`(?i)\s*[\(\[]\s*recommended\s*[\)\]]`)

// orderOptions puts the option the user is most likely to choose first in
// every group, and marks it (Recommended) when the choice is clear. The user's
// answers refer to option positions, so this runs before the questionnaire is
// shown, recorded or sent anywhere: every consumer sees one order.
//
// The harness is the only source of the marker. A "(recommended)" the model
// wrote into a label is stripped, and if the decision backend is off or
// unavailable, the model's own choice is kept, moved first and marked in the
// canonical form, so a model cannot make two options look recommended and the
// marker always reads the same. The number of options, their wording and the
// question are never changed.
func (s *Session) orderOptions(ctx context.Context, q clarify.Questionnaire) clarify.Questionnaire {
	out := clarify.Questionnaire{Groups: make([]clarify.Group, len(q.Groups))}
	modelPick := make([]int, len(q.Groups))
	for i, g := range q.Groups {
		g.Options = append([]clarify.Option(nil), g.Options...)
		modelPick[i] = stripRecommended(&g)
		out.Groups[i] = g
	}

	ordered := make([]bool, len(out.Groups))
	var model string
	start := time.Now()
	if s.jev != nil && s.jev.Enabled(config.JevOptionOrder) {
		var wg sync.WaitGroup
		var mu sync.Mutex
		sem := make(chan struct{}, 3)
		for i := range out.Groups {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()
				res, ok := s.pickOrder(ctx, out.Groups[i])
				if !ok {
					return
				}
				mu.Lock()
				out.Groups[i] = applyOrder(out.Groups[i], res)
				ordered[i] = true
				model = res.Identity
				mu.Unlock()
			}(i)
		}
		wg.Wait()
	}

	done := 0
	for i := range out.Groups {
		if ordered[i] {
			done++
			continue
		}
		// Fall back to the model's own pick, normalised.
		if modelPick[i] >= 0 {
			out.Groups[i] = moveFirst(out.Groups[i], modelPick[i])
			out.Groups[i] = markFirst(out.Groups[i])
		}
	}
	switch {
	case s.jev == nil || !s.jev.Enabled(config.JevOptionOrder):
		// The job is off: nothing to record beyond the normalisation.
	case done == len(out.Groups):
		rolemanager.RecordOptionOrder("ordered", len(out.Groups), model, time.Since(start))
	default:
		rolemanager.RecordOptionOrder("fallback", len(out.Groups)-done, model, time.Since(start))
	}
	return out
}

// pickOrder asks the decision backend how likely each option is to be chosen.
func (s *Session) pickOrder(ctx context.Context, g clarify.Group) (jev.PickResult, bool) {
	opts := make([]jev.ScoreItem, len(g.Options))
	for i, o := range g.Options {
		label := o.Label
		if o.Description != "" {
			label += ": " + o.Description
		}
		opts[i] = jev.ScoreItem{ID: "o" + strconv.Itoa(i+1), Label: sanitize.ForDecision(label, 240)}
	}
	res, err := s.jev.PickOptions(ctx, g.Context, s.turnPrompt, opts)
	if err != nil {
		return jev.PickResult{}, false
	}
	return res, true
}

// applyOrder reorders a group by the backend's probabilities (highest first,
// ties keeping the model's order) and marks the first option when the choice
// is clear. A multi-select group is ordered but never marked: there is no one
// option to recommend.
func applyOrder(g clarify.Group, res jev.PickResult) clarify.Group {
	byID := map[string]clarify.Option{}
	for i, o := range g.Options {
		byID["o"+strconv.Itoa(i+1)] = o
	}
	ids := orderedIDs(res, len(g.Options))
	opts := make([]clarify.Option, 0, len(ids))
	for _, id := range ids {
		opts = append(opts, byID[id])
	}
	g.Options = opts
	if !g.Multi && clearWinner(res.Probs, ids) {
		g = markFirst(g)
	}
	return g
}

// orderedIDs lists option ids highest probability first; ties, and any option
// the backend did not score, keep the model's order.
func orderedIDs(res jev.PickResult, n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = "o" + strconv.Itoa(i+1)
	}
	// Insertion sort keeps the sort stable for the handful of options.
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && res.Probs[ids[j]] > res.Probs[ids[j-1]]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	return ids
}

// clearWinner reports that the first option is clearly the likeliest: it holds
// at least half the probability with a lead of at least a tenth, or leads the
// runner-up by a quarter.
func clearWinner(probs map[string]float64, ids []string) bool {
	if len(ids) < 2 {
		return false
	}
	p1, p2 := probs[ids[0]], probs[ids[1]]
	return (p1 >= 0.5 && p1-p2 >= 0.1) || p1-p2 >= 0.25
}

// stripRecommended removes every "(recommended)" marker from a group's labels
// and returns the index of the option that carried one (the first, if several
// did), or -1.
func stripRecommended(g *clarify.Group) int {
	pick := -1
	for i, o := range g.Options {
		if recommendedMarkRe.MatchString(o.Label) {
			if pick < 0 {
				pick = i
			}
			g.Options[i].Label = strings.TrimSpace(recommendedMarkRe.ReplaceAllString(o.Label, ""))
		}
	}
	return pick
}

// moveFirst moves option i to the front, keeping the others in order.
func moveFirst(g clarify.Group, i int) clarify.Group {
	if i <= 0 || i >= len(g.Options) {
		return g
	}
	opts := make([]clarify.Option, 0, len(g.Options))
	opts = append(opts, g.Options[i])
	opts = append(opts, g.Options[:i]...)
	opts = append(opts, g.Options[i+1:]...)
	g.Options = opts
	return g
}

// markFirst appends the canonical marker to the first option's label, trimming
// the label so the result still fits the label cap.
func markFirst(g clarify.Group) clarify.Group {
	if len(g.Options) == 0 {
		return g
	}
	label := g.Options[0].Label
	room := maxOptionLabel - utf8.RuneCountInString(recommendedSuffix)
	if utf8.RuneCountInString(label) > room {
		label = strings.TrimSpace(string([]rune(label)[:room]))
	}
	g.Options[0].Label = label + recommendedSuffix
	return g
}
