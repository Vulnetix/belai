package rc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// Inventory is what the daemon tells the website about this host's fleet:
// the worker profiles and crews it can start, its worker cap, and the
// workers running now. Harness facts only; no prompt or profile text.
type Inventory struct {
	MaxWorkers int
	Profiles   []sessionsync.RCProfile
	Crews      []sessionsync.RCCrew
	Workers    []sessionsync.RCWorker
}

// Caps on what one host reports; the server applies the same caps.
const (
	maxInvProfiles = 64
	maxInvCrews    = 64
	maxInvWorkers  = 64
)

// catalogueHash identifies the profiles, crews and cap, so the daemon
// re-advertises only when they change.
func (i Inventory) catalogueHash() string {
	b, _ := json.Marshal(struct {
		M int
		P []sessionsync.RCProfile
		C []sessionsync.RCCrew
	}{i.MaxWorkers, i.Profiles, i.Crews})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// LocalInventory reads this machine's worker profiles, crews and fleet
// registry. A part it cannot read is left empty rather than failing.
func LocalInventory() Inventory {
	var inv Inventory
	if s, err := config.LoadGlobal(); err == nil {
		inv.MaxWorkers = s.MaxWorkers()
	} else {
		inv.MaxWorkers = config.DefaultMaxWorkers
	}
	if ps, err := agentprofile.List(); err == nil {
		for _, p := range ps {
			if p.Mode != agentprofile.ModeWorker || p.Kanban == nil || len(inv.Profiles) >= maxInvProfiles {
				continue
			}
			inv.Profiles = append(inv.Profiles, profileSummary(p))
		}
	}
	sort.Slice(inv.Profiles, func(a, b int) bool { return inv.Profiles[a].Name < inv.Profiles[b].Name })
	for _, c := range agentprofile.ListCrews() {
		if len(inv.Crews) >= maxInvCrews {
			break
		}
		rc := sessionsync.RCCrew{Name: c.Name, Builtin: c.Builtin, Members: []sessionsync.RCMember{}}
		for _, m := range c.Members {
			rc.Members = append(rc.Members, sessionsync.RCMember{Profile: m.Profile, Replicas: m.Count()})
		}
		inv.Crews = append(inv.Crews, rc)
	}
	inv.Workers = localWorkers()
	return inv
}

// RecentWorkers is how long a stopped or failed worker stays on the
// website after it ends, so its exit reason and last log lines can be read.
const RecentWorkers = 15 * time.Minute

func localWorkers() []sessionsync.RCWorker {
	reg, err := fleet.OpenRegistry(nil)
	if err != nil {
		return []sessionsync.RCWorker{}
	}
	all, err := reg.List()
	if err != nil {
		return []sessionsync.RCWorker{}
	}
	return reportWorkers(all, reg.LogDir(), time.Now())
}

// reportWorkers picks and shapes the workers a heartbeat carries: every live
// one, then those that ended within RecentWorkers, newest first, up to
// maxInvWorkers. Each gets the tail of its own log (read from logDir by id,
// never from a path in the record) within RCWorkerLogBudget overall.
func reportWorkers(all []fleet.Record, logDir string, now time.Time) []sessionsync.RCWorker {
	var live, ended []fleet.Record
	for _, r := range all {
		switch {
		case r.State.Live():
			live = append(live, r)
		case r.Stopped > 0 && now.Sub(time.UnixMilli(r.Stopped)) <= RecentWorkers:
			ended = append(ended, r)
		}
	}
	sort.SliceStable(ended, func(i, j int) bool { return ended[i].Stopped > ended[j].Stopped })
	out := []sessionsync.RCWorker{}
	budget := sessionsync.RCWorkerLogBudget
	for _, r := range append(live, ended...) {
		if len(out) >= maxInvWorkers {
			break
		}
		w := sessionsync.RCWorker{
			ID: r.ID, Profile: r.Profile, Crew: r.Crew, State: string(r.State),
			Item: r.Item, Project: r.Project, Session: r.Session, Started: r.Started,
			Beat: r.Beat, Stopped: r.Stopped, Reason: sessionsync.CleanLogLine(r.Reason),
			Done: r.Done, Failed: r.Failed, Branch: r.Branch,
		}
		if fleet.ValidID(r.ID) {
			for _, l := range logTail(filepath.Join(logDir, r.ID+".log"), sessionsync.RCWorkerLogLines) {
				if budget -= len(l); budget < 0 {
					break
				}
				w.Log = append(w.Log, l)
			}
		}
		out = append(out, w)
	}
	return out
}

// logTailRead bounds how much of a log file is read for its tail.
const logTailRead = 32 << 10

// logTail returns the last n non-empty lines of the file at path, cleaned
// with sessionsync.CleanLogLine. A missing or unreadable file has none.
func logTail(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > logTailRead {
		if _, err := f.Seek(fi.Size()-logTailRead, io.SeekStart); err != nil {
			return nil
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, logTailRead))
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if l = sessionsync.CleanLogLine(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func profileSummary(p agentprofile.AgentProfile) sessionsync.RCProfile {
	k := p.Kanban
	lists := slices.Clone(k.Lists)
	if len(lists) == 0 {
		lists = []string{"backlog"}
	}
	out := sessionsync.RCProfile{
		Name: p.Name, Builtin: p.Builtin, Lists: lists, Labels: nonNil(k.Labels), AssignedOnly: k.AssignedOnly,
		OnSuccess: route(k.OnSuccess), OnFailure: route(k.OnFailure),
		HandoffTo: nonNil(k.HandoffTo), HandoffLabels: nonNil(k.HandoffLabels),
		MaxAttempts: k.MaxAttempts, Lease: p.LeaseDuration().String(),
	}
	if b := p.Budget; b != nil {
		out.MaxWall, out.MaxPasses = b.MaxWallPerItem, b.MaxPassesPerItem
	}
	return out
}

func route(r agentprofile.Route) sessionsync.RCRoute {
	return sessionsync.RCRoute{List: r.List, Labels: nonNil(r.Labels), DropLabels: nonNil(r.DropLabels)}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return slices.Clone(s)
}
