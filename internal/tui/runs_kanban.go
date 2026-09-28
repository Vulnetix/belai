package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/tui/components"
)

// The runs panel's kanban and crew tabs: the board and the fleet workers
// next to the chat, with single-key actions. The kanban tab draws its items
// exactly as the composer pane does (kanbanPaneHeader, kanbanPaneRow); every
// action goes through the same kanban.Store calls as /kanban, and starting a
// crew goes through startFleetWorkers, preflight and max_workers included.
// Nothing here reaches a model: the one bridge is ⏎, which puts the item's
// prompt in the composer for the typed-prompt path.

// kanbanTabFilters are the kanban tab's list filters. They add in_progress
// and done to the pane's, since the panel is where claims are watched.
var kanbanTabFilters = []kanbanFilter{
	{"open", []kanban.List{kanban.Review, kanban.InProgress, kanban.Blocked, kanban.Backlog}},
	{"backlog", []kanban.List{kanban.Backlog}},
	{"review", []kanban.List{kanban.Review}},
	{"in progress", []kanban.List{kanban.InProgress}},
	{"blocked", []kanban.List{kanban.Blocked}},
	{"done", []kanban.List{kanban.Done}},
}

// kanbanInputKind is what the composer is asking for on the kanban tab.
type kanbanInputKind int

const (
	kanbanInputNone kanbanInputKind = iota
	kanbanInputFilter
	kanbanInputNew
	kanbanInputNote
	kanbanInputAssign
	kanbanInputLabels
)

// kanbanInputState is the kanban tab's open composer prompt.
type kanbanInputState struct {
	kind kanbanInputKind
	id   string // the item a note, assignee or labels are for
	ref  string // its short id, for the composer title
}

// kanbanTabVisible reports whether the kanban tab is offered: the board is on.
func (a *App) kanbanTabVisible() bool { return a.kb != nil }

// kanbanTabList returns the kanban tab's items and whether they are every
// project's.
func (a *App) kanbanTabList() ([]kanban.Item, bool) {
	if a.kb == nil {
		return nil, false
	}
	ps := &a.kb.tab
	ps.filter = clamp(ps.filter, 0, len(kanbanTabFilters)-1)
	q := kanban.Query{Text: ps.query, Lists: kanbanTabFilters[ps.filter].lists, Limit: kanban.MaxItems}
	if !ps.all {
		q.Project = a.kanbanProject()
	}
	items, err := a.kb.store.Search(q)
	if err != nil {
		return nil, ps.all
	}
	return items, ps.all
}

// kanbanTabItems maps the tab's items to runs rows, so the panel's selection
// and windowing apply.
func (a *App) kanbanTabItems() []runsItem {
	items, all := a.kanbanTabList()
	now := time.Now().UnixMilli()
	out := make([]runsItem, 0, len(items))
	for _, it := range items {
		out = append(out, runsItem{
			ID:     it.ID,
			Label:  it.Short() + " " + it.Title,
			Row:    a.kanbanPaneRow(it, all, false),
			Detail: kanbanRouting(it, now),
			State:  string(it.List),
		})
	}
	return out
}

// kanbanTabSummary is the pane header and the sync status.
func (a *App) kanbanTabSummary() []string {
	if a.kb == nil {
		return nil
	}
	_, all := a.kanbanTabList()
	return []string{kanbanPaneHeader(kanbanTabFilters, &a.kb.tab, all) + components.MutedStyle.Render("  · "+a.kanbanSyncLabel())}
}

// kanbanTabSelected is the item under the panel cursor.
func (a *App) kanbanTabSelected() (kanban.Item, bool) {
	items, _ := a.kanbanTabList()
	if a.runsSel < 0 || a.runsSel >= len(items) {
		return kanban.Item{}, false
	}
	return items[a.runsSel], true
}

// openKanbanTabFromPane opens the runs panel on the kanban tab with the
// composer pane's filter, scope, query and selection carried over.
func (a *App) openKanbanTabFromPane(paneItems []kanban.Item) tea.Cmd {
	if a.kb == nil || a.height < minRunsPanelOpenHeight {
		return nil
	}
	ps := a.kb.pane
	tab := &a.kb.tab
	tab.all, tab.query, tab.filter = ps.all, ps.query, 0
	// The pane's filters are open/backlog/review/blocked; the tab's are the
	// same labels by name, plus in progress and done.
	for i, f := range kanbanTabFilters {
		if f.label == kanbanPaneFilters[ps.filter].label {
			tab.filter = i
		}
	}
	var selID string
	if ps.sel >= 0 && ps.sel < len(paneItems) {
		selID = paneItems[ps.sel].ID
	}
	a.kb.pane.focus = false
	a.runsOpen, a.runsFocus, a.runsTab = true, true, tabKanban
	a.runsSel, a.runsScroll = 0, 0
	a.reloadFleet()
	a.kanbanTabSelect(selID)
	return nil
}

// kanbanTabSelect moves the panel cursor to item id when the tab lists it.
func (a *App) kanbanTabSelect(id string) {
	if id == "" {
		return
	}
	items, _ := a.kanbanTabList()
	for i, it := range items {
		if it.ID == id {
			a.runsSel = i
			return
		}
	}
}

// kanbanNotice reports an action's outcome in the transcript.
func (a *App) kanbanNotice(format string, args ...any) {
	a.addSystem("kanban: " + fmt.Sprintf(format, args...))
}

// handleRunsKanbanKey routes the kanban tab's keys.
func (a *App) handleRunsKanbanKey(m tea.KeyMsg) tea.Cmd {
	if a.kb == nil {
		return nil
	}
	ps := &a.kb.tab
	key := m.String()
	switch key {
	case "[", "]":
		n := len(kanbanTabFilters)
		if key == "]" {
			ps.filter = (ps.filter + 1) % n
		} else {
			ps.filter = (ps.filter + n - 1) % n
		}
		a.runsSel, a.runsScroll = 0, 0
		return nil
	case "p":
		ps.all = !ps.all
		a.runsSel, a.runsScroll = 0, 0
		return nil
	case "/":
		a.startKanbanInput(kanbanInputFilter, kanban.Item{}, ps.query)
		return nil
	case "n":
		a.startKanbanInput(kanbanInputNew, kanban.Item{}, "")
		return nil
	}
	it, ok := a.kanbanTabSelected()
	if !ok {
		return nil
	}
	sid := a.kb.src.Get().SessionID
	switch key {
	case "enter":
		a.runsFocus = false
		a.prefillKanbanPrompt(it)
	case "1", "2", "3", "4", "5":
		to := kanban.Lists[key[0]-'1']
		if to == it.List {
			return nil
		}
		if _, err := a.kb.store.Move(it.ID, to, "moved by the user", sid); err != nil {
			a.kanbanNotice("%s: %v", it.Short(), err)
		} else {
			a.kanbanNotice("%s moved %s → %s", it.Short(), it.List, to)
		}
		a.kanbanTabSelect(it.ID)
	case "o":
		a.startKanbanInput(kanbanInputNote, it, "")
	case "a":
		a.startKanbanInput(kanbanInputAssign, it, it.Assignee)
	case "L":
		a.startKanbanInput(kanbanInputLabels, it, strings.Join(it.Labels, ", "))
	case "+", "=", "-":
		p := it.Priority + 1
		if key == "-" {
			p = it.Priority - 1
		}
		if got, err := a.kb.store.Route(it.ID, kanban.RoutePatch{Priority: &p}, sid); err != nil {
			a.kanbanNotice("%s: %v", it.Short(), err)
		} else {
			a.kanbanNotice("%s priority %d", got.Short(), got.Priority)
		}
	case "u":
		if it.ClaimedBy == "" {
			a.kanbanNotice("%s is not claimed", it.Short())
		} else if got, err := a.kb.store.Unclaim(it.ID, "claim released by the user", sid, false); err != nil {
			a.kanbanNotice("%s: %v", it.Short(), err)
		} else {
			a.kanbanNotice("%s released to %s", got.Short(), got.List)
		}
	case "w":
		a.handToCrew(it)
	case "K":
		v := &a.kb.view
		v.tab = max(0, slices.Index(kanbanViewLists, it.List))
		v.all, v.query, v.sel = ps.all, "", 0
		if rows, err := a.kanbanViewRows(); err == nil {
			for i, r := range rows {
				if r.ID == it.ID {
					v.sel = i
				}
			}
		}
		a.runsFocus = false
		return tea.Batch(a.enterKanban(), a.push(viewKanban))
	}
	return nil
}

// handToCrew puts an item where the chosen crew takes work: the crew's entry
// labels (its first backlog-claiming member's) are added, the item goes to
// backlog, and the crew is started when no worker is running. A claimed item
// is left alone: its worker holds it.
func (a *App) handToCrew(it kanban.Item) {
	now := time.Now().UnixMilli()
	if it.Claimed(now) {
		a.kanbanNotice("%s is claimed by %s; release it first (u)", it.Short(), it.ClaimedBy)
		return
	}
	crew, ok := a.chosenCrew()
	if !ok {
		a.kanbanNotice("no crews defined")
		return
	}
	sid := a.kb.src.Get().SessionID
	entry := crewEntryLabels(crew)
	labels := append([]string(nil), it.Labels...)
	for _, l := range entry {
		if !slices.Contains(labels, l) {
			labels = append(labels, l)
		}
	}
	if len(labels) != len(it.Labels) {
		if _, err := a.kb.store.Route(it.ID, kanban.RoutePatch{Labels: &labels}, sid); err != nil {
			a.kanbanNotice("%s: %v", it.Short(), err)
			return
		}
	}
	if it.List != kanban.Backlog {
		if _, err := a.kb.store.Move(it.ID, kanban.Backlog, "handed to crew "+crew.Name+" by the user", sid); err != nil {
			a.kanbanNotice("%s: %v", it.Short(), err)
			return
		}
	}
	msg := fmt.Sprintf("%s handed to %s", it.Short(), crew.Name)
	if len(entry) > 0 {
		msg += " (#" + strings.Join(entry, " #") + ")"
	}
	a.kanbanNotice("%s", msg)
	a.kanbanTabSelect(it.ID)
	a.reloadFleet()
	if a.fleetLive() == 0 {
		a.startFleetWorkers("", crew.Name, 0)
		a.reloadFleet()
	}
}

// crewEntryLabels are the labels of the crew's first member that claims from
// backlog: where a new request enters it (belai:delivery's scout, #scout).
func crewEntryLabels(c agentprofile.Crew) []string {
	for _, m := range c.Members {
		p, err := agentprofile.Load(m.Profile)
		if err != nil || p.Kanban == nil {
			continue
		}
		if len(p.Kanban.Lists) == 0 || slices.Contains(p.Kanban.Lists, string(kanban.Backlog)) {
			return p.Kanban.Labels
		}
	}
	return nil
}

// kanbanInputActive reports whether a kanban-tab prompt owns the composer.
func (a *App) kanbanInputActive() bool {
	return a.kb != nil && a.kb.input.kind != kanbanInputNone
}

// startKanbanInput hands the composer to a kanban-tab prompt.
func (a *App) startKanbanInput(kind kanbanInputKind, it kanban.Item, value string) {
	a.kb.input = kanbanInputState{kind: kind, id: it.ID, ref: it.Short()}
	a.runsFocus = false
	a.editor.Reset()
	a.clearAutocomplete()
	a.editor.SetValue(value)
	a.editor.CursorEnd()
}

// endKanbanInput closes the prompt and gives the panel its focus back.
func (a *App) endKanbanInput() {
	a.resetKanbanInput()
	a.runsFocus = a.runsOpen
}

// resetKanbanInput drops an open kanban-tab prompt.
func (a *App) resetKanbanInput() {
	if a.kanbanInputActive() {
		a.editor.Reset()
		a.kb.input = kanbanInputState{}
	}
}

// kanbanComposerTitle is the composer title and meta while a kanban-tab
// prompt owns it; ok is false otherwise.
func (a *App) kanbanComposerTitle() (title, meta string, ok bool) {
	if !a.kanbanInputActive() {
		return "", "", false
	}
	in := a.kb.input
	switch in.kind {
	case kanbanInputFilter:
		return "filter the kanban tab", "⏎ apply · esc cancel", true
	case kanbanInputNew:
		to := a.kanbanTabNewList()
		return "new kanban item in " + strings.ReplaceAll(string(to), "_", " ") + ": title", "⏎ add · esc cancel", true
	case kanbanInputNote:
		return "note for " + in.ref, "⏎ save · esc cancel", true
	case kanbanInputAssign:
		return "assign " + in.ref + " to a profile (empty clears)", "⏎ save · esc cancel", true
	case kanbanInputLabels:
		return "labels for " + in.ref + " (comma separated)", "⏎ save · esc cancel", true
	}
	return "", "", false
}

// kanbanTabNewList is where n files an item: the filter's list, or backlog
// under the open filter.
func (a *App) kanbanTabNewList() kanban.List {
	if f := kanbanTabFilters[clamp(a.kb.tab.filter, 0, len(kanbanTabFilters)-1)]; len(f.lists) == 1 {
		return f.lists[0]
	}
	return kanban.Backlog
}

// handleKanbanInputKey routes keys while a kanban-tab prompt owns the
// composer.
func (a *App) handleKanbanInputKey(m tea.KeyMsg) tea.Cmd {
	switch m.String() {
	case "esc":
		a.endKanbanInput()
		return nil
	case "enter":
	default:
		return a.editor.Update(m)
	}
	in := a.kb.input
	text := strings.TrimSpace(a.editor.Value())
	a.endKanbanInput()
	switch in.kind {
	case kanbanInputFilter:
		a.kb.tab.query = text
		a.runsSel, a.runsScroll = 0, 0
	case kanbanInputNew:
		if text == "" {
			a.kanbanNotice("a title is required")
			return nil
		}
		it, dup, err := a.kb.store.Add(kanban.ItemInput{Title: text, List: a.kanbanTabNewList()}, a.kb.src.Get())
		switch {
		case err != nil:
			a.kanbanNotice("%v", err)
		case dup:
			a.kanbanNotice("already on the board as %s", it.Short())
		default:
			a.kanbanNotice("%s added to %s", it.Short(), it.List)
		}
		a.kanbanTabSelect(it.ID)
	case kanbanInputNote, kanbanInputAssign, kanbanInputLabels:
		if in.kind == kanbanInputNote && text == "" {
			return nil
		}
		mode := map[kanbanInputKind]string{kanbanInputNote: "note", kanbanInputAssign: "assign", kanbanInputLabels: "labels"}[in.kind]
		if status, err := a.kanbanApplyField(mode, in.id, text); err != nil {
			a.kanbanNotice("%s: %v", in.ref, err)
		} else {
			a.kanbanNotice("%s", status)
		}
	}
	return nil
}

// crewItems lists the fleet workers as runs rows, live ones first.
func (a *App) crewItems() []runsItem {
	now := time.Now()
	recs := a.agentState.fleet.recs
	out := make([]runsItem, 0, len(recs))
	for _, r := range recs {
		parts := []string{string(r.State)}
		if r.Item != "" {
			parts = append(parts, r.Item)
		}
		parts = append(parts, fmt.Sprintf("✓%d ✗%d", r.Done, r.Failed))
		if r.State.Live() && r.Beat > 0 {
			parts = append(parts, "♥ "+compactDuration(now.Sub(time.UnixMilli(r.Beat))))
		}
		if r.Crew != "" {
			parts = append(parts, r.Crew)
		}
		if !r.State.Live() && r.Reason != "" {
			parts = append(parts, r.Reason)
		}
		st := "done"
		switch r.State {
		case fleet.StateWorking, fleet.StateIdle, fleet.StateStarting, fleet.StateStopping:
			st = "running"
		case fleet.StateFailed:
			st = "failed"
		}
		out = append(out, runsItem{ID: r.ID, Label: r.ID, Detail: strings.Join(parts, " · "), State: st})
	}
	return out
}

// chosenCrew is the crew c cycles to and s starts.
func (a *App) chosenCrew() (agentprofile.Crew, bool) {
	crews := agentprofile.ListCrews()
	if len(crews) == 0 {
		return agentprofile.Crew{}, false
	}
	f := &a.agentState.fleet
	if f.crew == "" {
		f.crew = "belai:delivery"
	}
	for _, c := range crews {
		if c.Name == f.crew {
			return c, true
		}
	}
	f.crew = crews[0].Name
	return crews[0], true
}

// crewSummary is the chosen crew, the worker count against max_workers and
// the board counts; with l on, the selected worker's log tail follows.
func (a *App) crewSummary() []string {
	var lines []string
	head := fmt.Sprintf("%d of %d workers running", a.fleetLive(), a.settings.MaxWorkers())
	if c, ok := a.chosenCrew(); ok {
		var members []string
		for _, m := range c.Members {
			members = append(members, fmt.Sprintf("%s ×%d", strings.TrimPrefix(m.Profile, "belai:"), m.Count()))
		}
		head = "crew " + c.Name + " (" + strings.Join(members, ", ") + ") · " + head
	}
	if a.kb != nil {
		if s := a.kanbanStatus(); s != "" {
			head += " · board: " + s
		}
	}
	lines = append(lines, head)
	f := &a.agentState.fleet
	if f.panelLog {
		if len(f.panelLines) == 0 {
			lines = append(lines, "(no log for this worker)")
		}
		for _, l := range f.panelLines {
			lines = append(lines, "│ "+cleanFleetLog(l))
		}
	}
	return lines
}

// reloadCrewLog reads the selected worker's log tail for the crew tab.
func (a *App) reloadCrewLog() {
	f := &a.agentState.fleet
	f.panelLines = nil
	if !f.panelLog || a.runsSel < 0 || a.runsSel >= len(f.recs) {
		return
	}
	reg, err := a.fleetRegistry()
	if err != nil {
		return
	}
	f.panelLines = tailFile(filepath.Join(reg.LogDir(), f.recs[a.runsSel].ID+".log"), 4)
}

// handleRunsCrewKey routes the crew tab's keys.
func (a *App) handleRunsCrewKey(m tea.KeyMsg) tea.Cmd {
	f := &a.agentState.fleet
	switch m.String() {
	case "c":
		crews := agentprofile.ListCrews()
		if len(crews) == 0 {
			return nil
		}
		cur, _ := a.chosenCrew()
		i := slices.IndexFunc(crews, func(c agentprofile.Crew) bool { return c.Name == cur.Name })
		f.crew = crews[(i+1)%len(crews)].Name
		return nil
	case "s":
		if c, ok := a.chosenCrew(); ok {
			a.startFleetWorkers("", c.Name, 0)
			a.reloadFleet()
			return a.fleetTick()
		}
		return nil
	case "r":
		a.reloadFleet()
		a.reloadCrewLog()
		return nil
	case "l":
		f.panelLog = !f.panelLog
		a.reloadCrewLog()
		return nil
	case "v":
		f.loaded = time.Time{}
		a.runsFocus = false
		return tea.Batch(a.openAgentsTab(agentTabFleet), a.fleetTick())
	case "X":
		return a.stopFleetWorkers(nil)
	}
	if a.runsSel < 0 || a.runsSel >= len(f.recs) {
		return nil
	}
	r := f.recs[a.runsSel]
	switch m.String() {
	case "x":
		if r.State.Live() {
			return a.stopFleetWorkers(&r)
		}
	case "enter":
		if r.Item == "" || a.kb == nil {
			return nil
		}
		it, err := a.kb.store.Get(r.Item)
		if err != nil {
			a.kanbanNotice("%s: %v", r.Item, err)
			return nil
		}
		// Show it on the kanban tab under a filter that lists it.
		a.kb.tab.query, a.kb.tab.filter, a.kb.tab.all = "", 0, true
		for i, f := range kanbanTabFilters {
			if len(f.lists) == 1 && f.lists[0] == it.List {
				a.kb.tab.filter = i
			}
		}
		a.runsTab, a.runsSel, a.runsScroll = tabKanban, 0, 0
		a.kanbanTabSelect(it.ID)
	}
	return nil
}

// stopFleetWorkers stops one worker, or every live one when rec is nil, off
// the Bubble Tea goroutine; a fleet tick reloads the list afterwards.
func (a *App) stopFleetWorkers(rec *fleet.Record) tea.Cmd {
	reg, err := a.fleetRegistry()
	if err != nil {
		a.agentNotice("fleet: " + err.Error())
		return nil
	}
	var targets []fleet.Record
	if rec == nil {
		targets, _ = reg.Live()
	} else {
		targets = []fleet.Record{*rec}
	}
	if len(targets) == 0 {
		return nil
	}
	return func() tea.Msg {
		for _, r := range targets {
			_ = reg.Stop(r, 20*time.Second)
		}
		return fleetTickMsg{}
	}
}
