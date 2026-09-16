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
	// Skills is the rendered list of available skills (name + description),
	// harness-loaded from disk. The classifier turns never carry these.
	Skills []string
	// ExploreNote is a harness-generated framing sentence added when explore
	// subagent reports are appended as user turns.
	ExploreNote string
	// Explore marks a plan-mode explore subagent. It renders the exploration
	// preamble that tells the subagent to investigate with read-only tools
	// rather than ask the user for clarification.
	Explore bool
	// Provider and Model name the two identities the harness does not own.
	// Empty values are omitted rather than guessed at.
	Provider string
	Model    string
}

// identity tells the model which of the three identities in a session is its
// own. Signet is the harness, not the assistant: an earlier version of this
// prompt opened with "You are Signet, an LLM coding harness", which the model
// read as an instruction to adopt Signet as its identity. It then disclaimed
// knowledge of itself — answering "I don't have visibility into the underlying
// model" to a direct question about what it is — because the prompt had
// replaced what it knows about itself from training.
//
// The harness names itself, names the provider, and leaves the model's own
// identity to the model.
func identity(provider, model string) string {
	var b strings.Builder
	b.WriteString("You are an AI model running inside Signet, an LLM coding harness.\n")
	b.WriteString("Three identities are in play in this session and they are not interchangeable:\n")
	b.WriteString("- Harness: Signet. The tooling around you — this prompt, the tools, the safety pipeline. Signet is not you.\n")
	if provider != "" {
		b.WriteString(fmt.Sprintf("- Provider: %s. The API serving this session.\n", provider))
	} else {
		b.WriteString("- Provider: the API serving this session.\n")
	}
	if model != "" {
		b.WriteString(fmt.Sprintf("- Model: %s, as the provider names it. That is you.\n", model))
	} else {
		b.WriteString("- Model: you.\n")
	}
	b.WriteString("Keep your own identity, capabilities, and knowledge as they come from your training. ")
	b.WriteString("Asked what you are, answer as yourself and name Signet as the harness rather than claiming to be it.\n")
	return b.String()
}

const normalVoice = "Voice guidance: respond clearly and professionally.\n"

const cavemanVoice = "Voice guidance: talk like caveman. Short words. No long words. 'Me fix now.'\n"

// explorePreamble is the harness-authored guidance attached to a plan-mode
// explore subagent's system prompt. It is trusted harness text (SourceHarness
// provenance via SealSystem), never model output.
const explorePreamble = `You are in plan-mode exploration. You may use the read-only tools listed below to investigate the repository. Prefer to discover facts yourself with the native read-only tools (Grep, Glob, Find, Git, Cat, Head, Tail, JQ, YQ, LS, File, Diff, and the others) rather than asking questions. Only ask the user a clarifying question when you have exhausted the available evidence and the decision genuinely requires user judgment. Produce a concise findings report as your final reply.
`

// System renders the system prompt. Exactly one carrier may be active: if
// Carrier is set, only that carrier's text may be non-empty; if Carrier is
// none, no carrier text may be provided.
func System(opts Options) (string, error) {
	carrier, text, err := resolveCarrier(opts)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(identity(opts.Provider, opts.Model))
	if carrier != CarrierNone {
		b.WriteString(fmt.Sprintf("Active %s:\n%s\n", carrier, text))
	}
	if len(opts.Skills) > 0 {
		b.WriteString("Available skills:\n")
		for _, s := range opts.Skills {
			b.WriteString("- " + s + "\n")
		}
	}
	if opts.ExploreNote != "" {
		b.WriteString(opts.ExploreNote + "\n")
	}
	if opts.Explore {
		b.WriteString(explorePreamble)
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
