package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/bgagent"
	"github.com/vulnetix/belai/internal/tui/components"
	"github.com/vulnetix/belai/internal/vulnid"
)

// The vulnerability row (docs/vuln-row.md). When a tool result or the turn's
// reply names an advisory identifier, the thread gets one render-only row per
// identifier per session: a link to the console page, a hint to the vdb
// command and a button that starts belai:triage on it.
//
// The row is composed from the identifier alone. vulnid.Find recognises it
// (strict prefix and shape, ASCII only, bounded on both sides) and every field
// of the card is built from the canonical string it returns, never from the
// text around it. The row is Ephemeral, so it never produces a session entry
// (no sync, no transcript) and buildTurns never promotes it (no model); it
// touches no telemetry or notification. Detection is a harness check on text
// the user already sees, so it calls no model and has no guardrails gate.

const (
	// vulnPerTurn caps the rows one turn adds, so a command that prints a list
	// of advisories does not bury the thread. The rest are offered on a later
	// mention.
	vulnPerTurn = 3
	// vulnPerSession caps the rows a session ever adds.
	vulnPerSession = 40
	// vulnQueueMax bounds the identifiers held until the turn ends.
	vulnQueueMax = 32
	// vulnScanMax bounds the text one scan reads.
	vulnScanMax = 1 << 20
)

// vulnState is the row's session state.
type vulnState struct {
	seen    map[string]bool // identifiers that already have a row
	queue   []string        // found this turn, not yet shown
	shown   int
	started int // triage agents started, for unique instance names
}

// observeVulnText notes the identifiers in one piece of text the user sees: a
// tool result or a reply. The rows appear when the turn ends, so one never
// lands inside a streaming reply.
func (a *App) observeVulnText(text string) {
	if text == "" {
		return
	}
	if len(text) > vulnScanMax {
		text = text[:vulnScanMax]
	}
	v := &a.vulns
	for _, id := range vulnid.Find(text, vulnPerTurn+vulnPerSession) {
		if v.seen[id] {
			continue
		}
		if len(v.queue) >= vulnQueueMax {
			break
		}
		queued := false
		for _, q := range v.queue {
			if q == id {
				queued = true
				break
			}
		}
		if !queued {
			v.queue = append(v.queue, id)
		}
	}
}

// flushVulnRows adds the rows for what the turn found.
func (a *App) flushVulnRows() {
	v := &a.vulns
	queue := v.queue
	v.queue = nil
	added := 0
	for _, id := range queue {
		if added >= vulnPerTurn || v.shown >= vulnPerSession {
			return
		}
		if v.seen[id] {
			continue
		}
		if v.seen == nil {
			v.seen = map[string]bool{}
		}
		v.seen[id] = true
		v.shown++
		added++
		a.messages = append(a.messages, components.Message{
			Role:      components.VulnRole,
			Ephemeral: true,
			Vuln:      &components.VulnCard{ID: id, URL: vulnid.URL(id), Hint: vulnid.Hint(id)},
		})
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
		if h.Action != components.VulnOpen && h.Action != components.VulnTriage {
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
