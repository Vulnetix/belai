package rc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"

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

func localWorkers() []sessionsync.RCWorker {
	reg, err := fleet.OpenRegistry(nil)
	if err != nil {
		return []sessionsync.RCWorker{}
	}
	live, err := reg.Live()
	if err != nil {
		return []sessionsync.RCWorker{}
	}
	out := []sessionsync.RCWorker{}
	for _, r := range live {
		if len(out) >= maxInvWorkers {
			break
		}
		out = append(out, sessionsync.RCWorker{
			ID: r.ID, Profile: r.Profile, Crew: r.Crew, State: string(r.State),
			Item: r.Item, Project: r.Project, Session: r.Session, Started: r.Started,
		})
	}
	return out
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
