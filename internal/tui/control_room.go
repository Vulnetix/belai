package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/crewlog"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tui/components"
)

// The crew control room (/crew) is one crew run's operations screen: its
// workers, the cards it works, its providers and fallbacks, its scope of
// directories, its faults and its audit trail, on six tabs, refreshed every
// two seconds. Every change it makes goes to the run file or a worker's
// control file (docs/fleet.md, "Live controls") and applies at the workers'
// next claim or round; nothing waits for a restart. Every change is recorded
// as a crew.control fact in the run's crew log.

// Control room tabs.
const (
	crWorkers = iota
	crBoard
	crProviders
	crScope
	crFaults
	crAudit
	crTabs
)

var crTabNames = [crTabs]string{"workers", "board", "providers", "scope", "faults", "audit"}

// controlRoom is the screen's state.
type controlRoom struct {
	runs    []fleet.CrewRun
	runID   string
	run     fleet.CrewRun
	hasRun  bool
	tab     int
	sel     [crTabs]int
	workers []fleet.Record
	acks    map[string]fleet.ControlAck
	cards   []kanban.Item
	marks   map[string]bool
	faults  []crewlog.Fault
	entries []crewlog.Entry
	chain   int64 // the audit chain's first bad seq, -1 when intact
	class   string
	done    bool // the board shows done cards too
	showLog bool
	log     []string
	// mode is an open editor or confirmation; see handleControlRoomKey.
	mode    string
	status  string
	errMsg  string
	ticking bool
	loaded  time.Time
	now     func() time.Time
}

type controlRoomTickMsg struct{}

func (c *controlRoom) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (a *App) enterControlRoom() tea.Cmd {
	cr := &a.crew
	cr.mode, cr.errMsg = "", ""
	a.reloadControlRoom()
	return a.controlRoomTick()
}

func (a *App) controlRoomTick() tea.Cmd {
	if a.crew.ticking {
		return nil
	}
	a.crew.ticking = true
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return controlRoomTickMsg{} })
}

func (a *App) handleControlRoomTick() tea.Cmd {
	a.crew.ticking = false
	if a.view != viewControlRoom {
		return nil
	}
	if a.crew.mode == "" {
		a.reloadControlRoom()
	}
	return a.controlRoomTick()
}

// reloadControlRoom rereads the runs, the chosen run's workers, cards,
// faults and audit.
func (a *App) reloadControlRoom() {
	cr := &a.crew
	reg, err := a.fleetRegistry()
	if err != nil {
		cr.errMsg = "crew: " + err.Error()
		return
	}
	runs, _ := reg.ListRuns()
	// A run that lost its supervisor gets a new one when the room looks.
	for _, r := range runs {
		if r.State == fleet.RunOrphaned {
			if start, err := a.supervisorStarter(reg); err == nil && len(reg.AdoptOrphans("human", start)) > 0 {
				runs, _ = reg.ListRuns()
			}
			break
		}
	}
	// Live runs first, then the last few that ended.
	var live, ended []fleet.CrewRun
	for _, r := range runs {
		if r.State.Live() {
			live = append(live, r)
		} else if len(ended) < 6 {
			ended = append(ended, r)
		}
	}
	cr.runs = append(live, ended...)
	cr.hasRun = false
	if cr.runID == "" && len(cr.runs) > 0 {
		cr.runID = cr.runs[0].ID
	}
	for _, r := range cr.runs {
		if r.ID == cr.runID {
			cr.run, cr.hasRun = r, true
		}
	}
	if !cr.hasRun && len(cr.runs) > 0 {
		cr.run, cr.runID, cr.hasRun = cr.runs[0], cr.runs[0].ID, true
	}
	cr.loaded = cr.clock()
	cr.workers, cr.cards, cr.faults, cr.entries = nil, nil, nil, nil
	if !cr.hasRun {
		return
	}
	all, _ := reg.List()
	cr.acks = map[string]fleet.ControlAck{}
	for _, rec := range all {
		if rec.Run != cr.run.ID || (!rec.State.Live() && cr.clock().Sub(time.UnixMilli(rec.Stopped)) > time.Hour) {
			continue
		}
		cr.workers = append(cr.workers, rec)
		if ack, ok := reg.Ack(rec.ID); ok {
			cr.acks[rec.ID] = ack
		}
	}
	sort.SliceStable(cr.workers, func(i, j int) bool {
		if li, lj := cr.workers[i].State.Live(), cr.workers[j].State.Live(); li != lj {
			return li
		}
		if cr.workers[i].Repo != cr.workers[j].Repo {
			return cr.workers[i].Repo < cr.workers[j].Repo
		}
		return cr.workers[i].ID < cr.workers[j].ID
	})
	cr.cards = a.crewCards()
	if gd, err := config.GlobalDir(); err == nil {
		dir := crewlog.Dir(gd, cr.run.ID)
		cr.faults, _ = crewlog.ReadFaults(dir, crewlog.Filter{Class: cr.class, Limit: 200})
		slices.Reverse(cr.faults)
		cr.entries, _ = crewlog.ReadEntries(dir, crewlog.Filter{Limit: 200})
		slices.Reverse(cr.entries)
		cr.chain, _ = crewlog.Verify(dir)
	}
	for t := range crTabs {
		cr.sel[t] = clamp(cr.sel[t], 0, max(0, a.crewRows(t)-1))
	}
	cr.log = nil
	if cr.showLog && cr.tab == crWorkers && cr.sel[crWorkers] < len(cr.workers) {
		cr.log = tailFile(filepath.Join(reg.LogDir(), cr.workers[cr.sel[crWorkers]].ID+".log"), 10)
	}
}

// crewRows is the number of rows on a tab.
func (a *App) crewRows(tab int) int {
	cr := &a.crew
	switch tab {
	case crWorkers:
		return len(cr.workers)
	case crBoard:
		return len(cr.cards)
	case crProviders:
		return len(cr.run.Pool)
	case crScope:
		return len(cr.run.Scope) + len(cr.run.Members)
	case crFaults:
		return len(cr.faults)
	case crAudit:
		return len(cr.entries)
	}
	return 0
}

// crewScopeProjects are the board projects of the run's scope, by name and
// by key.
func (a *App) crewScopeProjects() map[string]string {
	out := map[string]string{}
	for _, d := range a.crew.run.Scope {
		name, key := kanban.ProjectFor(d.Path)
		out[key] = d.Path
		out[strings.ToLower(name)] = d.Path
	}
	return out
}

// crewCards are the cards the crew works or may claim in its scope: the ones
// its workers hold, the ones assigned to it or to a member, and the ones that
// carry a member's claim labels.
func (a *App) crewCards() []kanban.Item {
	cr := &a.crew
	store := kanbanStoreOf(a)
	if store == nil {
		return nil
	}
	lists := []kanban.List{kanban.Backlog, kanban.Review, kanban.InProgress, kanban.Blocked}
	if cr.done {
		lists = append(lists, kanban.Done)
	}
	q := kanban.Query{Lists: lists, Crew: cr.run.Crew, Limit: kanban.MaxItems}
	workers := map[string]bool{}
	for _, w := range cr.workers {
		workers[w.ID] = true
		q.ClaimedBy = append(q.ClaimedBy, w.ID)
	}
	for _, m := range cr.run.Members {
		q.Profile = append(q.Profile, m.Profile)
		if p, err := agentprofile.Load(m.Profile); err == nil && p.Kanban != nil && len(p.Kanban.Labels) > 0 {
			q.LabelSets = append(q.LabelSets, p.Kanban.Labels)
		}
	}
	items, err := store.Search(q)
	if err != nil {
		return nil
	}
	// The board is shared across projects: keep the cards in the run's scope,
	// and any card one of its workers holds wherever it lives.
	scope := a.crewScopeProjects()
	var out []kanban.Item
	for _, it := range items {
		_, inKey := scope[it.ProjectKey]
		_, inName := scope[strings.ToLower(it.Project)]
		if inKey || inName || workers[it.ClaimedBy] {
			out = append(out, it)
		}
	}
	order := map[kanban.List]int{kanban.InProgress: 0, kanban.Blocked: 1, kanban.Review: 2, kanban.Backlog: 3, kanban.Done: 4}
	sort.SliceStable(out, func(i, j int) bool {
		if order[out[i].List] != order[out[j].List] {
			return order[out[i].List] < order[out[j].List]
		}
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].Created < out[j].Created
	})
	return out
}

// targetHealth is one pool target's state across the run's live workers.
type targetHealth struct {
	target  string
	workers int
	outages int
	down    bool
}

// crewHealth is the state of each target the live workers are on, pool
// targets first, in pool order.
func (a *App) crewHealth() []targetHealth {
	cr := &a.crew
	by := map[string]*targetHealth{}
	var order []string
	add := func(t string) *targetHealth {
		h, ok := by[t]
		if !ok {
			h = &targetHealth{target: t}
			by[t] = h
			order = append(order, t)
		}
		return h
	}
	for _, t := range cr.run.Pool {
		add(t.String())
	}
	now := cr.clock().UnixMilli()
	for _, w := range cr.workers {
		if !w.State.Live() {
			continue
		}
		t := strings.Trim(w.Provider+"/"+w.Model, "/")
		if t == "" {
			t = "profile's model"
		}
		h := add(t)
		h.workers++
		h.outages += w.Outages
		if w.OutageUntil > now || w.Outages >= cr.run.SwitchAfterOr() {
			h.down = true
		}
	}
	out := make([]targetHealth, 0, len(order))
	for _, t := range order {
		out = append(out, *by[t])
	}
	return out
}

func (h targetHealth) label() (string, string) {
	switch {
	case h.down:
		return "down", "failed"
	case h.outages > 0:
		return "degraded", "paused"
	case h.workers == 0:
		return "idle", "idle"
	}
	return "ok", "running"
}

// poolPhaseText says where the crew stands against its pool.
func (a *App) poolPhaseText() (string, bool) {
	cr := &a.crew
	exhausted := 0
	for _, w := range cr.workers {
		if w.State.Live() && w.PoolExhausted {
			exhausted++
		}
	}
	switch cr.run.PoolPhase {
	case fleet.PoolSleeping:
		left := time.UnixMilli(cr.run.SleepUntil).Sub(cr.clock())
		if left < 0 {
			left = 0
		}
		return fmt.Sprintf("every target failed: the crew sleeps, final retry in %s", compactDuration(left)), true
	case fleet.PoolFinalRetry:
		return "final retry over the pool: the run ends if every target fails again", true
	}
	if exhausted > 0 {
		return fmt.Sprintf("%d worker(s) found every target failing", exhausted), true
	}
	return "", false
}

// controlRoomStatus is the screen switcher's summary.
func (a *App) controlRoomStatus() string {
	reg, err := a.fleetRegistry()
	if err != nil {
		return ""
	}
	runs, _ := reg.ListRuns()
	n := 0
	for _, r := range runs {
		if r.State.Live() {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d crew %s running", n, pluralWord(n, "run", "runs"))
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func (a *App) controlRoomView() string {
	cr := &a.crew
	w := a.contentWidth()
	if cr.loaded.IsZero() {
		a.reloadControlRoom()
	}
	var b strings.Builder
	title := "crew control room"
	sub := "no crew running"
	if cr.hasRun {
		sub = fmt.Sprintf("%s · %s · %s", cr.run.Crew, cr.run.ID, cr.run.State)
	}
	b.WriteString(components.SectionHeader(title, sub, w) + "\n")
	if !cr.hasRun {
		b.WriteString("\n" + components.MutedStyle.Render("  n launches a crew over a scope of directories: CREW DIR[:WEIGHT] …") + "\n")
		b.WriteString(components.MutedStyle.Render("  (`belai agent start -crew CREW -dir PATH[:WEIGHT]` does the same)") + "\n")
		b.WriteString(a.crewFooter(w))
		return b.String()
	}
	b.WriteString(a.roomSummary(w))
	b.WriteString(a.crewTabStrip(w) + "\n")
	switch cr.tab {
	case crWorkers:
		b.WriteString(a.crewWorkersView(w))
	case crBoard:
		b.WriteString(a.crewBoardView(w))
	case crProviders:
		b.WriteString(a.crewProvidersView(w))
	case crScope:
		b.WriteString(a.crewScopeView(w))
	case crFaults:
		b.WriteString(a.crewFaultsView(w))
	case crAudit:
		b.WriteString(a.crewAuditView(w))
	}
	b.WriteString(a.crewFooter(w))
	return b.String()
}

// crewSummary is the header: workers against replicas and the cap, the
// primary and the fallbacks, provider health, and the run's switcher.
func (a *App) roomSummary(w int) string {
	cr := &a.crew
	live, working, draining := 0, 0, 0
	tokens := 0
	for _, r := range cr.workers {
		if r.State.Live() {
			live++
			tokens += r.TokensLive
		}
		if r.State == fleet.StateWorking {
			working++
		}
		if r.Draining {
			draining++
		}
	}
	capN := cr.run.MaxWorkers
	if capN <= 0 {
		capN = a.settings.MaxWorkers()
	}
	line1 := fmt.Sprintf("  workers %d/%d (cap %d) · working %d", live, cr.run.Replicas(), capN, working)
	if draining > 0 {
		line1 += fmt.Sprintf(" · draining %d", draining)
	}
	if tokens > 0 {
		line1 += " · " + components.FormatTokens(tokens) + " tokens in hand"
	}
	line1 += fmt.Sprintf(" · %d %s", len(cr.run.Scope), pluralWord(len(cr.run.Scope), "dir", "dirs"))
	line2 := "  pool: each profile's model"
	if len(cr.run.Pool) > 0 {
		var pool []string
		for _, t := range cr.run.Pool {
			pool = append(pool, t.String())
		}
		line2 = "  pool " + strings.Join(pool, " → ") + " ↺"
	}
	var chips []string
	for _, h := range a.crewHealth() {
		if h.workers == 0 {
			continue
		}
		word, st := h.label()
		chips = append(chips, agentStateStyle(st).Render(fmt.Sprintf("%s %s ×%d", h.target, word, h.workers)))
	}
	if phase, ok := a.poolPhaseText(); ok {
		chips = append(chips, components.WarnStyle.Render(phase))
	}
	var runs []string
	for _, r := range cr.runs {
		label := r.Crew
		if !r.State.Live() {
			label += " (" + string(r.State) + ")"
		}
		if r.ID == cr.run.ID {
			label = components.EmphStyle.Render("[" + label + "]")
		} else {
			label = components.MutedStyle.Render(label)
		}
		runs = append(runs, label)
	}
	var b strings.Builder
	b.WriteString(ansi.Truncate(components.MutedStyle.Render(line1), w, "…") + "\n")
	b.WriteString(ansi.Truncate(components.MutedStyle.Render(line2), w, "…") + "\n")
	if len(chips) > 0 {
		b.WriteString(ansi.Truncate("  "+strings.Join(chips, "  "), w, "…") + "\n")
	}
	if cr.run.Reason != "" && !cr.run.State.Live() {
		b.WriteString(ansi.Truncate(components.MutedStyle.Render("  ended: "+sanitize.Line(cr.run.Reason, 200)), w, "…") + "\n")
	}
	if len(runs) > 1 {
		b.WriteString(ansi.Truncate("  "+components.MutedStyle.Render("crews [ ] ")+strings.Join(runs, " "), w, "…") + "\n")
	}
	return b.String()
}

func (a *App) crewTabStrip(w int) string {
	cr := &a.crew
	var parts []string
	for i, n := range crTabNames {
		label := fmt.Sprintf("%d %s", i+1, n)
		switch i {
		case crWorkers:
			label += fmt.Sprintf(" %d", len(cr.workers))
		case crBoard:
			label += fmt.Sprintf(" %d", len(cr.cards))
		case crFaults:
			label += fmt.Sprintf(" %d", len(cr.faults))
		}
		if i == cr.tab {
			parts = append(parts, components.EmphStyle.Render("["+label+"]"))
		} else {
			parts = append(parts, components.MutedStyle.Render(" "+label+" "))
		}
	}
	return ansi.Truncate("  "+strings.Join(parts, " "), w, "…")
}

func (a *App) crewWorkersView(w int) string {
	cr := &a.crew
	var b strings.Builder
	if len(cr.workers) == 0 {
		b.WriteString(components.MutedStyle.Render("  no workers yet: the supervisor starts them within a few seconds") + "\n")
		return b.String()
	}
	now := cr.clock()
	for i, r := range cr.workers {
		selected := i == cr.sel[crWorkers]
		st := fleetStateStyle(r.State)
		state := string(r.State)
		if r.Draining && r.State.Live() {
			state = "draining"
		}
		label := fmt.Sprintf("%-24s", truncateValue(r.ID, 24))
		if selected {
			label = components.EmphStyle.Render(label)
		}
		line := components.Cursor(selected) + agentStateStyle(st).Render(a.pulseGlyph(st)) + " " + label +
			agentStateStyle(st).Render(fmt.Sprintf("%-9s", state))
		parts := []string{components.MutedStyle.Render(fmt.Sprintf("%-14s", truncateValue(filepath.Base(r.Repo), 14)))}
		if r.Item != "" {
			parts = append(parts, components.AccentStyle.Render(r.Item))
		}
		model := r.Model
		if r.Provider != "" {
			model = r.Provider + "/" + r.Model
		}
		if model != "" {
			parts = append(parts, components.MutedStyle.Render(model))
		}
		if r.Sleeping {
			parts = append(parts, components.WarnStyle.Render("asleep"))
		} else if r.PoolExhausted {
			parts = append(parts, components.WarnStyle.Render("pool spent"))
		}
		if r.Outages > 0 {
			o := fmt.Sprintf("outages %d", r.Outages)
			if r.OutageUntil > now.UnixMilli() {
				o += " · retry in " + compactDuration(time.UnixMilli(r.OutageUntil).Sub(now))
			}
			parts = append(parts, components.WarnStyle.Render(o))
		}
		if r.TokensLive > 0 {
			parts = append(parts, components.MutedStyle.Render(components.FormatTokens(r.TokensLive)+" tok"))
		}
		parts = append(parts, components.MutedStyle.Render(fmt.Sprintf("✓%d ✗%d", r.Done, r.Failed)))
		if r.State.Live() && r.Beat > 0 {
			parts = append(parts, components.MutedStyle.Render("♥ "+compactDuration(now.Sub(time.UnixMilli(r.Beat)))))
		}
		if r.LastFault != "" {
			parts = append(parts, components.DangerStyle.Render(r.LastFault))
		}
		b.WriteString(ansi.Truncate(line+strings.Join(parts, "  "), w, "…") + "\n")
	}
	if i := cr.sel[crWorkers]; i < len(cr.workers) {
		r := cr.workers[i]
		b.WriteString("\n" + components.MutedStyle.Render(ansi.Truncate(fmt.Sprintf("  %s · %s", r.Profile, r.Repo), w, "…")) + "\n")
		if r.Branch != "" {
			b.WriteString(components.MutedStyle.Render(ansi.Truncate("  branch "+r.Branch+" · session "+r.Session, w, "…")) + "\n")
		}
		if n := len(cr.run.Pool); n > 0 && r.PoolIndex < n {
			b.WriteString(components.MutedStyle.Render(ansi.Truncate(fmt.Sprintf("  pool target %d of %d", r.PoolIndex+1, n), w, "…")) + "\n")
		}
		if ack, ok := cr.acks[r.ID]; ok {
			line := fmt.Sprintf("  control %d %s", ack.Seq, ack.Status)
			if ack.Reason != "" {
				line += ": " + ack.Reason
			}
			style := components.MutedStyle
			if ack.Status == fleet.AckRefused {
				style = components.DangerStyle
			}
			b.WriteString(style.Render(ansi.Truncate(line, w, "…")) + "\n")
		}
		if !r.State.Live() && r.Reason != "" {
			b.WriteString(components.MutedStyle.Render(ansi.Truncate("  "+sanitize.Line(r.Reason, 240), w, "…")) + "\n")
		}
		if cr.showLog {
			if len(cr.log) == 0 {
				b.WriteString(components.MutedStyle.Render("  (no log yet)") + "\n")
			}
			for _, l := range cr.log {
				b.WriteString(components.MutedStyle.Render(ansi.Truncate("  "+cleanFleetLog(l), w, "…")) + "\n")
			}
		}
	}
	return b.String()
}

func (a *App) crewBoardView(w int) string {
	cr := &a.crew
	var b strings.Builder
	counts := map[kanban.List]int{}
	for _, it := range cr.cards {
		counts[it.List]++
	}
	var cs []string
	for _, l := range kanban.Lists {
		if n := counts[l]; n > 0 {
			cs = append(cs, fmt.Sprintf("%s %d", l, n))
		}
	}
	marked := len(cr.marks)
	head := "  " + strings.Join(cs, " · ")
	if marked > 0 {
		head += fmt.Sprintf(" · %d marked", marked)
	}
	b.WriteString(components.MutedStyle.Render(ansi.Truncate(head, w, "…")) + "\n")
	if len(cr.cards) == 0 {
		b.WriteString(components.MutedStyle.Render("  no cards for this crew in its scope") + "\n")
		return b.String()
	}
	workers := map[string]fleet.Record{}
	for _, r := range cr.workers {
		workers[r.ID] = r
	}
	now := cr.clock().UnixMilli()
	start, end := window(cr.sel[crBoard], len(cr.cards), max(6, a.height-16))
	for i := start; i < end; i++ {
		it := cr.cards[i]
		selected := i == cr.sel[crBoard]
		mark := "  "
		if cr.marks[it.ID] {
			mark = components.AccentStyle.Render("● ")
		}
		title := truncateValue(kanban.CleanTitle(it.Title), 40)
		if selected {
			title = components.EmphStyle.Render(title)
		}
		line := components.Cursor(selected) + mark + kanbanChip(it.List) + " " + components.AccentStyle.Render(it.Short()) + " " + title
		var parts []string
		if it.Priority != 0 {
			parts = append(parts, fmt.Sprintf("p%+d", it.Priority))
		}
		if it.Attempts > 0 || it.Bounces > 0 {
			parts = append(parts, fmt.Sprintf("tries %d bounces %d", it.Attempts, it.Bounces))
		}
		if it.ClaimedBy != "" {
			who := it.ClaimedBy
			if r, ok := workers[who]; ok {
				who = r.Profile + " " + filepath.Base(r.Repo)
			}
			if it.LeaseUntil > now {
				who += " lease " + compactDuration(time.Duration(it.LeaseUntil-now)*time.Millisecond)
			}
			parts = append(parts, who)
		}
		if it.Assignee != "" {
			parts = append(parts, "→ "+it.Assignee)
		}
		b.WriteString(ansi.Truncate(line+"  "+components.MutedStyle.Render(strings.Join(parts, "  ")), w, "…") + "\n")
	}
	if i := cr.sel[crBoard]; i < len(cr.cards) {
		it := cr.cards[i]
		if n := len(it.History); n > 0 {
			last := it.History[n-1]
			b.WriteString("\n" + components.MutedStyle.Render(ansi.Truncate("  last: "+sanitize.Line(last.Note, 200), w, "…")) + "\n")
		}
	}
	return b.String()
}

// window is the slice of n rows that keeps sel in view within h rows.
func window(sel, n, h int) (int, int) {
	if n <= h {
		return 0, n
	}
	start := min(max(sel-h/2, 0), n-h)
	return start, start + h
}

func (a *App) crewProvidersView(w int) string {
	cr := &a.crew
	var b strings.Builder
	if len(cr.run.Pool) == 0 {
		b.WriteString(components.MutedStyle.Render("  no pool: each worker runs its profile's model, and an outage only backs it off. P sets a pool") + "\n")
	}
	health := map[string]targetHealth{}
	for _, h := range a.crewHealth() {
		health[h.target] = h
	}
	for i, t := range cr.run.Pool {
		selected := i == cr.sel[crProviders]
		h := health[t.String()]
		word, st := h.label()
		name := fmt.Sprintf("%d %-32s", i+1, truncateValue(t.String(), 32))
		if selected {
			name = components.EmphStyle.Render(name)
		}
		line := components.Cursor(selected) + name + " " + agentStateStyle(st).Render(fmt.Sprintf("%-9s", word)) +
			components.MutedStyle.Render(fmt.Sprintf(" workers %d · outages %d", h.workers, h.outages))
		b.WriteString(ansi.Truncate(line, w, "…") + "\n")
	}
	b.WriteString("\n" + components.MutedStyle.Render(fmt.Sprintf("  a worker moves to the next target after %d outages in a row, round the pool; once every target failed the crew sleeps %s for a final retry, then stops if it fails again",
		cr.run.SwitchAfterOr(), cr.run.FinalRetry())) + "\n")
	if phase, ok := a.poolPhaseText(); ok {
		b.WriteString(components.WarnStyle.Render("  "+phase) + "\n")
	}
	return b.String()
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func (a *App) crewScopeView(w int) string {
	cr := &a.crew
	var b strings.Builder
	busy := map[string]int{}
	held := map[string]int{}
	for _, r := range cr.workers {
		if r.State.Live() {
			busy[r.Repo]++
		}
	}
	scope := a.crewScopeProjects()
	open, blocked := map[string]int{}, map[string]int{}
	for _, it := range cr.cards {
		dir := scope[it.ProjectKey]
		if dir == "" {
			dir = scope[strings.ToLower(it.Project)]
		}
		switch it.List {
		case kanban.Blocked:
			blocked[dir]++
		case kanban.InProgress:
			held[dir]++
		case kanban.Backlog, kanban.Review:
			open[dir]++
		}
	}
	b.WriteString(components.MutedStyle.Render("  directories (weight · workers · open · in progress · blocked)") + "\n")
	for i, d := range cr.run.Scope {
		selected := i == cr.sel[crScope]
		name := truncateValue(d.Path, 44)
		if selected {
			name = components.EmphStyle.Render(name)
		}
		line := fmt.Sprintf("%s%-44s  w%-2d  %2d workers  %3d open  %2d held  %2d blocked", components.Cursor(selected), name, d.Weight, busy[d.Path], open[d.Path], held[d.Path], blocked[d.Path])
		if d.Paused {
			line += "  " + components.WarnStyle.Render("paused")
		}
		b.WriteString(ansi.Truncate(line, w, "…") + "\n")
	}
	b.WriteString("\n" + components.MutedStyle.Render("  members (replicas across the scope)") + "\n")
	running := map[string]int{}
	for _, r := range cr.workers {
		if r.State.Live() {
			running[r.Profile]++
		}
	}
	for j, m := range cr.run.Members {
		i := len(cr.run.Scope) + j
		selected := i == cr.sel[crScope]
		name := fmt.Sprintf("%-28s", m.Profile)
		if selected {
			name = components.EmphStyle.Render(name)
		}
		b.WriteString(ansi.Truncate(fmt.Sprintf("%s%s ×%d  (%d running)", components.Cursor(selected), name, m.Count(), running[m.Profile]), w, "…") + "\n")
	}
	return b.String()
}

func (a *App) crewFaultsView(w int) string {
	cr := &a.crew
	var b strings.Builder
	head := "  faults, newest first"
	if cr.class != "" {
		head += " · class " + cr.class
	}
	b.WriteString(components.MutedStyle.Render(head) + "\n")
	if len(cr.faults) == 0 {
		b.WriteString(components.MutedStyle.Render("  no faults recorded") + "\n")
		return b.String()
	}
	start, end := window(cr.sel[crFaults], len(cr.faults), max(6, a.height-16))
	for i := start; i < end; i++ {
		f := cr.faults[i]
		selected := i == cr.sel[crFaults]
		when := time.UnixMilli(f.At).Format("15:04:05")
		class := components.DangerStyle.Render(f.Class)
		if f.Outcome == "transient" {
			class = components.WarnStyle.Render(f.Class)
		}
		line := fmt.Sprintf("%s%s  %-22s %-10s %s", components.Cursor(selected), when, truncateValue(f.Worker, 22), truncateValue(f.Item, 10), class)
		if f.Status != 0 {
			line += fmt.Sprintf(" %d", f.Status)
		}
		line += "  " + components.MutedStyle.Render(f.Detail)
		b.WriteString(ansi.Truncate(line, w, "…") + "\n")
	}
	if i := cr.sel[crFaults]; i < len(cr.faults) {
		f := cr.faults[i]
		b.WriteString("\n" + components.MutedStyle.Render(ansi.Truncate(fmt.Sprintf("  %s · %s · %s/%s · session %s", f.Profile, f.Repo, f.Provider, f.Model, f.Session), w, "…")) + "\n")
	}
	return b.String()
}

func (a *App) crewAuditView(w int) string {
	cr := &a.crew
	var b strings.Builder
	chain := components.MutedStyle.Render("  chain intact")
	if cr.chain >= 0 {
		chain = components.DangerStyle.Render(fmt.Sprintf("  chain broken at seq %d", cr.chain))
	}
	b.WriteString(chain + "\n")
	if len(cr.entries) == 0 {
		b.WriteString(components.MutedStyle.Render("  no audit entries yet") + "\n")
		return b.String()
	}
	start, end := window(cr.sel[crAudit], len(cr.entries), max(6, a.height-16))
	for i := start; i < end; i++ {
		e := cr.entries[i]
		selected := i == cr.sel[crAudit]
		when := time.UnixMilli(e.At).Format("15:04:05")
		var data []string
		for _, k := range sortedKeys(e.Data) {
			data = append(data, k+"="+e.Data[k])
		}
		who := e.Worker
		if who == "" {
			who = e.ActorKind
		}
		line := fmt.Sprintf("%s%s #%-4d %-16s %-22s %-10s %s  %s", components.Cursor(selected), when, e.Seq, e.Kind, truncateValue(who, 22), truncateValue(e.Item, 10), e.Outcome,
			components.MutedStyle.Render(strings.Join(data, " ")))
		b.WriteString(ansi.Truncate(line, w, "…") + "\n")
	}
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (a *App) crewFooter(w int) string {
	cr := &a.crew
	var b strings.Builder
	switch {
	case cr.mode != "" && crewEditorModes[cr.mode] != "":
		b.WriteString("\n" + a.renderFieldEditor(crewEditorModes[cr.mode], w) + "\n")
	case cr.mode != "":
		b.WriteString("\n" + components.WarnStyle.Render("  "+crewConfirmText(cr)) + "\n")
	}
	if cr.errMsg != "" {
		b.WriteString("\n" + components.DangerStyle.Render(ansi.Truncate("  "+cr.errMsg, w, "…")) + "\n")
	} else if cr.status != "" {
		b.WriteString("\n" + components.MutedStyle.Render(ansi.Truncate("  "+cr.status, w, "…")) + "\n")
	}
	b.WriteString("\n" + crewHelp(cr.tab, cr.hasRun) + "\n")
	return b.String()
}

// crewHelp is the footer for a tab.
func crewHelp(tab int, hasRun bool) string {
	if !hasRun {
		return components.HelpBar("n", "launch", "r", "refresh", "esc", "back")
	}
	common := []string{"1-6", "tab", "[ ]", "crew", "n", "launch", "S", "stop run"}
	var keys []string
	switch tab {
	case crWorkers:
		keys = []string{"p", "pause", "d", "drain", "x", "stop", "M", "model", "E", "effort", "A", "auto", "l", "log"}
	case crBoard:
		keys = []string{"space", "mark", "a", "all", "m", "move", "b", "block", "u", "unblock", "+/-", "priority", "L", "labels", "A", "assign", "R", "release", "d", "delete", "o", "done"}
	case crProviders:
		keys = []string{"P", "pool", "f", "all to target", "g", "to the pool", "</>", "switch after", "{/}", "final retry"}
	case crScope:
		keys = []string{"+/-", "weight/replicas", "space", "pause dir", "a", "add dir", "D", "remove dir", "c", "max workers"}
	case crFaults:
		keys = []string{"c", "class", "enter", "card"}
	case crAudit:
		keys = []string{"enter", "card"}
	}
	return components.HelpBar(append(keys, append(common, "esc", "back")...)...)
}
