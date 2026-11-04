package fleet

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// Config faults and per-member models.
//
// A worker whose item came back with a config fault (Fault.Config: the AI
// Firewall's policy refuses the model or provider, or a quota only a person
// can raise ran out) releases the card uncounted and stops, because every
// other card would fail the same way. A crew worker also records the stop on
// its run (CrewRun.PolicyStops) so the supervisor does not start the member
// again on the same model a few seconds later: the member stays down while its
// start target is the refused one, until a person edits the run (the control
// room, the website, a pool or model change) or the run is launched again.
// belai rc raises one host attention request per refused model
// (internal/rc/policy.go).

// PolicyStop is a member held by a config fault: the target that was refused,
// the fault code and when.
type PolicyStop struct {
	Target Target `json:"target"`
	Code   string `json:"code"`
	At     int64  `json:"at"`
}

// personEdit reports whether an UpdateRun author is a person: the control
// room in the TUI ("human") or the website ("web").
func personEdit(by string) bool { return by == "human" || by == "web" }

// PoolFor is the pool a member's workers cycle: the member's own target
// (MemberModels) first, then the run's pool without it. A member with no
// target of its own has the run's pool.
func (r CrewRun) PoolFor(profile string) []Target {
	t, ok := r.MemberModels[profile]
	if !ok || t.Zero() {
		return r.Pool
	}
	out := []Target{t}
	for _, p := range r.Pool {
		if p != t {
			out = append(out, p)
		}
	}
	return out
}

// StartTarget is where the supervisor starts a member's worker: the first of
// PoolFor, zero for the profile's own model.
func (r CrewRun) StartTarget(profile string) Target {
	if pool := r.PoolFor(profile); len(pool) > 0 {
		return pool[0]
	}
	return Target{}
}

// PolicyHeld reports whether the supervisor must not start a worker of the
// member: one of its workers stopped on a config fault, and the member would
// start on the same target again. A member that starts on the profile's own
// model (no pool, no member model) stays held, since nothing about its model
// has changed.
func (r CrewRun) PolicyHeld(profile string) bool {
	stop, ok := r.PolicyStops[profile]
	if !ok {
		return false
	}
	start := r.StartTarget(profile)
	return start.Zero() || start == stop.Target
}

// takePolicyStop returns the reason the loop stops on, when the last item came
// back with a config fault, and records the stop on the crew run.
func (w *Worker) takePolicyStop() string {
	w.mu.Lock()
	why := w.policyStop
	w.policyStop = ""
	w.mu.Unlock()
	if why == "" {
		return ""
	}
	if w.Record.Run != "" && w.Registry != nil {
		stop := PolicyStop{Target: w.currentTarget(), Code: w.Record.LastFault, At: w.clock().UnixMilli()}
		if _, err := w.Registry.UpdateRun(w.Record.Run, "worker", func(cr *CrewRun) error {
			if cr.PolicyStops == nil {
				cr.PolicyStops = map[string]PolicyStop{}
			}
			cr.PolicyStops[w.Profile.Name] = stop
			return nil
		}); err != nil {
			w.logf("record the policy stop on the crew run: %v", err)
		}
	}
	w.logf("%s; a person must change the model or the organisation's AI Firewall policy", why)
	return why
}

// heldMembers splits a run's members into those the supervisor may start and
// the targets of those a policy stop holds.
func heldMembers(run CrewRun) (CrewRun, []string) {
	var held []string
	keep := run.Members[:0:0]
	for _, m := range run.Members {
		if run.PolicyHeld(m.Profile) {
			held = append(held, m.Profile+" ("+run.PolicyStops[m.Profile].Target.String()+")")
			continue
		}
		keep = append(keep, m)
	}
	run.Members = keep
	return run, held
}

// MaxTargetPart bounds a provider or a model name in a target.
const MaxTargetPart = 128

// CheckTarget checks a provider/model target given on a command line or in a
// request: a model, and words that cannot read as flags or carry control or
// space characters. Whether this host can run it is the worker's to decide,
// when it starts.
func CheckTarget(t Target) error {
	if t.Model == "" {
		return errors.New("a target needs a model")
	}
	for _, v := range []string{t.Provider, t.Model} {
		switch {
		case len(v) > MaxTargetPart:
			return fmt.Errorf("a provider or model name runs at most %d bytes", MaxTargetPart)
		case strings.HasPrefix(v, "-"):
			return fmt.Errorf("%q cannot start with a dash", v)
		case strings.ContainsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == '=' }):
			return fmt.Errorf("%q holds a space, a control character or '='", v)
		}
	}
	return nil
}

// ParseMemberModel reads one `-member-model PROFILE=PROVIDER/MODEL` and checks
// it against the crew's members.
func ParseMemberModel(s string, members []string) (string, Target, error) {
	profile, target, ok := strings.Cut(s, "=")
	profile = strings.TrimSpace(profile)
	if !ok || profile == "" || strings.TrimSpace(target) == "" {
		return "", Target{}, fmt.Errorf("-member-model %q: want PROFILE=PROVIDER/MODEL", s)
	}
	if !slices.Contains(members, profile) {
		return "", Target{}, fmt.Errorf("-member-model %q: %s is not a member of the crew", s, profile)
	}
	prov, model, ok := strings.Cut(strings.TrimSpace(target), "/")
	if !ok || prov == "" {
		return "", Target{}, fmt.Errorf("-member-model %q: want PROVIDER/MODEL", s)
	}
	t := Target{Provider: prov, Model: model}
	if err := CheckTarget(t); err != nil {
		return "", Target{}, fmt.Errorf("-member-model %q: %w", s, err)
	}
	return profile, t, nil
}

// onTarget is " on provider/model" for the target the next turn runs on, or
// nothing when the worker cannot tell.
func (w *Worker) onTarget() string {
	if t := w.currentTarget(); !t.Zero() {
		return " on " + t.String()
	}
	return ""
}
