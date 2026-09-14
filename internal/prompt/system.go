// Package prompt assembles the user-controlled system prompt. All system
// prompt assembly flows through this manager. It carries at most one context
// block — the active plan, the active goal, or the active agent profile — and
// rewrites assistant voice guidance when caveman mode is on.
package prompt

import (
	"fmt"
	"strings"
)

// Carrier identifies which single context block the system prompt carries.
type Carrier string

const (
	CarrierNone    Carrier = ""
	CarrierPlan    Carrier = "plan"
	CarrierGoal    Carrier = "goal"
	CarrierProfile Carrier = "profile"
)

// Options configures one system prompt rendering.
type Options struct {
	Carrier     Carrier
	PlanText    string
	GoalText    string
	ProfileText string
	Caveman     bool
}

const baseSystem = "You are Signet, an LLM coding harness.\n"

const normalVoice = "Voice guidance: respond clearly and professionally.\n"

const cavemanVoice = "Voice guidance: talk like caveman. Short words. No long words. 'Me fix now.'\n"

// System renders the system prompt. Exactly one carrier may be active: if
// Carrier is set, only that carrier's text may be non-empty; if Carrier is
// none, no carrier text may be provided.
func System(opts Options) (string, error) {
	carrier, text, err := resolveCarrier(opts)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(baseSystem)
	if carrier != CarrierNone {
		b.WriteString(fmt.Sprintf("Active %s:\n%s\n", carrier, text))
	}
	if opts.Caveman {
		b.WriteString(cavemanVoice)
	} else {
		b.WriteString(normalVoice)
	}
	return b.String(), nil
}

func resolveCarrier(opts Options) (Carrier, string, error) {
	texts := map[Carrier]string{
		CarrierPlan:    opts.PlanText,
		CarrierGoal:    opts.GoalText,
		CarrierProfile: opts.ProfileText,
	}
	var active []Carrier
	for c, t := range texts {
		if strings.TrimSpace(t) != "" {
			active = append(active, c)
		}
	}

	if opts.Carrier == CarrierNone {
		if len(active) != 0 {
			return "", "", fmt.Errorf("carrier is none but text provided for %v", active)
		}
		return CarrierNone, "", nil
	}

	if len(active) != 1 || active[0] != opts.Carrier {
		return "", "", fmt.Errorf("exactly one carrier text must match the selected carrier %q", opts.Carrier)
	}
	return opts.Carrier, texts[opts.Carrier], nil
}
