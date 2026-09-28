package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/kanban"
)

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// assignPlan is the routing `kanban assign` applies, worked out before any
// write so a bad profile or host changes nothing.
type assignPlan struct {
	assignee *string
	labels   []string
	list     kanban.List
	pin      *string
}

// planAssign works out the routing for a worker profile, or for a crew (its
// first member is the entry profile; a crew is never an assignee, so every
// member can claim what it hands on).
func planAssign(it kanban.Item, profile, crew, host, thisHost string) (assignPlan, error) {
	var plan assignPlan
	entry := profile
	if crew != "" {
		c, err := agentprofile.LoadCrew(crew)
		if err != nil {
			return plan, err
		}
		if len(c.Members) == 0 {
			return plan, fmt.Errorf("crew %s has no members", crew)
		}
		entry = c.Members[0].Profile
	}
	p, err := agentprofile.Load(entry)
	if err != nil {
		return plan, err
	}
	if p.Mode != agentprofile.ModeWorker || p.Kanban == nil {
		return plan, fmt.Errorf("%s is not a worker profile", entry)
	}
	if crew == "" {
		name := p.Name
		plan.assignee = &name
	}
	plan.labels = kanban.NormLabels(append(slices.Clone(it.Labels), p.Kanban.Labels...))
	lists := p.Kanban.Lists
	if len(lists) == 0 {
		lists = []string{string(kanban.Backlog)}
	}
	plan.list = it.List
	if !slices.Contains(lists, string(it.List)) {
		l, ok := kanban.ParseList(lists[0])
		if !ok {
			return plan, fmt.Errorf("%s claims from an unknown list %q", entry, lists[0])
		}
		plan.list = l
	}
	switch h := strings.ToLower(strings.TrimSpace(host)); h {
	case "":
	case "none":
		empty := ""
		plan.pin = &empty
	case "this", ".":
		if thisHost == "" {
			return plan, errors.New("this machine has no sync host id yet; log in with the Vulnetix CLI and start a session first")
		}
		plan.pin = &thisHost
	default:
		plan.pin = &h
	}
	return plan, nil
}

func (k *kanbanCLI) assign(ref, profile, crew, host string, start bool, stdout io.Writer) (int, error) {
	it, err := k.store.Get(ref)
	if err != nil {
		return 1, err
	}
	if it.ClaimedBy != "" {
		return 1, fmt.Errorf("%s is claimed by %s; release it first", it.Short(), it.ClaimedBy)
	}
	plan, err := planAssign(it, profile, crew, host, headless.HostID())
	if err != nil {
		return 1, err
	}
	if plan.list != it.List {
		if it, err = k.store.Move(it.ID, plan.list, "moved for assignment", k.prov.SessionID); err != nil {
			return 1, err
		}
	}
	if it, err = k.store.Route(it.ID, kanban.RoutePatch{Labels: &plan.labels, Assignee: plan.assignee, PinHost: plan.pin}, k.prov.SessionID); err != nil {
		return 1, err
	}
	who := crew
	if plan.assignee != nil {
		who = "@" + *plan.assignee
	}
	where := "any host"
	if it.PinHost != "" {
		where = "host " + it.PinHost
	}
	fmt.Fprintf(stdout, "%s → %s in %s, %s (labels %s)\n", it.Short(), who, it.List, where, strings.Join(it.Labels, ","))
	if !start {
		return 0, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return 1, err
	}
	args := []string{"agent", "start", profile}
	if crew != "" {
		args = []string{"agent", "start", "-crew", crew}
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = k.wd, stdout, stdout
	if err := cmd.Run(); err != nil {
		return 1, fmt.Errorf("the item is assigned, but the start failed: %w", err)
	}
	return 0, nil
}
