package rc

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/cron"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/schedule"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// Scheduled agents. A schedule is a worker profile, a cron expression and a
// directory this host offers (internal/schedule). The daemon looks at the
// stored schedules every ScheduleEvery and fires each one that is due the way
// a website worker request would: through startWorkers, so the directory is
// matched against the daemon's own list, the profile against its own catalogue,
// and `belai agent start` applies the trust check, the preflight and the worker
// cap. A schedule can start nothing a worker request could not.
//
// The website edits the definitions and never runs one. A definition the host
// cannot accept is disabled with the reason, never applied. A run missed while
// the daemon was down is skipped, a schedule fires at most once per cron tick,
// and its last run and next run are facts this host writes, in the host's local
// time.

// DefaultScheduleEvery is how often the scheduler looks at the stored
// schedules and syncs them with the website.
const DefaultScheduleEvery = 30 * time.Second

func (d *Daemon) now() time.Time {
	if d.o.Now != nil {
		return d.o.Now()
	}
	return time.Now()
}

// scheduler runs until ctx ends. Its first tick drops the runs the daemon
// missed while it was not running.
func (d *Daemon) scheduler(ctx context.Context) {
	defer d.wg.Done()
	d.rebaseSchedules()
	t := time.NewTicker(d.o.ScheduleEvery)
	defer t.Stop()
	for {
		d.scheduleTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// rebaseSchedules clears the next run of every schedule that came due while
// the daemon was not running, so it is skipped instead of fired late.
func (d *Daemon) rebaseSchedules() {
	if n, err := d.o.Schedules.Rebase(d.now()); err != nil {
		d.logf("schedules: %v", err)
	} else if n > 0 {
		d.logf("schedules: skipped %d run%s missed while belai rc was not running", n, plural(n, "", "s"))
	}
}

// scheduleTick fires what is due, then syncs with the website and checks what
// the sync brought. Firing comes first so a slow or unreachable website never
// delays a run.
func (d *Daemon) scheduleTick(ctx context.Context) {
	d.fireDue(ctx, d.now())
	if ctx.Err() != nil {
		return
	}
	remote := d.o.ScheduleRemote
	n, err := schedule.Pull(ctx, d.o.Schedules, remote, d.o.HostID)
	d.noteScheduleSync(ctx, "pull", err)
	if err == nil && n > 0 {
		d.logf("schedules: %d change%s from the website", n, plural(n, "", "s"))
	}
	d.fireDue(ctx, d.now())
	d.noteScheduleSync(ctx, "push", schedule.Push(ctx, d.o.Schedules, remote, d.o.HostID))
}

// noteScheduleSync logs a sync failure once and its recovery once, so a
// website that is unreachable does not fill the log.
func (d *Daemon) noteScheduleSync(ctx context.Context, what string, err error) {
	if ctx.Err() != nil {
		return
	}
	msg := ""
	if err != nil {
		msg = what + ": " + err.Error()
	}
	d.mu.Lock()
	if d.schedErr == nil {
		d.schedErr = map[string]string{}
	}
	prev := d.schedErr[what]
	d.schedErr[what] = msg
	d.mu.Unlock()
	switch {
	case msg != "" && msg != prev:
		d.logf("schedules: %s", msg)
	case msg == "" && prev != "":
		d.logf("schedules: %s works again", what)
	}
}

// fireDue checks every enabled schedule that is new, changed or due, and
// fires the ones that are due.
func (d *Daemon) fireDue(ctx context.Context, now time.Time) {
	st := d.o.Schedules
	recs, err := st.Live()
	if err != nil {
		d.logf("schedules: %v", err)
		return
	}
	for _, r := range recs {
		if ctx.Err() != nil {
			return
		}
		if !r.Enabled {
			continue
		}
		// A schedule that has a next run in the future needs no look: it was
		// checked when that run was set.
		if r.NextRunAt != 0 && now.UnixMilli() < r.NextRunAt {
			continue
		}
		sched, refusal := d.checkSchedule(r)
		if refusal != "" {
			d.logf("schedule %s for %s turned off: %s", short(r.ID), r.Profile, refusal)
			if err := st.Refuse(r.ID, refusal, now); err != nil {
				d.logf("schedules: %v", err)
			}
			d.auditSchedule(r, refusal)
			continue
		}
		next := sched.Next(now)
		if r.NextRunAt == 0 {
			// New, edited or just rebased: set the next run, fire nothing.
			if err := st.SetNext(r.ID, next); err != nil {
				d.logf("schedules: %v", err)
			}
			continue
		}
		// Due. The run is recorded before it is fired, so a crash between the
		// two skips a run and can never fire it twice.
		if err := st.RecordRun(r.ID, now, schedule.StatusStarted, next); err != nil {
			d.logf("schedules: %v", err)
			continue
		}
		status := d.fire(r)
		if status != schedule.StatusStarted {
			if err := st.SetStatus(r.ID, status); err != nil {
				d.logf("schedules: %v", err)
			}
		}
		d.auditSchedule(r, status)
	}
}

// checkSchedule decides whether this host can run the schedule at all: the
// cron must parse, the profile must be a worker profile in this host's
// catalogue and the directory one it offers. The refusal is a status.
func (d *Daemon) checkSchedule(r schedule.Record) (cron.Schedule, string) {
	sched, err := cron.Parse(r.Cron)
	if err != nil {
		return cron.Schedule{}, schedule.StatusRefusedCron
	}
	if _, ok := Allowed(d.o.Dirs, r.Dir); !ok {
		return cron.Schedule{}, schedule.StatusRefusedDir
	}
	if !schedule.ValidProfileName(r.Profile) ||
		!slices.ContainsFunc(d.o.Inventory().Profiles, func(p sessionsync.RCProfile) bool { return p.Name == r.Profile }) {
		return cron.Schedule{}, schedule.StatusRefusedProfile
	}
	return sched, ""
}

// fire starts the schedule's worker and returns the outcome.
func (d *Daemon) fire(r schedule.Record) string {
	cwd, ok := Allowed(d.o.Dirs, r.Dir)
	if !ok {
		return schedule.StatusRefusedDir
	}
	if d.o.Busy(r.Profile, cwd) {
		d.logf("schedule %s: %s is still working in %s, skipped", short(r.ID), r.Profile, cwd)
		return schedule.StatusSkippedBusy
	}
	_, reason := d.startWorkersDrain(sessionsync.Dispatch{Kind: "worker", Cwd: cwd, Profile: r.Profile}, true)
	if reason == "" {
		d.logf("schedule %s: started %s in %s", short(r.ID), r.Profile, cwd)
		return schedule.StatusStarted
	}
	d.logf("schedule %s: %s in %s refused: %s", short(r.ID), r.Profile, cwd, reason)
	return startStatus(reason)
}

// startStatus names a start refusal as a run status.
func startStatus(reason string) string {
	switch {
	case strings.Contains(reason, "worker cap"):
		return schedule.StatusRefusedCap
	case strings.Contains(reason, "turned off"):
		return schedule.StatusRefusedDisabled
	case strings.Contains(reason, "does not offer that directory"):
		return schedule.StatusRefusedDir
	case strings.Contains(reason, "no worker profile"):
		return schedule.StatusRefusedProfile
	}
	return schedule.StatusError
}

func (d *Daemon) auditSchedule(r schedule.Record, status string) {
	audit.Emit(audit.Fact{Kind: audit.HostSchedule, ActorKind: audit.ActorHarness, Outcome: status,
		Data: map[string]string{"schedule": r.ID, "profile": r.Profile, "status": status}})
}

// localBusy reports whether a worker of the profile is live in the directory's
// repository, read from the fleet registry.
func localBusy(profile, dir string) bool {
	reg, err := fleet.OpenRegistry(nil)
	if err != nil {
		return false
	}
	live, _ := reg.Live()
	for _, rec := range live {
		if rec.Profile == profile && sameRepo(rec.Repo, dir) {
			return true
		}
	}
	return false
}

// sameRepo reports whether dir is the repository or inside it.
func sameRepo(repo, dir string) bool {
	if repo == "" || dir == "" {
		return false
	}
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	repo, dir = resolve(repo), resolve(dir)
	return dir == repo || strings.HasPrefix(dir, repo+string(filepath.Separator))
}
