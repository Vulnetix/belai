package agent

import (
	"context"
	"strconv"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/sanitize"
)

// Agent pick. In Auto mode the role manager decides the mode, and for a general
// request it may also decide which of the user's own agent profiles carries the
// turn. A decision backend ranks the profiles by their descriptions against the
// sanitized prompt, with a default option that means no profile.
//
// The job only chooses among profiles the user already wrote and would have
// been offered by the picker. It never changes the mode, a permission or a
// gate: a profile that lowers guardrails or ask is never offered, a built-in
// is never offered (the intents already own debug, fan-out and handoff, and
// the rest are background workers), and only a clear lead engages one. Any
// other outcome (job off, no backend, a timeout, an unanswered or
// inconclusive pick) runs the turn without a profile, as before.

// agentPickTimeout bounds the pick so a slow backend never delays the turn.
const agentPickTimeout = 8 * time.Second

// maxAgentOffers leaves one pick slot for the default option.
const maxAgentOffers = jev.MaxPickOptions - 1

// agentOffer is one profile on offer: its name and the description the backend
// reads.
type agentOffer struct {
	name, description string
}

// eligibleAgents filters the stored profiles to those Auto may engage: the
// user's own, foreground (single mode), described, and not lowering the
// session's posture. The order is the store's (by name).
func eligibleAgents(list []agentprofile.AgentProfile) []agentOffer {
	var out []agentOffer
	for _, p := range list {
		switch {
		case p.Builtin, p.Mode != agentprofile.ModeSingle, p.Description == "":
			continue
		case p.Guardrails != nil && !*p.Guardrails, p.AskPermission != nil && !*p.AskPermission:
			continue
		}
		out = append(out, agentOffer{name: p.Name, description: p.Description})
		if len(out) == maxAgentOffers {
			break
		}
	}
	return out
}

// pickAgent returns the profile that is a clear fit for the request, or "".
func (s *Session) pickAgent(ctx context.Context, request string) string {
	if s.jev == nil || !s.jev.Enabled(config.JevAgentPick) || request == "" {
		return ""
	}
	list, err := agentprofile.List()
	if err != nil {
		return ""
	}
	offers := eligibleAgents(list)
	if len(offers) == 0 {
		return ""
	}
	items := make([]jev.ScoreItem, len(offers))
	byID := make(map[string]string, len(offers))
	for i, o := range offers {
		id := "a" + strconv.Itoa(i+1)
		byID[id] = o.name
		items[i] = jev.ScoreItem{ID: id, Label: sanitize.ForDecision(o.name+": "+o.description, 240)}
	}
	ctx, cancel := context.WithTimeout(ctx, agentPickTimeout)
	defer cancel()
	start := time.Now()
	res, err := s.jev.PickAgent(ctx, request, items)
	if err != nil {
		rolemanager.RecordAgentPick("unknown", len(offers), res.Identity, time.Since(start))
		return ""
	}
	name, ok := clearAgent(res.Probs, byID)
	verdict := "default"
	if ok {
		verdict = "engaged"
	}
	rolemanager.RecordAgentPick(verdict, len(offers), res.Identity, time.Since(start))
	return name
}

// clearAgent reports the profile that leads the pick by the same cut-offs a
// detected intent must meet: the default option and every other profile count
// as rivals.
func clearAgent(probs map[string]float64, byID map[string]string) (string, bool) {
	var top, second float64
	topID := ""
	for id, p := range probs {
		switch {
		case p > top || (p == top && id < topID):
			second, top, topID = top, p, id
		case p > second:
			second = p
		}
	}
	name, isProfile := byID[topID]
	t := config.ActiveJevThresholds()
	if !isProfile || top < t.ModeConfident || top-second < t.ModeMargin {
		return "", false
	}
	return name, true
}
