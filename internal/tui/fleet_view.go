package tui

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/gitinfo"
	"github.com/vulnetix/belai/internal/tui/components"
)

// The fleet tab of /agents lists the worker processes on this machine —
// detached `belai agent run` workers started here, from the CLI or from
// another session — read from the fleet registry. It is refreshed every two
// seconds while the tab is open.

// fleetUI is the fleet tab's state.
type fleetUI struct {
	recs    []fleet.Record
	sel     int
	showLog bool
	log     []string
	ticking bool
	loaded  time.Time
	// The runs panel's crew tab: the crew c cycles to and s starts, and its
	// log tail of the selected worker.
	crew       string
	panelLog   bool
	panelLines []string
}

type fleetTickMsg struct{}

// fleetRegistry opens the registry over the session's board.
func (a *App) fleetRegistry() (*fleet.Registry, error) {
	return fleet.OpenRegistry(kanbanStoreOf(a))
}

// reloadFleet rereads the registry, and the selected worker's log tail when
// it is shown.
func (a *App) reloadFleet() {
	f := &a.agentState.fleet
	reg, err := a.fleetRegistry()
	if err != nil {
		a.agentState.errorMsg = "fleet: " + err.Error()
		return
	}
	recs, err := reg.List()
	if err != nil {
		a.agentState.errorMsg = "fleet: " + err.Error()
		return
	}
	// Live workers first, then the most recently stopped few.
	var live, done []fleet.Record
	for _, r := range recs {
		if r.State.Live() {
			live = append(live, r)
		} else if len(done) < 8 {
			done = append(done, r)
		}
	}
	f.recs = append(live, done...)
	f.sel = clamp(f.sel, 0, max(0, len(f.recs)-1))
	f.loaded = time.Now()
	f.log = nil
	if f.showLog && f.sel < len(f.recs) {
		f.log = tailFile(filepath.Join(reg.LogDir(), f.recs[f.sel].ID+".log"), 12)
	}
}

// fleetTick keeps the tab fresh while it is open; only one tick is pending.
func (a *App) fleetTick() tea.Cmd {
	if a.agentState.fleet.ticking {
		return nil
	}
	a.agentState.fleet.ticking = true
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return fleetTickMsg{} })
}

func (a *App) handleFleetTick() tea.Cmd {
	a.agentState.fleet.ticking = false
	panel := a.runsOpen && a.runsTab == tabCrew
	if !panel && (a.view != viewAgent || a.agentState.tab != agentTabFleet) {
		return nil
	}
	a.reloadFleet()
	if panel {
		a.reloadCrewLog()
	}
	return a.fleetTick()
}

// fleetLive counts running workers, for the tab strip.
func (a *App) fleetLive() int {
	n := 0
	for _, r := range a.agentState.fleet.recs {
		if r.State.Live() {
			n++
		}
	}
	return n
}

func tailFile(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		out = append(out, sc.Text())
		if len(out) > n {
			out = out[1:]
		}
	}
	return out
}

func fleetStateStyle(s fleet.State) string {
	switch s {
	case fleet.StateWorking:
		return "running"
	case fleet.StateIdle, fleet.StateStarting:
		return "idle"
	case fleet.StateStopping:
		return "paused"
	case fleet.StateFailed:
		return "failed"
	}
	return "stopped"
}

func (a *App) fleetView(w int) string {
	f := &a.agentState.fleet
	if f.loaded.IsZero() {
		a.reloadFleet()
	}
	var b strings.Builder
	if len(f.recs) == 0 {
		b.WriteString(components.MutedStyle.Render("  no workers — s on a worker profile, /fleet start NAME, /fleet crew NAME, or `belai agent start`") + "\n")
		b.WriteString("\n" + components.HelpBar("2", "profiles", "esc", "back") + "\n")
		return b.String()
	}
	now := time.Now()
	for i, r := range f.recs {
		selected := i == f.sel
		st := fleetStateStyle(r.State)
		label := fmt.Sprintf("%-28s", truncateValue(r.ID, 28))
		if selected {
			label = components.EmphStyle.Render(label)
		}
		line := components.Cursor(selected) + agentStateStyle(st).Render(a.pulseGlyph(st)) + " " + label +
			agentStateStyle(st).Render(fmt.Sprintf("%-9s", r.State))
		var parts []string
		if r.Item != "" {
			parts = append(parts, components.AccentStyle.Render(r.Item))
		}
		parts = append(parts, components.MutedStyle.Render(fmt.Sprintf("✓%d ✗%d", r.Done, r.Failed)))
		if r.State.Live() && r.Beat > 0 {
			parts = append(parts, components.MutedStyle.Render("♥ "+compactDuration(now.Sub(time.UnixMilli(r.Beat)))))
		}
		if r.Crew != "" {
			parts = append(parts, components.MutedStyle.Render(r.Crew))
		}
		if !r.State.Live() && r.Reason != "" {
			parts = append(parts, components.MutedStyle.Render(r.Reason))
		}
		b.WriteString(ansi.Truncate(line+strings.Join(parts, "  "), w, "…") + "\n")
	}
	if f.sel < len(f.recs) {
		r := f.recs[f.sel]
		b.WriteString("\n" + components.MutedStyle.Render(ansi.Truncate(fmt.Sprintf("  %s · %s", r.Profile, r.Repo), w, "…")) + "\n")
		if r.Branch != "" {
			b.WriteString(components.MutedStyle.Render(ansi.Truncate("  branch "+r.Branch, w, "…")) + "\n")
		}
		if f.showLog {
			if len(f.log) == 0 {
				b.WriteString(components.MutedStyle.Render("  (no log — a foreground worker logs to its terminal)") + "\n")
			}
			for _, l := range f.log {
				b.WriteString(components.MutedStyle.Render(ansi.Truncate("  "+cleanFleetLog(l), w, "…")) + "\n")
			}
		}
	}
	b.WriteString("\n" + components.HelpBar("↑↓", "move", "l", "log", "x", "stop", "X", "stop all", "r", "refresh", "esc", "back") + "\n")
	return b.String()
}

// cleanFleetLog strips escapes and control runes from a log line.
func cleanFleetLog(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return -1
		}
		return r
	}, s)
}

func (a *App) handleFleetKey(m tea.KeyMsg) tea.Cmd {
	f := &a.agentState.fleet
	switch m.String() {
	case "esc":
		a.pop()
	case "up", "k":
		if f.sel > 0 {
			f.sel--
		}
		a.reloadFleet()
	case "down", "j":
		if f.sel < len(f.recs)-1 {
			f.sel++
		}
		a.reloadFleet()
	case "l":
		f.showLog = !f.showLog
		a.reloadFleet()
	case "r":
		a.reloadFleet()
	case "X":
		return a.stopFleetWorkers(nil)
	case "x":
		if f.sel < len(f.recs) && f.recs[f.sel].State.Live() {
			r := f.recs[f.sel]
			return a.stopFleetWorkers(&r)
		}
	}
	return nil
}

// startFleetWorkers starts detached workers for a profile or every member of
// a crew, in the session's repository. The repository is already trusted:
// the session is running in it.
func (a *App) startFleetWorkers(profile, crew string, replicas int) {
	notice := func(s string) { a.agentNotice(s) }
	repo := a.workdir
	if info, ok := gitinfo.Detect(a.workdir); ok && info.Root != "" {
		repo = info.Root
	}
	type launch struct{ profile, crew string }
	var launches []launch
	if crew != "" {
		c, err := agentprofile.LoadCrew(crew)
		if err != nil {
			notice("fleet: " + err.Error())
			return
		}
		for _, m := range c.Members {
			for range m.Count() {
				launches = append(launches, launch{m.Profile, c.Name})
			}
		}
	} else {
		for range max(replicas, 1) {
			launches = append(launches, launch{profile, ""})
		}
	}
	for _, l := range launches {
		p, err := agentprofile.Load(l.profile)
		if err != nil {
			notice("fleet: " + err.Error())
			return
		}
		if err := fleet.Preflight(p, a.settings, a.effectivePosture()); err != nil {
			notice("fleet: " + l.profile + ": " + err.Error())
			return
		}
	}
	reg, err := a.fleetRegistry()
	if err != nil {
		notice("fleet: " + err.Error())
		return
	}
	live, _ := reg.Live()
	if max := a.settings.MaxWorkers(); len(live)+len(launches) > max {
		notice(fmt.Sprintf("fleet: starting %d would run %d workers; agents.max_workers is %d", len(launches), len(live)+len(launches), max))
		return
	}
	exe, err := os.Executable()
	if err != nil {
		notice("fleet: " + err.Error())
		return
	}
	var ids []string
	for _, l := range launches {
		id, err := reg.Spawn(fleet.SpawnOptions{Exe: exe, Repo: repo, Profile: l.profile, Crew: l.crew})
		if err != nil {
			notice("fleet: " + err.Error())
			break
		}
		ids = append(ids, id)
	}
	if len(ids) > 0 {
		a.addSystem(fmt.Sprintf("⚙ started %d worker(s): %s — /agents fleet tab, or `belai agent ps`", len(ids), strings.Join(ids, ", ")))
	}
}
