package tui

import (
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/models"
	"github.com/vulnetix/belai/internal/provider"
	"github.com/vulnetix/belai/internal/sessionctl"
)

// Session controls as slash commands. Each command is parsed by
// internal/sessionctl, the vocabulary a `belai rc --web-controls` session
// speaks too, so a command typed here and the same command sent from the
// website mean one thing. The parsed change is then applied through the TUI's
// own toggles (toggleGuardrails, toggleAsk, toggleCaveman, the ctrl+r and
// ctrl+t overrides, setOperatingMode), so a command and its key do exactly the
// same work. The settings-backed controls (auto-commit, decisions, tests,
// language servers, Jev) are a session overlay: they are re-applied after
// every settings reload and never written to a settings file.

// ctlOverlayControls are kept as an overlay on the session's settings.
var ctlOverlayControls = map[string]bool{
	sessionctl.CtlAutoCommit: true,
	sessionctl.CtlDecisions:  true,
	sessionctl.CtlTests:      true,
	sessionctl.CtlLSP:        true,
	sessionctl.CtlJev:        true,
}

// registerSessionControls adds the control commands. /mode, /model and /lsp
// keep their own entries; /model and /lsp hand an argument to runControl.
func registerSessionControls(r *Registry) {
	for _, c := range sessionctl.Controls {
		name := strings.TrimPrefix(c.Command, "/")
		if _, ok := r.Command(name); ok {
			continue
		}
		usage := c.Usage
		values := c.Values
		r.Register(name, "session only: "+usage, func() []string { return values }, func(a *App, arg string) tea.Cmd {
			return a.runControl("/" + name + " " + arg)
		})
	}
}

// ctlState is the session's controls as the TUI holds them.
func (a *App) ctlState() sessionctl.State {
	st := sessionctl.FromSettings(a.settings, a.modeName(), a.cfg.Provider, a.cfg.Model, a.settings.Effort)
	st.Guardrails = a.guardrailsEnabled()
	st.Ask = a.askEnabled()
	switch {
	case a.reasoningOverride == nil:
		st.Reasoning = "auto"
	case *a.reasoningOverride:
		st.Reasoning = "shown"
	default:
		st.Reasoning = "hidden"
	}
	st.Tools = sessionctl.ToolModes[a.toolDisplay]
	return st
}

// ctlEnv is the TUI's view of what a control may name. The host's user may
// turn guardrails off here, as f3 does.
func (a *App) ctlEnv() sessionctl.Env {
	return sessionctl.Env{
		AllowGuardrailsOff: true,
		CheckModel: func(p, _, _ string) string {
			if _, ok := a.settings.Providers[p]; ok || provider.Builtin(p) {
				return ""
			}
			return "unknown provider " + p
		},
		Efforts: func(p, m string) []string {
			if l := models.Efforts(p, m); len(l) > 0 {
				return l
			}
			return models.DefaultEfforts()
		},
	}
}

// runControl parses and applies one control line.
func (a *App) runControl(line string) tea.Cmd {
	ch, err := sessionctl.Parse(line, a.ctlState(), a.ctlEnv())
	if err != nil {
		if errors.Is(err, sessionctl.ErrUnknown) {
			err = errors.New("unknown control")
		}
		a.addSystem(strings.Fields(line)[0] + ": " + err.Error())
		return nil
	}
	cur := a.ctlState()
	next := ch.State
	switch ch.Control {
	case sessionctl.CtlMode:
		return a.setOperatingMode(next.Mode)
	case sessionctl.CtlModel:
		a.addSystem(ch.Summary + " (this session)")
		if next.CodeTier != cur.CodeTier || next.CodeProvider != cur.CodeProvider || next.CodeModel != cur.CodeModel {
			code := config.CodeSettings{}
			if a.settings.Code != nil {
				code = *a.settings.Code
			}
			code.Model = &config.CodeModel{Tier: next.CodeTier, Provider: next.CodeProvider, Model: next.CodeModel}
			a.settings.Code = &code
			a.invalidateAgentSession()
			return a.refreshProvider()
		}
		return a.applyModelProvider(next.Provider, next.Model, next.Effort)
	case sessionctl.CtlEffort:
		a.settings.Effort = next.Effort
		a.invalidateAgentSession()
		a.addSystem(ch.Summary)
		return a.refreshProvider()
	case sessionctl.CtlGuardrails:
		if next.Guardrails != cur.Guardrails {
			return a.toggleGuardrails()
		}
	case sessionctl.CtlAsk:
		if next.Ask != cur.Ask {
			return a.toggleAsk()
		}
	case sessionctl.CtlCaveman:
		if next.Caveman != cur.Caveman {
			return a.toggleCaveman()
		}
	case sessionctl.CtlReasoning:
		switch next.Reasoning {
		case "auto":
			a.reasoningOverride = nil
		default:
			v := next.Reasoning == "shown"
			a.reasoningOverride = &v
		}
		a.addSystem("reasoning display: " + showLabel(a.reasoningVisible()))
		return nil
	case sessionctl.CtlTools:
		for i, m := range sessionctl.ToolModes {
			if m == next.Tools {
				a.toolDisplay = toolDisplay(i)
			}
		}
		a.addSystem(a.toolDisplayNotice())
		return nil
	default:
		if ctlOverlayControls[ch.Control] {
			a.ctlOverlay = append(a.ctlOverlay, line)
			a.applyCtlOverlay()
			a.invalidateAgentSession()
			a.addSystem(ch.Summary + " (this session)")
			return nil
		}
	}
	a.addSystem(ch.Summary)
	return nil
}

// applyCtlOverlay re-applies the session's settings-backed controls on top of
// freshly resolved settings. A line that no longer parses (a job removed by a
// settings edit) is dropped.
func (a *App) applyCtlOverlay() {
	if len(a.ctlOverlay) == 0 {
		return
	}
	st := sessionctl.FromSettings(a.settings, a.modeName(), a.cfg.Provider, a.cfg.Model, a.settings.Effort)
	kept := a.ctlOverlay[:0]
	for _, l := range a.ctlOverlay {
		ch, err := sessionctl.Parse(l, st, a.ctlEnv())
		if err != nil || !ctlOverlayControls[ch.Control] {
			continue
		}
		st = ch.State
		kept = append(kept, l)
	}
	a.ctlOverlay = kept
	a.settings = st.Apply(a.settings)
}
