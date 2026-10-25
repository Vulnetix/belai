package rc

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/calltrace"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/turnlog"
)

// The request's ask reaches rc-session as -ask on or off, and only when the
// request set it.
func TestChildArgsAsk(t *testing.T) {
	on, off := true, false
	for _, c := range []struct {
		ask  *bool
		want string
	}{{nil, ""}, {&on, "on"}, {&off, "off"}} {
		args := childArgs(Child{Dispatch: "d", SessionID: "s", Ask: c.ask})
		i := slices.Index(args, "-ask")
		switch {
		case c.want == "" && i >= 0:
			t.Errorf("unset ask passed: %v", args)
		case c.want != "" && (i < 0 || args[i+1] != c.want):
			t.Errorf("ask %s: argv %v", c.want, args)
		}
	}
	// The trigger never rides on argv, where every user of the host can read it.
	args := childArgs(Child{Dispatch: "d", SessionID: "s", Trigger: `{"source":"pix_call","note":"secret plan"}`})
	if strings.Contains(strings.Join(args, " "), "secret plan") || slices.Contains(args, "-trigger") {
		t.Errorf("trigger on argv: %v", args)
	}
}

// A start takes the request's ask and its cleaned trigger; a pix-assistant
// start keeps the trigger but never the ask.
func TestStartCarriesAskAndTrigger(t *testing.T) {
	d, proj, children, _ := agentDaemon(t, nil)
	t.Cleanup(d.wg.Wait)
	off := false
	trig := &sessionsync.Trigger{Source: "pix_call", Note: "fix it\nnow"}
	if _, why := d.start(sessionsync.Dispatch{ID: "d", Kind: "start", Cwd: proj, Prompt: "hi", Ask: &off, Trigger: trig}); why != "" {
		t.Fatal(why)
	}
	c := (*children)[0]
	if c.Ask == nil || *c.Ask {
		t.Errorf("child ask = %v, want off", c.Ask)
	}
	if c.Trigger != `{"source":"pix_call","note":"fix it now"}` {
		t.Errorf("child trigger = %s", c.Trigger)
	}
	if _, why := d.start(sessionsync.Dispatch{ID: "d2", Kind: "start", Cwd: proj, Prompt: "hi", Mode: "pix-assistant", Profile: "vuln-scout",
		Ask: &off, Trigger: &sessionsync.Trigger{Source: "pix_assistant"}}); why != "" {
		t.Fatal(why)
	}
	c = (*children)[1]
	if c.Ask != nil || slices.Contains(childArgs(c), "-ask") {
		t.Errorf("pix-assistant child took the ask: %+v", c)
	}
	if c.Trigger != `{"source":"pix_assistant"}` {
		t.Errorf("pix-assistant trigger = %s", c.Trigger)
	}
	if _, why := d.start(sessionsync.Dispatch{ID: "d3", Kind: "start", Cwd: proj, Prompt: "hi", Trigger: &sessionsync.Trigger{Source: "email"}}); why != "" {
		t.Fatal(why)
	}
	if c := (*children)[2]; c.Trigger != "" || c.Ask != nil {
		t.Errorf("unknown trigger or unset ask passed on: %+v", c)
	}
}

// Worker and crew starts take the trigger, and ask: false as -ask-off; ask:
// true and no ask keep today's behaviour (no flag).
func TestStartWorkersCarriesTriggerAndAskOff(t *testing.T) {
	d, proj, _, workers := agentDaemon(t, nil)
	on, off := true, false
	trig := &sessionsync.Trigger{Source: "workflow", Via: "cron", WorkflowName: "Nightly"}
	for _, r := range []sessionsync.Dispatch{
		{Kind: "worker", Cwd: proj, Profile: "belai:builder", Ask: &off, Trigger: trig},
		{Kind: "crew", Cwd: proj, Crew: "crew1", Ask: &off, Trigger: trig},
		{Kind: "worker", Cwd: proj, Profile: "belai:builder", Ask: &on, Trigger: trig},
		{Kind: "crew", Cwd: proj, Crew: "crew1", Trigger: trig},
	} {
		if _, why := d.startWorkers(r); why != "" {
			t.Fatal(why)
		}
	}
	for i, w := range *workers {
		if w.Trigger != `{"source":"workflow","via":"cron","workflowName":"Nightly"}` {
			t.Errorf("%d: worker trigger = %s", i, w.Trigger)
		}
		wantOff := i < 2
		if w.AskOff != wantOff || slices.Contains(agentStartArgs(w), "-ask-off") != wantOff {
			t.Errorf("%d: ask off = %v, argv %v; want %v", i, w.AskOff, agentStartArgs(w), wantOff)
		}
	}
}

// The child's environment carries this start's trigger only, never one the
// daemon inherited.
func TestTriggerEnv(t *testing.T) {
	base := []string{"PATH=/bin", calltrace.EnvTrigger + `={"source":"console"}`, "HOME=/h"}
	got := TriggerEnv(base, "")
	if slices.ContainsFunc(got, func(e string) bool { return strings.HasPrefix(e, calltrace.EnvTrigger+"=") }) {
		t.Errorf("inherited trigger kept: %v", got)
	}
	if !slices.Equal(got, []string{"PATH=/bin", "HOME=/h"}) {
		t.Errorf("env = %v", got)
	}
	got = TriggerEnv(base, `{"source":"fleet"}`)
	n := 0
	for _, e := range got {
		if strings.HasPrefix(e, calltrace.EnvTrigger+"=") {
			n++
			if e != calltrace.EnvTrigger+`={"source":"fleet"}` {
				t.Errorf("trigger = %s", e)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d trigger entries in %v", n, got)
	}
}

func TestProvenanceFromEnvValue(t *testing.T) {
	line, enc := Provenance(`{"source":"console","note":"x\ny","ignored":1}`)
	if line != "Started from the Vulnetix console. Note: x y." || enc != `{"source":"console","note":"x y"}` {
		t.Errorf("line %q enc %s", line, enc)
	}
	if line, enc := Provenance(""); line != "" || enc != "" {
		t.Errorf("empty value gave %q %q", line, enc)
	}
	if line, enc := Provenance(`{"source":"nope"}`); line != "" || enc != "" {
		t.Errorf("unknown source gave %q %q", line, enc)
	}
}

// provRunner records each turn's provenance.
type provRunner struct {
	mu   sync.Mutex
	seen []string
}

func (r *provRunner) RunInputObserved(_ context.Context, _ []run.Turn, in agent.TurnInput, _ func(agent.Event)) (run.Result, error) {
	r.mu.Lock()
	r.seen = append(r.seen, in.Prompt+"|"+in.Provenance)
	r.mu.Unlock()
	return run.Result{Reply: "ok"}, nil
}

// The dispatched prompt's turn carries the trigger line; later web prompts do
// not.
func TestRunSessionProvenanceOnTheFirstTurnOnly(t *testing.T) {
	r := &provRunner{}
	m := &fakeMirror{prompts: make(chan sessionsync.RemotePrompt, 2)}
	m.prompts <- sessionsync.RemotePrompt{ID: "p1", Content: "second"}
	err := RunSession(context.Background(), SessionOptions{
		Agent: r, Log: turnlog.New(nil), Mirror: m, Dispatch: "d1", Prompt: "first",
		Idle: 150 * time.Millisecond, Provenance: "Started from the Vulnetix console.",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	want := []string{"first|Started from the Vulnetix console.", "second|"}
	if !slices.Equal(r.seen, want) {
		t.Fatalf("turns = %q, want %q", r.seen, want)
	}
}
