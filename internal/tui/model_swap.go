package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/run"
)

// The model-swap key (ctrl+q) toggles the session between its main model and
// its fast tier. It is a session-only choice: it calls the same
// applyModelProvider the /model picker's "session" scope uses and writes no
// settings file, and the saved last-used selection (saveState, saveSession)
// keeps the main model while the fast one is borrowed. Only a model the
// session already resolved is offered (cfg.Routing.Fast), so there is nothing
// for the modeltest ladder to prove: nothing is being saved.
//
// A press schedules the switch for the next turn. A second press before it
// applies cancels it. A press during a running turn interrupts that turn and
// retries the same prompt on the other model. Every line it shows is
// harness text (a provider/model identifier), Ephemeral so it never reaches
// the session record, and never a model's input.

// modelSwapKey is the key that toggles fast and main.
const modelSwapKey = "ctrl+q"

// promptRec is the prompt a turn started from, kept so an interrupting swap
// can start it again on the other model.
type promptRec struct {
	input     string
	atts      []run.Attachment
	directive string
}

// modelTarget is one provider/model pair of a swap.
type modelTarget struct{ provider, model string }

func (t modelTarget) label() string { return t.provider + "/" + t.model }

func (t modelTarget) zero() bool { return t.model == "" }

// modelSwap is the session's fast/main toggle state.
type modelSwap struct {
	// onFast is true while the session runs the fast tier instead of its main
	// model. main is the model to return to; fast the one borrowed.
	onFast     bool
	main, fast modelTarget
	// pending is the switch scheduled for the next turn: "", "fast" or "main".
	pending string
}

// swapSide names a target for the thread line.
func swapSide(fast bool) string {
	if fast {
		return "fast"
	}
	return "main"
}

// current is the target the session is running now.
func (a *App) currentTarget() modelTarget {
	return modelTarget{a.cfg.Provider, a.cfg.Model}
}

// normaliseSwap forgets a borrowed fast model the user has since replaced
// (a /model pick, a resume), so a stale swap can never put an old model back.
func (a *App) normaliseSwap() {
	if a.swap.onFast && a.currentTarget() != a.swap.fast {
		a.swap = modelSwap{}
	}
	if !a.swap.onFast && a.swap.pending == "main" {
		a.swap.pending = ""
	}
}

// swapTargets returns the main and fast models the toggle switches between,
// and whether a separate fast model exists.
func (a *App) swapTargets() (main, fast modelTarget, ok bool) {
	if a.swap.onFast {
		return a.swap.main, a.swap.fast, true
	}
	f := a.cfg.Routing.Fast
	if f == nil || f.Model == "" {
		return modelTarget{}, modelTarget{}, false
	}
	return a.currentTarget(), modelTarget{f.Provider, f.Model}, true
}

// persistedModel is the provider and model written to the saved last-used
// selection: the main model, never a borrowed fast one.
func (a *App) persistedModel() (provider, model string) {
	if a.swap.onFast && !a.swap.main.zero() {
		return a.swap.main.provider, a.swap.main.model
	}
	return a.cfg.Provider, a.cfg.Model
}

// swapPendingLabel is the model the scheduled switch will use, "" when none.
func (a *App) swapPendingLabel() string {
	switch a.swap.pending {
	case "fast":
		_, f, _ := a.swapTargets()
		return f.label()
	case "main":
		return a.swap.main.label()
	}
	return ""
}

// toggleModelSwap handles the key. Chat only: it needs a thread to write to.
func (a *App) toggleModelSwap() tea.Cmd {
	a.normaliseSwap()
	main, fast, ok := a.swapTargets()
	if !ok {
		a.addEphemeralSystem("no separate fast model is configured, so there is nothing to switch to (see /model)")
		return nil
	}
	// A second press before the switch applies reverts it.
	if a.swap.pending != "" {
		stays := swapSide(a.swap.onFast)
		a.swap.pending = ""
		a.addEphemeralSystem(fmt.Sprintf("model switch cancelled: next turn stays on the %s model (%s)", stays, a.currentTarget().label()))
		return nil
	}
	want := "fast"
	target := fast
	if a.swap.onFast {
		want, target = "main", main
	}
	if !a.swap.onFast {
		// Remember where to come back to.
		a.swap.main, a.swap.fast = main, fast
	}
	a.swap.pending = want
	a.addEphemeralSystem(fmt.Sprintf("next turn will use %s model (%s)", want, target.label()))
	if !a.working() {
		return nil
	}
	// A running turn is interrupted and its prompt retried on the other
	// model. cancelTurn leaves the record exactly as an esc does (partial
	// rows flushed, "request cancelled"); the retry is then an ordinary
	// dispatch of the same prompt, which applies the scheduled model in send.
	rec := a.lastPrompt
	a.cancelTurn()
	if rec == nil {
		return nil
	}
	a.addEphemeralSystem(fmt.Sprintf("retrying the interrupted turn on the %s model", want))
	return a.dispatchPrompt(rec.input, rec.atts, rec.directive, false)
}

// applyModelSwap makes a scheduled switch real. send calls it as the turn
// starts, so the new model takes effect on the next turn and never mid-turn.
func (a *App) applyModelSwap() {
	a.normaliseSwap()
	if a.swap.pending == "" {
		return
	}
	want := a.swap.pending
	a.swap.pending = ""
	var target modelTarget
	switch want {
	case "fast":
		if a.swap.onFast || a.swap.fast.zero() || a.swap.main != a.currentTarget() {
			// The main model changed since the press: the schedule is stale.
			return
		}
		target = a.swap.fast
	case "main":
		target = a.swap.main
	}
	// refreshProvider resends a held prompt when credentials land; a swap
	// must not start a second turn from inside send.
	held := a.pending
	a.pending = ""
	a.applyModelProvider(target.provider, target.model, "")
	a.pending = held
	if !a.status.Configured {
		// The target cannot be used: go back rather than fail the turn.
		back := a.swap.main
		if want == "main" {
			back = a.swap.fast
		}
		a.applyModelProvider(back.provider, back.model, "")
		a.pending = held
		a.addEphemeralSystem(fmt.Sprintf("the %s model (%s) is not configured; staying on %s", want, target.label(), back.label()))
		return
	}
	a.swap.onFast = want == "fast"
	if !a.swap.onFast {
		a.swap = modelSwap{}
	}
	a.addEphemeralSystem(fmt.Sprintf("this turn runs on the %s model (%s)", want, target.label()))
}
