package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/firewall"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tui/components"
)

// firewallVerdictMsg carries one firewall event out of the run observer.
type firewallVerdictMsg firewall.Verdict

// firewallCoalesce is how long an identical event is folded into the card
// already shown, so retries of one refused request draw one card.
const firewallCoalesce = 15 * time.Second

// watchFirewall registers the process-wide firewall observer. Like the
// role-manager feed it is a non-blocking send on a buffered channel: the
// cards are render-only, so dropping on overflow is correct.
func (a *App) watchFirewall() {
	a.fwEvents = make(chan firewall.Verdict, 64)
	a.fwCancel = run.SetFirewallObserver(func(v firewall.Verdict) {
		select {
		case a.fwEvents <- v:
		default:
		}
	}, func(string) { a.fwPasses.Add(1) })
}

// nextFirewall reads one verdict and re-arms.
func (a *App) nextFirewall() tea.Cmd {
	if a.fwEvents == nil {
		return nil
	}
	return func() tea.Msg {
		v, ok := <-a.fwEvents
		if !ok {
			return nil
		}
		return firewallVerdictMsg(v)
	}
}

// stopFirewallWatch detaches the observer on teardown.
func (a *App) stopFirewallWatch() {
	if a.fwCancel != nil {
		a.fwCancel()
		a.fwCancel = nil
	}
}

// addFirewallCard appends one verdict as a report card. The card is
// harness-composed: every third-party value was cleaned and capped by
// firewall.Verdict.Clean and is rendered inside code spans, so gateway text
// cannot restyle the card. Like every report card it is render-only and never
// reaches a model.
func (a *App) addFirewallCard(v firewall.Verdict) {
	key := v.Firewall + "|" + string(v.Action) + "|" + v.Code + "|" + v.RequestID
	if v.RequestID == "" {
		key += "|" + v.Provider + "|" + v.Model
	}
	now := time.Now()
	if key == a.fwLastKey && now.Sub(a.fwLastAt) < firewallCoalesce {
		a.fwLastAt = now
		return
	}
	a.fwLastKey, a.fwLastAt = key, now
	a.fwEventCount++

	status := components.ReportAttention
	if v.Action.Severe() {
		status = components.ReportFailed
	}
	meta := v.Provider
	if v.Model != "" {
		meta += " · " + v.Model
	}
	a.messages = append(a.messages, components.Message{
		Role:      components.ReportRole,
		Content:   firewallCardBody(v),
		ToolName:  "⛨ " + v.Firewall + " · " + string(v.Action),
		ToolArgs:  meta,
		Status:    status,
		CreatedAt: now,
	})
	a.refreshFooter()
}

// firewallCardBody composes the card's markdown.
func firewallCardBody(v firewall.Verdict) string {
	var b strings.Builder
	line := func(label, value string) {
		if value != "" {
			fmt.Fprintf(&b, "- **%s** %s\n", label, value)
		}
	}
	switch v.Action {
	case firewall.ActionBlock:
		b.WriteString("The request was refused before it reached the model.\n\n")
	case firewall.ActionRefused:
		b.WriteString("The firewall refused the request; it did not reach the model.\n\n")
	case firewall.ActionRedact:
		b.WriteString("The request was forwarded with matched text replaced.\n\n")
	case firewall.ActionFlag:
		b.WriteString("The request was forwarded unchanged and recorded.\n\n")
	case firewall.ActionStrip:
		b.WriteString("Parts of the request were removed before forwarding.\n\n")
	}
	if len(v.Rules) > 0 {
		spans := make([]string, len(v.Rules))
		for i, r := range v.Rules {
			spans[i] = codeSpan(r)
		}
		line("Rules", strings.Join(spans, ", "))
	}
	if v.Redactions > 0 {
		line("Redactions", fmt.Sprint(v.Redactions))
	}
	if v.Stripped > 0 {
		line("Stripped", fmt.Sprintf("%d capabilities", v.Stripped))
	}
	if n := v.Nonce; n != nil && (n.Unknown > 0 || n.Tampered > 0 || n.Stripped > 0) {
		line("Sealed blocks", fmt.Sprintf("%s · %d verified · %d unknown · %d tampered · %d stripped", codeSpan(n.Mode), n.Verified, n.Unknown, n.Tampered, n.Stripped))
	}
	if v.Code != "" {
		line("Code", codeSpan(v.Code))
	}
	if v.Status != 0 {
		line("Status", fmt.Sprint(v.Status))
	}
	if v.Message != "" {
		line("Message", codeSpan(v.Message))
	}
	if v.RequestID != "" {
		line("Request", codeSpan(v.RequestID))
	}
	if v.Instance != "" {
		line("Firewall", codeSpan(v.Instance))
	}
	if v.Hint != "" {
		b.WriteString("\n" + escapeMarkdown(v.Hint) + "\n")
	}
	return b.String()
}

// codeSpan renders cleaned third-party text as an inline code span.
func codeSpan(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "'") + "`"
}

// escapeMarkdown neutralises markdown syntax in a hint. Hints are harness
// text today; escaping keeps that true should one ever carry a value.
func escapeMarkdown(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "<", "&lt;", ">", "&gt;", "#", "\\#")
	out := r.Replace(s)
	// Backticked commands in hints stay code spans.
	return out
}

// firewallFooterLabel is the footer chip text: the active instance, and the
// number of events seen once there is one.
func (a *App) firewallFooterLabel() string {
	if !a.firewallEnabled() {
		return ""
	}
	label := a.settings.FirewallActive()
	if a.fwEventCount > 0 {
		label += fmt.Sprintf(" · %d event", a.fwEventCount)
		if a.fwEventCount > 1 {
			label += "s"
		}
	}
	return label
}
