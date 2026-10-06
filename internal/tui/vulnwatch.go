package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/bgagent"
	"github.com/vulnetix/belai/internal/clipboard"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/tui/components"
	"github.com/vulnetix/belai/internal/vulnid"
)

// The vulnerability row (docs/vuln-row.md). When a tool result or the turn's
// reply names an advisory identifier, the thread gets one render-only row per
// identifier per session: the console link and the vdb command as selectable
// text, copy and open buttons, a button that sends a prepared remediation
// prompt and a button that starts belai:triage on it.
//
// The row is composed from the identifier alone. vulnid.Find recognises it
// (strict prefix and shape, ASCII only, bounded on both sides) and every field
// of the card is built from the canonical string it returns, never from the
// text around it. The row on screen is Ephemeral and buildTurns never promotes
// it, so no model sees it; the website gets the same facts from a vuln session
// entry (docs/session-sync.md), which holds only what vulnid composed from the
// identifier. It touches no telemetry or notification. Detection is a harness
// check on text the user already sees, so it calls no model and has no
// guardrails gate.

// The limits are vulnid's, shared with the headless transcript writer.
const vulnPerTurn = vulnid.PerTurn

// vulnState is the row's session state.
type vulnState struct {
	track   vulnid.Tracker // what the session has shown and what waits for the turn's end
	started int            // triage agents started, for unique instance names
}

// observeVulnText notes the identifiers in one piece of text the user sees: a
// tool result or a reply. The rows appear when the turn ends, so one never
// lands inside a streaming reply.
func (a *App) observeVulnText(text string) { a.vulns.track.Observe(text) }

// flushVulnRows adds the rows for what the turn found, and records each in the
// session for the website: a vuln entry holding the identifier and what the
// row offers, all composed from it. The row on screen stays ephemeral (a resume
// does not redraw it); the entry is what the website draws.
func (a *App) flushVulnRows() {
	for _, id := range a.vulns.track.Flush() {
		a.messages = append(a.messages, components.Message{
			Role:      components.VulnRole,
			Ephemeral: true,
			Vuln:      &components.VulnCard{ID: id, URL: vulnid.URL(id), Command: vulnid.Command(id)},
		})
		a.appendEntry(session.Entry{Type: vulnid.EntryType, Role: "system", Content: id, Meta: vulnid.EntryMeta(id)})
	}
}

// vulnMouse handles a press over a vulnerability row's clickable regions. It
// reports whether the press was one, so text selection does not start.
func (a *App) vulnMouse(p components.Pos) (bool, tea.Cmd) {
	if p.Line < 0 || p.Line >= len(a.lastFrame.lines) {
		return false, nil
	}
	line := a.lastFrame.lines[p.Line]
	for _, h := range line.Hits {
		if p.Col < h.Col || p.Col >= h.Col+h.Width {
			continue
		}
		switch h.Action {
		case components.VulnOpen, components.VulnCopyURL, components.VulnCopyCmd, components.VulnRemediate, components.VulnTriage:
		default:
			continue
		}
		return true, a.vulnHit(line.Owner, h.Action)
	}
	return false, nil
}

// vulnHit applies a click. The identifier is read from the clicked row and
// validated again, so nothing but a recognised identifier reaches the address
// or the agent.
func (a *App) vulnHit(owner int, action string) tea.Cmd {
	if !a.messageIs(owner, components.VulnRole) || a.messages[owner].Vuln == nil {
		return nil
	}
	card := a.messages[owner].Vuln
	id, ok := vulnid.Valid(card.ID)
	if !ok {
		return nil
	}
	switch action {
	case components.VulnOpen:
		return openLink(vulnid.URL(id))
	case components.VulnCopyURL:
		return copyText(vulnid.URL(id), "link")
	case components.VulnCopyCmd:
		return copyText(vulnid.Command(id), "command")
	case components.VulnRemediate:
		if card.Remediating {
			return nil
		}
		cmd, started := a.remediateVuln(id)
		if started {
			updated := *card
			updated.Remediating = true
			a.messages[owner].Vuln = &updated
		}
		return cmd
	case components.VulnTriage:
		if card.Launched {
			return nil
		}
		cmd, started := a.startVulnTriage(id)
		if started {
			updated := *card
			updated.Launched = true
			a.messages[owner].Vuln = &updated
		}
		return cmd
	}
	return nil
}

// copyText puts text on the clipboard and says what was copied.
func copyText(text, what string) tea.Cmd {
	if text == "" {
		return nil
	}
	return func() tea.Msg {
		method, err := clipboard.Copy(text)
		if err != nil {
			return copiedMsg{text: "copy failed: " + err.Error()}
		}
		return copiedMsg{text: fmt.Sprintf("copied the %s to clipboard (%s)", what, method)}
	}
}

// remediateVuln sends the prepared remediation prompt for one identifier as a
// prompt of the user's own: it is echoed, admitted (sanitised and classified)
// and run like a typed one, in the mode the session is in. With no agent
// engaged in agent mode the session moves to Auto first, so the role manager
// picks the mode and the agent instead of the turn waiting on the picker. The
// composer's draft and attachments are left alone. A turn already running
// refuses it: the click never steers or queues.
func (a *App) remediateVuln(id string) (tea.Cmd, bool) {
	prompt := vulnid.RemediationPrompt(id)
	if prompt == "" {
		return nil, false
	}
	if a.working() || a.preSend {
		a.addSystem("remediate: a turn is running; click again when it ends")
		return nil, false
	}
	if a.mode == "agent" && a.namedAgent == "" && !a.modeAuto {
		a.enterAutoMode()
	}
	firstUser := !a.hasUserMessage()
	a.echoUser(prompt)
	return a.dispatchPrompt(prompt, nil, "", firstUser), true
}

// startVulnTriage starts the built-in belai:triage agent on one identifier. It
// is the ordinary background-agent path (bgagent.Manager.StartTask): the
// profile's tools, the permission rules, the trust gate, the OS sandbox and
// the pass budget all apply as they do for any background agent. The
// identifier rides in the task as data (vulnid.TaskPrompt); it is not in the
// instance name, which only counts.
func (a *App) startVulnTriage(id string) (tea.Cmd, bool) {
	if a.bgManager == nil {
		a.addSystem("triage: no background manager")
		return nil, false
	}
	prompt := vulnid.TaskPrompt(id)
	if prompt == "" {
		return nil, false
	}
	profile, err := agentprofile.Load("belai:triage")
	if err != nil {
		a.addSystem("triage: " + err.Error())
		return nil, false
	}
	a.vulns.started++
	key := fmt.Sprintf("belai:triage#%d", a.vulns.started)
	if err := a.bgManager.StartTask(a.workdir, key, profile, bgagent.Task{Prompt: prompt}); err != nil {
		a.addSystem("triage: " + err.Error())
		return nil, false
	}
	a.registerAgentActivity(key, a.workdir)
	return a.noteAgentStarted(key), true
}
