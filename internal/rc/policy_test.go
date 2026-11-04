package rc

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// The server's subject alphabet (vdb-site belai_host_attention.go).
var subjectShape = regexp.MustCompile(`^[a-z0-9._:-]{1,64}$`)

func TestPolicySubjectFitsTheServersAlphabet(t *testing.T) {
	for _, c := range []struct {
		stop PolicyStop
		want string
	}{
		{PolicyStop{Code: fleet.FaultPolicy, Target: fleet.Target{Provider: "openai", Model: "gpt-5"}}, "provider_policy:openai:gpt-5"},
		{PolicyStop{Code: fleet.FaultPolicy, Target: fleet.Target{Provider: "OpenRouter", Model: "anthropic/Claude Sonnet@4"}}, "provider_policy:openrouter:anthropic:claude-sonnet-4"},
		{PolicyStop{Code: fleet.FaultQuota, Target: fleet.Target{Provider: "builtin", Model: "pix-smart"}}, "provider_quota:builtin:pix-smart"},
		{PolicyStop{Code: fleet.FaultPolicy, Target: fleet.Target{Provider: "p", Model: strings.Repeat("m", 100)}}, "provider_policy:p:" + strings.Repeat("m", 64-len("provider_policy:p:"))},
	} {
		got := policySubject(c.stop)
		if got != c.want || !subjectShape.MatchString(got) {
			t.Errorf("%+v: %q, want %q", c.stop, got, c.want)
		}
	}
}

// Only a worker that stopped on a config fault, recently, is a policy stop.
func TestPolicyStopsPicksConfigFaultStops(t *testing.T) {
	now := time.Now()
	recent := now.Add(-time.Minute).UnixMilli()
	recs := []fleet.Record{
		{ID: "a", State: fleet.StateStopped, Stopped: recent, Reason: "stopped: provider_policy on openai/gpt-5", LastFault: fleet.FaultPolicy, Provider: "openai", Model: "gpt-5"},
		{ID: "b", State: fleet.StateStopped, Stopped: recent, Reason: "stopped: provider_quota on builtin/pix-smart", LastFault: fleet.FaultQuota, Provider: "builtin", Model: "pix-smart"},
		{ID: "c", State: fleet.StateStopped, Stopped: now.Add(-time.Hour).UnixMilli(), Reason: "stopped: provider_policy", LastFault: fleet.FaultPolicy, Provider: "openai", Model: "old"},
		{ID: "d", State: fleet.StateStopped, Stopped: recent, Reason: "done: nothing left to claim", LastFault: fleet.FaultPolicy, Provider: "openai", Model: "gpt-5"},
		{ID: "e", State: fleet.StateFailed, Stopped: recent, Reason: "stopped: provider_policy", LastFault: fleet.FaultPolicy, Provider: "openai", Model: "gpt-5"},
		{ID: "f", State: fleet.StateIdle, LastFault: fleet.FaultPolicy, Provider: "openai", Model: "gpt-5"},
	}
	got := policyStops(recs, now)
	if len(got) != 2 || got[0].Target.Model != "gpt-5" || got[1].Code != fleet.FaultQuota {
		t.Fatalf("stops %+v", got)
	}
}

// The heartbeat asks the owner once per refused model, as an approve request
// with reason action_needed, however many workers stopped on it.
func TestAPolicyStopAsksTheOwnerOncePerModel(t *testing.T) {
	site := &attentionSite{status: http.StatusCreated, body: `{"id":"req-2","kind":"approve","state":"pending","expiresAt":4102444800000}`}
	stops := []PolicyStop{
		{Code: fleet.FaultPolicy, Target: fleet.Target{Provider: "openai", Model: "gpt-5"}},
		{Code: fleet.FaultPolicy, Target: fleet.Target{Provider: "openai", Model: "gpt-5"}},
		{Code: fleet.FaultPolicy, Target: fleet.Target{Provider: "openai", Model: "gpt-5-mini"}},
	}
	d, log := attentionDaemon(t, site, Options{PolicyStops: func() []PolicyStop { return stops }})
	d.askPolicyStops(context.Background())
	d.askPolicyStops(context.Background())
	if site.count() != 2 {
		t.Fatalf("%d requests, want one per model", site.count())
	}
	var subjects []string
	for _, p := range site.posts {
		if p["kind"] != sessionsync.RequestApprove || p["reason"] != sessionsync.ReasonActionNeeded {
			t.Fatalf("posted %v", p)
		}
		subjects = append(subjects, p["subject"])
	}
	if !slices.Equal(subjects, []string{"provider_policy:openai:gpt-5", "provider_policy:openai:gpt-5-mini"}) {
		t.Fatalf("subjects %v", subjects)
	}
	if !strings.Contains(log.String(), "refused openai/gpt-5") {
		t.Fatalf("log %q", log.String())
	}
}

// A server that does not take host requests is asked once, not on every
// heartbeat.
func TestAPolicyStopIsNotRepeatedToAServerThatCannotTakeIt(t *testing.T) {
	site := &attentionSite{status: http.StatusNotFound, body: `{"error":"x"}`}
	d, _ := attentionDaemon(t, site, Options{PolicyStops: func() []PolicyStop {
		return []PolicyStop{{Code: fleet.FaultPolicy, Target: fleet.Target{Provider: "openai", Model: "gpt-5"}}}
	}})
	d.askPolicyStops(context.Background())
	d.askPolicyStops(context.Background())
	if site.count() != 1 {
		t.Fatalf("%d requests", site.count())
	}
}

// A crew request's member models reach `belai agent start` as -member-model
// words in profile order, each checked like the request's own model.
func TestCrewStartCarriesMemberModels(t *testing.T) {
	models := &sessionsync.RCModels{Providers: []sessionsync.RCProvider{{Name: "builtin"}, {Name: "anthropic"}}}
	d, proj, _, workers := agentDaemon(t, models)
	d.o.Inventory = func() Inventory {
		return Inventory{Crews: []sessionsync.RCCrew{{Name: "crew1", Members: []sessionsync.RCMember{{Profile: "belai:builder"}, {Profile: "belai:reviewer"}}}}}
	}
	mm := map[string]string{"belai:reviewer": "builtin/pix-fast", "belai:builder": "anthropic/claude-sonnet-5-5"}
	if _, why := d.startWorkers(sessionsync.Dispatch{Kind: "crew", Cwd: proj, Crew: "crew1", MemberModels: mm}); why != "" {
		t.Fatalf("refused: %s", why)
	}
	got := strings.Join(agentStartArgs((*workers)[0]), " ")
	if !strings.HasSuffix(got, "-crew crew1 -member-model belai:builder=anthropic/claude-sonnet-5-5 -member-model belai:reviewer=builtin/pix-fast") {
		t.Fatalf("argv %q", got)
	}
	for _, bad := range []map[string]string{
		{"belai:scout": "builtin/pix-fast"},   // not a member
		{"-x": "builtin/pix-fast"},            // reads as a flag
		{"belai:builder": "openai/gpt-5"},     // a provider the host cannot run
		{"belai:builder": "pix-fast"},         // no provider
		{"belai:builder": "builtin/--oops"},   // a flag-shaped model
		{"belai:builder": "builtin/pix fast"}, // a space
	} {
		if _, why := d.startWorkers(sessionsync.Dispatch{Kind: "crew", Cwd: proj, Crew: "crew1", MemberModels: bad}); why == "" {
			t.Errorf("%v accepted", bad)
		}
	}
	if len(*workers) != 1 {
		t.Fatalf("a refused request started workers: %d", len(*workers))
	}
	// The argv builder drops anything that slipped past the checks.
	if args := memberModelArgs(map[string]string{"-x": "p/m", "ok": "-p/m", "fine": "p/m"}); !slices.Equal(args, []string{"-member-model", "fine=p/m"}) {
		t.Fatalf("args %v", args)
	}
}
