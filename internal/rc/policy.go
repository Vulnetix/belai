package rc

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// A fleet worker whose model the provider refuses by policy (or whose quota
// only a person can raise) releases its card uncounted and stops
// (internal/fleet/policy.go). Nobody sits at an rc host to see that, so the
// daemon asks the owner through the website: one approve request with reason
// action_needed per refused model, its subject the fault code and the model
// ("provider_policy:openai:gpt-5"). The server folds a repeat of a pending
// request into it, and the daemon asks once per model while it runs.

// PolicyStop is a worker stopped by a config fault: the fault code and the
// target that was refused.
type PolicyStop struct {
	Code   string
	Target fleet.Target
}

// maxSubject is the server's limit on a request subject.
const maxSubject = 64

// policySubject is the request subject for a stop: the fault code, then the
// target in the server's subject alphabet (lowercase letters, digits and
// . _ : -), with "/" as ":" and anything else as "-", cut to maxSubject.
func policySubject(s PolicyStop) string {
	var b strings.Builder
	b.WriteString(s.Code)
	b.WriteByte(':')
	for _, r := range strings.ToLower(s.Target.String()) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == ':', r == '-':
			b.WriteRune(r)
		case r == '/':
			b.WriteByte(':')
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	if len(out) > maxSubject {
		out = out[:maxSubject]
	}
	return out
}

// askPolicyStops asks the owner about each refused model not asked about yet.
// A request the website cannot take (an older server, a login it no longer
// accepts) is not repeated either: Request has logged what to do instead.
func (d *Daemon) askPolicyStops(ctx context.Context) {
	if d.o.PolicyStops == nil {
		return
	}
	for _, s := range d.o.PolicyStops() {
		if s.Code == "" || s.Target.Zero() {
			continue
		}
		subject := policySubject(s)
		d.mu.Lock()
		asked := d.policyAsked[subject]
		d.mu.Unlock()
		if asked {
			continue
		}
		d.logf("a worker stopped: the provider refused %s (%s); asking the owner to change the model or the AI Firewall policy", s.Target, s.Code)
		err := d.Request(ctx, sessionsync.RequestApprove, sessionsync.ReasonActionNeeded, subject)
		if err != nil && !errors.Is(err, sessionsync.ErrNotFound) && !errors.Is(err, sessionsync.ErrUnauthorized) {
			// Try again on the next heartbeat.
			continue
		}
		d.mu.Lock()
		if d.policyAsked == nil {
			d.policyAsked = map[string]bool{}
		}
		d.policyAsked[subject] = true
		d.mu.Unlock()
	}
}

// localPolicyStops reads the fleet registry for workers that stopped on a
// config fault within RecentWorkers.
func localPolicyStops() []PolicyStop {
	reg, err := fleet.OpenRegistry(nil)
	if err != nil {
		return nil
	}
	all, err := reg.List()
	if err != nil {
		return nil
	}
	return policyStops(all, time.Now())
}

// policyStops picks the config-fault stops out of the records: a worker that
// stopped on its own (not failed) with a config fault as its last fault and its
// stop reason, recently.
func policyStops(all []fleet.Record, now time.Time) []PolicyStop {
	var out []PolicyStop
	for _, r := range all {
		if r.State != fleet.StateStopped || r.Stopped == 0 || now.Sub(time.UnixMilli(r.Stopped)) > RecentWorkers {
			continue
		}
		if !strings.HasPrefix(r.Reason, "stopped: "+r.LastFault) || (r.LastFault != fleet.FaultPolicy && r.LastFault != fleet.FaultQuota) {
			continue
		}
		out = append(out, PolicyStop{Code: r.LastFault, Target: fleet.Target{Provider: r.Provider, Model: r.Model}})
	}
	return out
}
