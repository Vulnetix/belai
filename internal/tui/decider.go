package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/deciderserver"
	"github.com/vulnetix/belai/internal/decisions"
)

// deciderInfo is what /providers and /model know about Strands Decider-2B
// on this machine and on Hugging Face: harness facts from
// deciderserver.Detect and HubProviders, refreshed with the provider
// availability probe and read while rendering.
type deciderInfo struct {
	status   deciderserver.Status
	hub      []string
	hubKnown bool
	probedAt time.Time
	inFlight bool
}

// deciderProbedMsg carries a finished probe back into Update.
type deciderProbedMsg struct {
	status   deciderserver.Status
	hub      []string
	hubKnown bool
}

// deciderProbeCmd detects the decider off the Update loop: the command on
// PATH, the pinned weights, a server answering on loopback, and (with a
// Hugging Face token) which inference providers serve it. It launches and
// downloads nothing.
func (a *App) deciderProbeCmd() tea.Cmd {
	if a.decider.inFlight {
		return nil
	}
	a.decider.inFlight = true
	token := a.hfToken()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		m := decisions.Decider2B
		msg := deciderProbedMsg{status: deciderserver.Detect(ctx, m, deciderserver.Options{})}
		if token != "" {
			if names, err := deciderserver.HubProviders(ctx, nil, m, token); err == nil {
				msg.hub, msg.hubKnown = names, true
			}
		}
		return msg
	}
}

func (a *App) handleDeciderProbed(m deciderProbedMsg) tea.Cmd {
	a.decider = deciderInfo{status: m.status, hub: m.hub, hubKnown: m.hubKnown, probedAt: time.Now()}
	return nil
}

// deciderStatus is the last probe's status; before the first probe it is
// the zero status, which reads as not installed until the probe answers.
func (a *App) deciderStatus() deciderserver.Status {
	return a.decider.status
}

// deciderHubNote is the harness-composed Hugging Face line for the
// strands-decider row, or "" when no token is configured to ask with.
func (a *App) deciderHubNote() string {
	if !a.decider.hubKnown {
		return ""
	}
	if len(a.decider.hub) == 0 {
		return "Hugging Face: no inference provider serves it yet; a dedicated Inference Endpoint running strands-decider serve can be added as a systemone provider"
	}
	return "Hugging Face inference providers serving it: " + strings.Join(a.decider.hub, ", ")
}

// deciderDetected reports whether the decider can be offered as ready: a
// server answers, or the command is installed.
func (a *App) deciderDetected() bool {
	st := a.decider.status
	return st.Running != "" || st.Installed
}
