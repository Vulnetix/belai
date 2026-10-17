package tui

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/gitinfo"
	"github.com/vulnetix/belai/internal/kanban"
)

// crewEditorModes are the control room's one-line editors and their titles.
var crewEditorModes = map[string]string{
	"launch":     "launch a crew: CREW DIR[:WEIGHT] … (default this repository)",
	"model":      "model for the selected worker: provider/model",
	"effort":     "effort for the selected worker",
	"primary":    "the crew's primary model: provider/model (empty: each profile's)",
	"fallbacks":  "fallbacks in order: provider/model provider/model …",
	"add-dir":    "add a directory to the scope: PATH[:WEIGHT]",
	"move":       "move the marked cards to: backlog, review, blocked or done",
	"labels":     "labels for the marked cards: +add -remove …",
	"assign":     "assign the marked cards to: PROFILE, crew:NAME, or empty to clear",
	"maxworkers": "max workers for this crew (0: agents.max_workers)",
}

// crewConfirmText is the question a confirmation mode asks.
func crewConfirmText(cr *controlRoom) string {
	switch cr.mode {
	case "stop-worker":
		return "stop the selected worker? it commits and hands back its card (y/n)"
	case "stop-run":
		return fmt.Sprintf("stop crew run %s and all its workers? (y/n)", cr.run.ID)
	case "delete-cards":
		return fmt.Sprintf("delete %d card(s)? (y/n)", len(cr.targets()))
	case "remove-dir":
		return "remove the selected directory from the scope? its workers finish their cards and leave (y/n)"
	}
	return ""
}

func (a *App) handleControlRoomKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	cr := &a.crew
	key := m.String()
	if cr.mode != "" {
		if _, ok := crewEditorModes[cr.mode]; ok {
			switch key {
			case "esc":
				cr.mode, cr.errMsg = "", ""
				a.editor.Reset()
				return a, nil
			case "enter":
				text := strings.TrimSpace(a.editor.Value())
				mode := cr.mode
				cr.mode = ""
				a.editor.Reset()
				a.crewCommit(mode, text)
				a.reloadControlRoom()
				return a, nil
			}
			return a, a.editor.Update(m)
		}
		mode := cr.mode
		cr.mode = ""
		if key == "y" {
			cmd := a.crewConfirm(mode)
			a.reloadControlRoom()
			return a, cmd
		}
		return a, nil
	}
	cr.errMsg, cr.status = "", ""
	switch key {
	case "esc":
		a.pop()
		return a, nil
	case "tab", "right":
		cr.tab = (cr.tab + 1) % crTabs
	case "shift+tab", "left":
		cr.tab = (cr.tab + crTabs - 1) % crTabs
	case "1", "2", "3", "4", "5", "6":
		cr.tab = int(key[0] - '1')
	case "up", "k":
		if cr.sel[cr.tab] > 0 {
			cr.sel[cr.tab]--
		}
	case "down", "j":
		if cr.sel[cr.tab] < a.crewRows(cr.tab)-1 {
			cr.sel[cr.tab]++
		}
	case "[", "]":
		a.crewSwitch(key == "]")
	case "r":
		cr.status = "refreshed"
	case "n":
		a.crewEdit("launch", a.crewLaunchDefault())
	case "S":
		if cr.hasRun && cr.run.State.Live() {
			cr.mode = "stop-run"
		}
	default:
		if cr.hasRun {
			a.crewTabKey(key)
		}
	}
	a.reloadControlRoom()
	return a, nil
}

// crewTabKey handles a tab's own keys.
func (a *App) crewTabKey(key string) {
	cr := &a.crew
	switch cr.tab {
	case crWorkers:
		rec, ok := a.crewWorker()
		switch key {
		case "l":
			cr.showLog = !cr.showLog
		case "p":
			if ok && rec.State.Live() {
				a.crewPause(rec)
			}
		case "d":
			if ok && rec.State.Live() {
				a.crewControlWorker(rec, "drain", func(c *fleet.Control) { c.Drain = true })
			}
		case "x":
			if ok && rec.State.Live() {
				cr.mode = "stop-worker"
			}
		case "M":
			if ok {
				a.crewEdit("model", strings.Trim(rec.Provider+"/"+rec.Model, "/"))
			}
		case "E":
			if ok {
				a.crewEdit("effort", rec.Effort)
			}
		case "A":
			if ok {
				a.crewControlWorker(rec, "auto", func(c *fleet.Control) { c.Auto, c.Target, c.Effort = true, fleet.Target{}, "" })
			}
		}
	case crBoard:
		a.crewBoardKey(key)
	case crProviders:
		switch key {
		case "P":
			a.crewEdit("primary", cr.run.Primary.String())
		case "F":
			var fb []string
			for _, t := range cr.run.Fallbacks {
				fb = append(fb, t.String())
			}
			a.crewEdit("fallbacks", strings.Join(fb, " "))
		case "f":
			if len(cr.run.Fallbacks) == 0 {
				cr.errMsg = "this crew has no fallback: F sets one"
				return
			}
			t := cr.run.Fallbacks[0]
			if i := cr.sel[crProviders]; i > 0 && i <= len(cr.run.Fallbacks) {
				t = cr.run.Fallbacks[i-1]
			}
			a.crewControlRun("force-fallback", map[string]string{"target": t.String()}, func(c *fleet.Control) { c.Target, c.Auto = t, false })
		case "g":
			t := cr.run.Primary
			a.crewControlRun("to-primary", map[string]string{"target": t.String()}, func(c *fleet.Control) { c.Target, c.Auto = t, true })
		case "<", ">":
			d := 1
			if key == "<" {
				d = -1
			}
			a.crewUpdateRun("fallback-after", nil, func(r *fleet.CrewRun) error {
				r.FallbackAfter = max(r.FallbackAfterOr()+d, 1)
				return nil
			})
		case "{", "}":
			d := 60
			if key == "{" {
				d = -60
			}
			a.crewUpdateRun("probe", nil, func(r *fleet.CrewRun) error {
				r.ProbeSecs = max(int(r.Probe().Seconds())+d, 60)
				return nil
			})
		}
	case crScope:
		a.crewScopeKey(key)
	case crFaults:
		switch key {
		case "c":
			a.crewCycleClass()
		case "enter":
			if i := cr.sel[crFaults]; i < len(cr.faults) && cr.faults[i].Item != "" {
				a.handleCommand("/kanban " + cr.faults[i].Item)
			}
		}
	case crAudit:
		if key == "enter" {
			if i := cr.sel[crAudit]; i < len(cr.entries) && cr.entries[i].Item != "" {
				if it, err := kanbanStoreOf(a).Get(cr.entries[i].Item); err == nil {
					a.handleCommand("/kanban " + it.Short())
				}
			}
		}
	}
}

func (a *App) crewBoardKey(key string) {
	cr := &a.crew
	store := kanbanStoreOf(a)
	if store == nil {
		cr.errMsg = "the kanban board is off"
		return
	}
	switch key {
	case " ":
		if i := cr.sel[crBoard]; i < len(cr.cards) {
			if cr.marks == nil {
				cr.marks = map[string]bool{}
			}
			id := cr.cards[i].ID
			if cr.marks[id] {
				delete(cr.marks, id)
			} else {
				cr.marks[id] = true
			}
			if i < len(cr.cards)-1 {
				cr.sel[crBoard]++
			}
		}
	case "a":
		if len(cr.marks) == len(cr.cards) {
			cr.marks = nil
		} else {
			cr.marks = map[string]bool{}
			for _, it := range cr.cards {
				cr.marks[it.ID] = true
			}
		}
	case "o":
		cr.done = !cr.done
	case "m":
		a.crewEdit("move", "")
	case "L":
		a.crewEdit("labels", "")
	case "A":
		a.crewEdit("assign", "crew:"+cr.run.Crew)
	case "d":
		if len(cr.targets()) > 0 {
			cr.mode = "delete-cards"
		}
	case "b":
		a.crewBulk("block", func(it kanban.Item) error {
			if it.ClaimedBy != "" {
				if _, err := store.Unclaim(it.ID, "released by the crew control room", a.sessionID, false); err != nil {
					return err
				}
			}
			_, err := store.Move(it.ID, kanban.Blocked, "blocked from the crew control room", a.sessionID)
			return err
		})
	case "u":
		a.crewBulk("unblock", func(it kanban.Item) error {
			if it.List != kanban.Blocked {
				return nil
			}
			_, err := store.Move(it.ID, kanban.Backlog, "unblocked from the crew control room", a.sessionID)
			return err
		})
	case "R":
		a.crewBulk("release", func(it kanban.Item) error {
			if it.ClaimedBy == "" {
				return nil
			}
			_, err := store.Unclaim(it.ID, "claim released from the crew control room", a.sessionID, false)
			return err
		})
	case "+", "=", "-":
		d := 1
		if key == "-" {
			d = -1
		}
		a.crewBulk("priority", func(it kanban.Item) error {
			p := kanban.ClampPriority(it.Priority + d)
			_, err := store.Route(it.ID, kanban.RoutePatch{Priority: &p}, a.sessionID)
			return err
		})
	}
}

func (a *App) crewScopeKey(key string) {
	cr := &a.crew
	i := cr.sel[crScope]
	inScope := i < len(cr.run.Scope)
	switch key {
	case "+", "=", "-":
		d := 1
		if key == "-" {
			d = -1
		}
		if inScope {
			path := cr.run.Scope[i].Path
			a.crewUpdateRun("weight", map[string]string{"repo": path}, func(r *fleet.CrewRun) error {
				for k := range r.Scope {
					if r.Scope[k].Path == path {
						r.Scope[k].Weight = min(max(r.Scope[k].Weight+d, fleet.MinWeight), fleet.MaxWeight)
					}
				}
				return nil
			})
		} else if j := i - len(cr.run.Scope); j < len(cr.run.Members) {
			profile := cr.run.Members[j].Profile
			a.crewUpdateRun("replicas", map[string]string{"profile": profile}, func(r *fleet.CrewRun) error {
				for k := range r.Members {
					if r.Members[k].Profile == profile {
						r.Members[k].Replicas = min(max(r.Members[k].Count()+d, 1), agentprofile.MaxReplicas)
					}
				}
				return nil
			})
		}
	case " ":
		if inScope {
			path := cr.run.Scope[i].Path
			action := "pause-dir"
			if cr.run.Scope[i].Paused {
				action = "resume-dir"
			}
			a.crewUpdateRun(action, map[string]string{"repo": path}, func(r *fleet.CrewRun) error {
				for k := range r.Scope {
					if r.Scope[k].Path == path {
						r.Scope[k].Paused = !r.Scope[k].Paused
					}
				}
				return nil
			})
		}
	case "a":
		a.crewEdit("add-dir", "")
	case "D":
		if inScope {
			cr.mode = "remove-dir"
		}
	case "c":
		a.crewEdit("maxworkers", strconv.Itoa(cr.run.MaxWorkers))
	}
}

// crewEdit opens the shared editor for a control room field.
func (a *App) crewEdit(mode, value string) {
	a.crew.mode = mode
	a.editor.Reset()
	a.editor.SetValue(value)
}

// crewCommit applies an editor's value.
func (a *App) crewCommit(mode, text string) {
	cr := &a.crew
	switch mode {
	case "launch":
		a.crewLaunch(text)
	case "model":
		if rec, ok := a.crewWorker(); ok && text != "" {
			t := parseCrewTarget(text)
			a.crewControlWorker(rec, "model", func(c *fleet.Control) { c.Target, c.Auto = t, false })
		}
	case "effort":
		if rec, ok := a.crewWorker(); ok && text != "" {
			a.crewControlWorker(rec, "effort", func(c *fleet.Control) { c.Effort = text })
		}
	case "primary":
		t := parseCrewTarget(text)
		a.crewUpdateRun("primary", map[string]string{"target": t.String()}, func(r *fleet.CrewRun) error {
			r.Primary = t
			return nil
		})
	case "fallbacks":
		var fb []fleet.Target
		for _, f := range strings.Fields(strings.ReplaceAll(text, ",", " ")) {
			fb = append(fb, parseCrewTarget(f))
		}
		a.crewUpdateRun("fallbacks", nil, func(r *fleet.CrewRun) error {
			r.Fallbacks = fb
			return nil
		})
	case "add-dir":
		d, err := fleet.ParseScopeDir(text)
		if err != nil {
			cr.errMsg = err.Error()
			return
		}
		d.Path = repoRootOf(expandHome(d.Path))
		if err := fleet.CheckScopeDir(d.Path, cr.run.Members); err != nil {
			cr.errMsg = err.Error()
			return
		}
		a.crewUpdateRun("add-dir", map[string]string{"repo": d.Path}, func(r *fleet.CrewRun) error {
			if _, ok := r.Dir(d.Path); ok {
				return errors.New(d.Path + " is already in the scope")
			}
			r.Scope = append(r.Scope, d)
			return nil
		})
	case "maxworkers":
		n, err := strconv.Atoi(text)
		if err != nil || n < 0 {
			cr.errMsg = "max workers is a number, 0 or more"
			return
		}
		a.crewUpdateRun("max-workers", nil, func(r *fleet.CrewRun) error {
			r.MaxWorkers = n
			return nil
		})
	case "move":
		to, ok := kanban.ParseList(text)
		if !ok {
			cr.errMsg = "no list " + text
			return
		}
		store := kanbanStoreOf(a)
		a.crewBulk("move", func(it kanban.Item) error {
			if it.ClaimedBy != "" {
				if _, err := store.Unclaim(it.ID, "released by the crew control room", a.sessionID, false); err != nil {
					return err
				}
			}
			_, err := store.Move(it.ID, to, "moved from the crew control room", a.sessionID)
			return err
		})
	case "labels":
		var add, drop []string
		for _, f := range strings.Fields(strings.ReplaceAll(text, ",", " ")) {
			switch {
			case strings.HasPrefix(f, "-"):
				drop = append(drop, f[1:])
			default:
				add = append(add, strings.TrimPrefix(f, "+"))
			}
		}
		store := kanbanStoreOf(a)
		a.crewBulk("labels", func(it kanban.Item) error {
			labels := slices.DeleteFunc(slices.Clone(it.Labels), func(l string) bool { return slices.Contains(drop, l) })
			for _, l := range add {
				if !slices.Contains(labels, l) {
					labels = append(labels, l)
				}
			}
			_, err := store.Route(it.ID, kanban.RoutePatch{Labels: &labels}, a.sessionID)
			return err
		})
	case "assign":
		who, err := kanban.CleanAssignee(text)
		if err != nil {
			cr.errMsg = err.Error()
			return
		}
		store := kanbanStoreOf(a)
		a.crewBulk("assign", func(it kanban.Item) error {
			_, err := store.Route(it.ID, kanban.RoutePatch{Assignee: &who}, a.sessionID)
			return err
		})
	}
}

// crewConfirm runs a confirmed action.
func (a *App) crewConfirm(mode string) tea.Cmd {
	cr := &a.crew
	reg, err := a.fleetRegistry()
	if err != nil {
		cr.errMsg = err.Error()
		return nil
	}
	switch mode {
	case "stop-worker":
		if rec, ok := a.crewWorker(); ok {
			fleet.RecordControl(cr.run.ID, cr.run.Crew, "stop", "", map[string]string{"worker": rec.ID})
			return a.stopFleetWorkers(&rec)
		}
	case "stop-run":
		id, crew := cr.run.ID, cr.run.Crew
		fleet.RecordControl(id, crew, "stop", "", nil)
		cr.status = id + " stopping: its workers finish or hand back their cards"
		return func() tea.Msg {
			_ = reg.StopRun(id, "human", 45e9)
			return controlRoomTickMsg{}
		}
	case "delete-cards":
		store := kanbanStoreOf(a)
		a.crewBulk("delete", func(it kanban.Item) error {
			_, err := store.Delete(it.ID)
			return err
		})
	case "remove-dir":
		i := cr.sel[crScope]
		if i >= len(cr.run.Scope) {
			return nil
		}
		path := cr.run.Scope[i].Path
		a.crewUpdateRun("remove-dir", map[string]string{"repo": path}, func(r *fleet.CrewRun) error {
			r.Scope = slices.DeleteFunc(r.Scope, func(d fleet.ScopeDir) bool { return d.Path == path })
			return nil
		})
	}
	return nil
}

// crewWorker is the selected worker.
func (a *App) crewWorker() (fleet.Record, bool) {
	cr := &a.crew
	if i := cr.sel[crWorkers]; i < len(cr.workers) {
		return cr.workers[i], true
	}
	return fleet.Record{}, false
}

// targets are the marked cards, or the selected one when none is marked.
func (cr *controlRoom) targets() []kanban.Item {
	var out []kanban.Item
	for _, it := range cr.cards {
		if cr.marks[it.ID] {
			out = append(out, it)
		}
	}
	if len(out) == 0 && cr.sel[crBoard] < len(cr.cards) {
		out = append(out, cr.cards[cr.sel[crBoard]])
	}
	return out
}

// crewBulk applies fn to the marked cards (or the selected one), records one
// crew.control fact per card, and clears the marks.
func (a *App) crewBulk(action string, fn func(kanban.Item) error) {
	cr := &a.crew
	items := cr.targets()
	if len(items) == 0 {
		return
	}
	n := 0
	var errs []error
	for _, it := range items {
		if err := fn(it); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", it.Short(), err))
			continue
		}
		fleet.RecordControl(cr.run.ID, cr.run.Crew, action, it.ID, nil)
		n++
	}
	cr.marks = nil
	cr.status = fmt.Sprintf("%s: %d of %d card(s)", action, n, len(items))
	if err := errors.Join(errs...); err != nil {
		cr.errMsg = err.Error()
	}
}

// crewPause pauses or resumes a worker.
func (a *App) crewPause(rec fleet.Record) {
	cr := &a.crew
	reg, err := a.fleetRegistry()
	if err != nil {
		cr.errMsg = err.Error()
		return
	}
	pause := !reg.Paused(rec.ID)
	if err := reg.SetPaused(rec.ID, pause); err != nil {
		cr.errMsg = err.Error()
		return
	}
	action := "resume"
	if pause {
		action = "pause"
	}
	fleet.RecordControl(cr.run.ID, cr.run.Crew, action, "", map[string]string{"worker": rec.ID})
	cr.status = rec.ID + " " + action + "d"
}

// crewControlWorker writes a control to one worker.
func (a *App) crewControlWorker(rec fleet.Record, action string, fn func(*fleet.Control)) {
	cr := &a.crew
	reg, err := a.fleetRegistry()
	if err != nil {
		cr.errMsg = err.Error()
		return
	}
	c, err := reg.SetControl(rec.ID, "human", fn)
	if err != nil {
		cr.errMsg = err.Error()
		return
	}
	data := map[string]string{"worker": rec.ID}
	if !c.Target.Zero() {
		data["target"] = c.Target.String()
	}
	fleet.RecordControl(cr.run.ID, cr.run.Crew, action, "", data)
	cr.status = fmt.Sprintf("%s: control %d written to %s; it applies before the next round", action, c.Seq, rec.ID)
}

// crewControlRun writes a control to every live worker of the run.
func (a *App) crewControlRun(action string, data map[string]string, fn func(*fleet.Control)) {
	cr := &a.crew
	reg, err := a.fleetRegistry()
	if err != nil {
		cr.errMsg = err.Error()
		return
	}
	ids, err := reg.ControlRun(cr.run.ID, "", "human", fn)
	fleet.RecordControl(cr.run.ID, cr.run.Crew, action, "", data)
	cr.status = fmt.Sprintf("%s: control written to %d worker(s)", action, len(ids))
	if err != nil {
		cr.errMsg = err.Error()
	}
}

// crewUpdateRun edits the run file; the supervisor and the workers read it
// again at their next step.
func (a *App) crewUpdateRun(action string, data map[string]string, fn func(*fleet.CrewRun) error) {
	cr := &a.crew
	reg, err := a.fleetRegistry()
	if err != nil {
		cr.errMsg = err.Error()
		return
	}
	run, err := reg.UpdateRun(cr.run.ID, "human", fn)
	if err != nil {
		cr.errMsg = err.Error()
		return
	}
	fleet.RecordControl(run.ID, run.Crew, action, "", data)
	cr.run = run
	cr.status = fmt.Sprintf("%s saved (edit %d); it applies at the next claim", action, run.Seq)
}

// crewSwitch moves to the next or previous crew run.
func (a *App) crewSwitch(next bool) {
	cr := &a.crew
	if len(cr.runs) < 2 {
		return
	}
	i := slices.IndexFunc(cr.runs, func(r fleet.CrewRun) bool { return r.ID == cr.runID })
	if next {
		i = (i + 1) % len(cr.runs)
	} else {
		i = (i - 1 + len(cr.runs)) % len(cr.runs)
	}
	cr.runID = cr.runs[i].ID
	cr.marks, cr.sel = nil, [crTabs]int{}
}

// crewCycleClass steps the faults filter through the classes seen.
func (a *App) crewCycleClass() {
	cr := &a.crew
	seen := []string{""}
	for _, f := range cr.faults {
		if !slices.Contains(seen, f.Class) {
			seen = append(seen, f.Class)
		}
	}
	if cr.class != "" && !slices.Contains(seen, cr.class) {
		seen = append(seen, cr.class)
	}
	i := slices.Index(seen, cr.class)
	cr.class = seen[(i+1)%len(seen)]
	cr.sel[crFaults] = 0
}

// crewLaunchDefault prefills the launch editor: the first crew, here.
func (a *App) crewLaunchDefault() string {
	crew := "belai:delivery"
	if cr := a.crew; cr.hasRun {
		crew = cr.run.Crew
	} else if crews := agentprofile.ListCrews(); len(crews) > 0 {
		crew = crews[0].Name
	}
	return crew + " " + repoRootOf(a.workdir)
}

// crewLaunch launches a crew run from "CREW DIR[:WEIGHT] …": the same run the
// CLI's `agent start -crew` writes. A crew already running takes the new
// directories into its scope.
func (a *App) crewLaunch(text string) {
	cr := &a.crew
	fields := strings.Fields(text)
	if len(fields) == 0 {
		cr.errMsg = "name a crew"
		return
	}
	c, err := agentprofile.LoadCrew(fields[0])
	if err != nil {
		cr.errMsg = err.Error()
		return
	}
	dirs := fields[1:]
	if len(dirs) == 0 {
		dirs = []string{repoRootOf(a.workdir)}
	}
	var scope []fleet.ScopeDir
	for _, d := range dirs {
		sd, err := fleet.ParseScopeDir(d)
		if err != nil {
			cr.errMsg = d + ": " + err.Error()
			return
		}
		sd.Path = repoRootOf(expandHome(sd.Path))
		if err := fleet.CheckScopeDir(sd.Path, c.Members); err != nil {
			cr.errMsg = err.Error()
			return
		}
		scope = append(scope, sd)
	}
	reg, err := a.fleetRegistry()
	if err != nil {
		cr.errMsg = err.Error()
		return
	}
	if live, ok := reg.LiveRun(c.Name); ok {
		var added []string
		run, err := reg.UpdateRun(live.ID, "human", func(r *fleet.CrewRun) error {
			for _, d := range scope {
				if _, ok := r.Dir(d.Path); !ok {
					r.Scope = append(r.Scope, d)
					added = append(added, d.Path)
				}
			}
			return nil
		})
		if err != nil {
			cr.errMsg = err.Error()
			return
		}
		cr.runID = run.ID
		if len(added) == 0 {
			cr.status = c.Name + " is already running over this scope"
			return
		}
		fleet.RecordControl(run.ID, run.Crew, "add-dir", "", nil)
		cr.status = fmt.Sprintf("%s is running; added %s to its scope", c.Name, strings.Join(added, ", "))
		return
	}
	exe, err := os.Executable()
	if err != nil {
		cr.errMsg = err.Error()
		return
	}
	start := a.crewStarter
	if start == nil {
		start = reg.StartSupervisor(exe)
	}
	run, err := reg.LaunchCrew(fleet.NewCrewRun(c, scope), start)
	if err != nil {
		cr.errMsg = err.Error()
		return
	}
	fleet.RecordControl(run.ID, run.Crew, "launch", "", nil)
	cr.runID, cr.tab = run.ID, crWorkers
	cr.status = fmt.Sprintf("launched %s over %d %s; its supervisor starts the workers", run.ID, len(run.Scope), pluralWord(len(run.Scope), "directory", "directories"))
}

func parseCrewTarget(s string) fleet.Target {
	if p, m, ok := strings.Cut(s, "/"); ok {
		return fleet.Target{Provider: p, Model: m}
	}
	return fleet.Target{Model: s}
}

func repoRootOf(dir string) string {
	if info, ok := gitinfo.Detect(dir); ok && info.Root != "" {
		return info.Root
	}
	return dir
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return h + p[1:]
		}
	}
	return p
}
