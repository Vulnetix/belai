package rc

import (
	"slices"
	"strings"

	"github.com/vulnetix/belai/internal/schedule"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// Rotating schedules. A schedule whose profile is schedule.RotateProfile names
// no one worker: each firing starts the next worker profile in the next
// directory this host offers, so one schedule keeps every repository visited by
// every kind of worker in turn. It rotates the host's own worker profiles when
// it has any, and Belai's built-in workers otherwise, so a machine nobody has
// written an agent for still has agents at work. The turn is host-local
// (schedule.Record.Turn); what each firing started is kept beside it so the
// outcome can be read back from the fleet registry.

// rotationOrder puts the built-in workers that find their own work first; any
// other built-in follows in name order.
var rotationOrder = []string{
	"belai:vuln-scout", "belai:scout", "belai:planner", "belai:reviewer",
	"belai:builder", "belai:repro", "belai:patcher", "belai:verifier", "belai:designer",
}

// rotationProfiles is what a rotating schedule cycles through: the host's own
// worker profiles when it has any, otherwise its built-in ones.
func rotationProfiles(inv Inventory) []string {
	var own, builtin []string
	for _, p := range inv.Profiles {
		switch {
		case p.Name == schedule.RotateProfile:
		case p.Builtin || strings.HasPrefix(p.Name, "belai:"):
			builtin = append(builtin, p.Name)
		default:
			own = append(own, p.Name)
		}
	}
	if len(own) > 0 {
		slices.Sort(own)
		return own
	}
	rank := func(n string) int {
		if i := slices.Index(rotationOrder, n); i >= 0 {
			return i
		}
		return len(rotationOrder)
	}
	slices.SortStableFunc(builtin, func(a, b string) int {
		if d := rank(a) - rank(b); d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})
	return builtin
}

// rotationDirs is every directory a rotating schedule visits, in the order
// this host offers them.
func (d *Daemon) rotationDirs() []string {
	var out []string
	for _, dir := range d.o.Dirs {
		if cwd, ok := Allowed(d.o.Dirs, dir.Path); ok && !slices.Contains(out, cwd) {
			out = append(out, cwd)
		}
	}
	return out
}

// rotationPick is the profile and directory a rotating schedule's turn
// starts: every directory with the first profile, then every directory with
// the next, so each repository sees each kind of worker in turn.
func rotationPick(turn int, profiles, dirs []string) (string, string, bool) {
	if len(profiles) == 0 || len(dirs) == 0 || turn < 0 {
		return "", "", false
	}
	return profiles[(turn/len(dirs))%len(profiles)], dirs[turn%len(dirs)], true
}

// rotationEntry is what the inventory advertises for rotating schedules, so the
// website can offer one; only present when there is something to rotate.
func rotationEntry(inv Inventory) (sessionsync.RCProfile, bool) {
	if len(rotationProfiles(inv)) == 0 {
		return sessionsync.RCProfile{}, false
	}
	return sessionsync.RCProfile{
		Name: schedule.RotateProfile, Builtin: true, DisplayName: "Rotate agents",
		Lists: []string{}, Labels: []string{}, HandoffTo: []string{}, HandoffLabels: []string{},
		OnSuccess: sessionsync.RCRoute{Labels: []string{}, DropLabels: []string{}},
		OnFailure: sessionsync.RCRoute{Labels: []string{}, DropLabels: []string{}},
	}, true
}

// fireRotation starts a rotating schedule's next turn and records what it
// started, before it starts it, so the outcome is matched to the right worker.
func (d *Daemon) fireRotation(r schedule.Record) string {
	profile, cwd, ok := rotationPick(r.Turn, rotationProfiles(d.o.Inventory()), d.rotationDirs())
	if !ok {
		return schedule.StatusRefusedProfile
	}
	if err := d.o.Schedules.RecordTurn(r.ID, r.Turn+1, profile, cwd); err != nil {
		d.logf("schedules: %v", err)
	}
	if d.o.Busy(profile, cwd) {
		d.logf("schedule %s: rotation turn %d: %s is still working in %s, skipped", short(r.ID), r.Turn+1, profile, cwd)
		return schedule.StatusSkippedBusy
	}
	_, reason := d.startWorkersDrain(sessionsync.Dispatch{Kind: "worker", Cwd: cwd, Profile: profile}, true)
	if reason == "" {
		d.logf("schedule %s: rotation turn %d: started %s in %s", short(r.ID), r.Turn+1, profile, cwd)
		return schedule.StatusStarted
	}
	d.logf("schedule %s: rotation turn %d: %s in %s refused: %s", short(r.ID), r.Turn+1, profile, cwd, reason)
	return startStatus(reason)
}

// ranAs is the profile and directory whose worker a schedule's last firing
// started: its own, or for a rotating schedule what that turn picked.
func ranAs(r schedule.Record) (string, string) {
	if r.Profile == schedule.RotateProfile {
		return r.RanProfile, r.RanDir
	}
	return r.Profile, r.Dir
}
