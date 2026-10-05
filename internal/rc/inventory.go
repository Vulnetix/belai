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
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/libstore"
	"github.com/vulnetix/belai/internal/profiles"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// Inventory is what the daemon tells the website about this host's fleet:
// the worker profiles and crews it can start, its worker cap, and the
// workers running now. Harness facts only; no prompt or profile text.
type Inventory struct {
	MaxWorkers int
	Profiles   []sessionsync.RCProfile
	Crews      []sessionsync.RCCrew
	// Agents are the agent profiles a web session may be started with.
	Agents  []sessionsync.RCAgent
	Workers []sessionsync.RCWorker
	// Items are the library items this host holds, for the kinds whose sync
	// switch is on: kind, name and hash, never a document.
	Items []sessionsync.RCItem
	// Models is what a web-started session can run on (models.go).
	Models *sessionsync.RCModels
	// Knowledge is the catalogue of this host's knowledge indexes: document
	// facts only (knowledge.go).
	Knowledge []sessionsync.RCKnowledge
	// Prefs are the offered directories' project preferences, by path, read
	// only with --web-project-settings (prefs.go).
	Prefs map[string]DirPrefs
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
		A []sessionsync.RCAgent
		I []sessionsync.RCItem
		D *sessionsync.RCModels
		K []sessionsync.RCKnowledge
		F map[string]DirPrefs
	}{i.MaxWorkers, i.Profiles, i.Crews, i.Agents, i.Items, i.Models, i.Knowledge, i.Prefs})
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
		rc := sessionsync.RCCrew{ID: c.ID, Name: c.Name, Description: c.Description, Builtin: c.Builtin, Members: []sessionsync.RCMember{}}
		for _, m := range c.Members {
			rc.Members = append(rc.Members, sessionsync.RCMember{Profile: m.Profile, Replicas: m.Count()})
		}
		inv.Crews = append(inv.Crews, rc)
	}
	inv.Agents = SessionAgents()
	inv.Items = localInventoryItems()
	inv.Models = LocalModels()
	inv.Workers = localWorkers()
	return inv
}

// localInventoryItems lists the library items this host holds for the kinds whose
// sync switch is on, at most sessionsync.MaxRCItems of them.
func localInventoryItems() []sessionsync.RCItem {
	s, err := config.LoadGlobal()
	if err != nil {
		return nil
	}
	var kinds []libitem.Kind
	for _, k := range libstore.Kinds() {
		if s.SyncItemEnabled(string(k)) {
			kinds = append(kinds, k)
		}
	}
	var out []sessionsync.RCItem
	for _, it := range localLibraryItems(kinds) {
		if len(out) >= sessionsync.MaxRCItems {
			break
		}
		out = append(out, sessionsync.RCItem{Kind: it.kind, Name: it.name, SHA256: hashOf(it.data)})
	}
	return out
}

// maxInvAgents bounds the agent profiles one host advertises.
const maxInvAgents = 64

// SessionAgents lists the agent profiles a web session can be started with:
// the flat profiles the TUI's picker offers (built-ins first, then the user's)
// and the single-mode agent definitions it does not already shadow. A worker,
// loop, monitor or scheduled definition runs on its own and is not a choice for
// a session someone talks to. Names only; the host checks the name again when
// the request arrives.
func SessionAgents() []sessionsync.RCAgent {
	look := map[string]string{}
	var defs []agentprofile.AgentProfile
	if ps, err := agentprofile.List(); err == nil {
		for _, p := range ps {
			look[p.Name] = p.DisplayName
			if p.Mode == agentprofile.ModeSingle || p.Mode == "" {
				defs = append(defs, p)
			}
		}
	}
	var out []sessionsync.RCAgent
	seen := map[string]bool{}
	add := func(name string, builtin bool) {
		if name == "" || seen[name] || len(out) >= maxInvAgents || !ValidAgentName(name) {
			return
		}
		seen[name] = true
		out = append(out, sessionsync.RCAgent{Name: name, Builtin: builtin, DisplayName: look[name]})
	}
	if flat, err := profiles.List(); err == nil {
		sort.Slice(flat, func(a, b int) bool {
			if flat[a].Builtin != flat[b].Builtin {
				return flat[a].Builtin
			}
			return flat[a].Name < flat[b].Name
		})
		for _, p := range flat {
			add(p.Name, p.Builtin)
		}
	}
	for _, p := range defs {
		add(p.Name, p.Builtin)
	}
	return out
}

// ValidAgentName reports whether name can be an agent profile name in a start
// request: it starts with a letter or digit, so it can never read as a flag.
func ValidAgentName(name string) bool { return workerName.MatchString(name) }

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
		ID: p.ID, DisplayName: p.DisplayName, Palette: slices.Clone(p.Palette), AvatarID: p.AvatarID,
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
