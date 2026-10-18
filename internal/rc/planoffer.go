package rc

import (
	"strings"

	"github.com/vulnetix/belai/internal/modelinfo"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/transcript"
	"github.com/vulnetix/belai/internal/webask"
)

// planModels are the models a plan review offers to execute on: the session's
// own model first, then every model the host advertises. Only a session with
// web controls can switch model (a /model control), so one without them offers
// none.
func planModels(o SessionOptions) []webask.PlanModel {
	if o.Controls == nil || o.Models == nil {
		return nil
	}
	adv := o.Models()
	if adv == nil {
		return nil
	}
	st := o.Controls.State()
	var out []webask.PlanModel
	seen := map[string]bool{}
	add := func(provider, model string, current bool) {
		key := provider + "/" + model
		if provider == "" || model == "" || seen[key] || len(out) >= webask.MaxPlanModels {
			return
		}
		seen[key] = true
		window, _ := modelinfo.Resolve(model, o.ContextWindows)
		out = append(out, webask.PlanModel{Provider: provider, Model: model, ContextWindow: window, Current: current})
	}
	add(strings.ToLower(st.Provider), st.Model, true)
	for _, p := range adv.Providers {
		for _, m := range p.Models {
			add(p.Name, m, false)
		}
	}
	return out
}

// historyTokens estimates what approving here re-sends besides the system
// prompt and the plan: a remote session keeps only the prompts and replies of
// its turns, plus the prompt of the turn that wrote the plan.
func historyTokens(history []run.Turn, text string) int {
	n := transcript.EstimateTokens(transcript.Message{Role: "user", Content: text})
	for _, t := range history {
		n += transcript.EstimateTokens(transcript.Message{Role: t.Role, Content: t.Content})
	}
	return n
}

// applyPlanModel switches the session to the model an approval picked, before
// the plan executes. The model was validated against the review's offer, which
// only a session with web controls makes.
func applyPlanModel(o SessionOptions, pa webask.PlanAnswer) {
	if pa.Model == "" || o.Controls == nil {
		return
	}
	st := o.Controls.State()
	if strings.EqualFold(st.Provider, pa.Provider) && st.Model == pa.Model {
		return
	}
	line := "/model " + pa.Provider + " " + pa.Model
	if summary, err := o.Controls.Apply(sessionsync.RemoteCommand{Line: line}, true); err != nil {
		o.Log.System("web: the plan's model could not be set, it runs on " + st.Provider + "/" + st.Model + ": " + err.Error())
	} else {
		o.Log.System("web: " + summary)
	}
}
