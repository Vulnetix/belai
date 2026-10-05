package rc

import (
	"slices"
	"testing"

	"github.com/vulnetix/belai/internal/sessionsync"
)

func agentDaemon(t *testing.T, models *sessionsync.RCModels, agents ...string) (*Daemon, string, *[]Child, *[]WorkerStart) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	proj := t.TempDir()
	real, _ := Normalize(proj)
	client, err := sessionsync.NewClient("http://127.0.0.1:1", func() (string, error) { return "ApiKey o:k", nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	var children []Child
	var workers []WorkerStart
	offered := make([]sessionsync.RCAgent, len(agents))
	for i, a := range agents {
		offered[i] = sessionsync.RCAgent{Name: a}
	}
	d, err := New(Options{
		Client: client, HostID: testHost, Dirs: []Dir{{Path: real, Name: "proj", Source: SourceArg}}, Max: 4,
		Inventory: func() Inventory {
			return Inventory{Agents: offered, Profiles: []sessionsync.RCProfile{{Name: "belai:builder"}}, Crews: []sessionsync.RCCrew{{Name: "crew1"}}}
		},
		Models: func() *sessionsync.RCModels { return models },
		Start: func(c Child) (int, func() error, error) {
			children = append(children, c)
			return 999999, func() error { return nil }, nil
		},
		StartWorkers: func(w WorkerStart) (string, error) { workers = append(workers, w); return "ok", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return d, proj, &children, &workers
}

// A web session may be engaged with an agent profile the host offers, in agent
// mode, and the profile reaches the child as a flag of its own.
func TestStartEngagesAnOfferedAgentProfile(t *testing.T) {
	d, proj, children, _ := agentDaemon(t, nil, "belai:debug", "reviewer")

	sid, why := d.start(sessionsync.Dispatch{ID: "d1", Kind: "start", Cwd: proj, Prompt: "hi", Mode: "agent", Profile: "reviewer"})
	if why != "" || sid == "" {
		t.Fatalf("start refused: %q", why)
	}
	if got := (*children)[0].Profile; got != "reviewer" {
		t.Fatalf("child profile = %q", got)
	}
	args := childArgs((*children)[0])
	if i := slices.Index(args, "-profile"); i < 0 || args[i+1] != "reviewer" {
		t.Fatalf("argv = %v, want -profile reviewer", args)
	}
}

func TestStartRefusesAProfileTheHostDoesNotOfferOrOutsideAgentMode(t *testing.T) {
	d, proj, children, _ := agentDaemon(t, nil, "reviewer")
	for _, c := range []struct{ profile, mode, want string }{
		{"stranger", "agent", "this host has no agent profile stranger"},
		{"reviewer", "plan", "an agent profile needs agent mode"},
		{"reviewer", "code", "an agent profile needs agent mode"},
		{"reviewer", "", "an agent profile needs agent mode"},
		{"--dangerous", "agent", "that is not an agent profile name"},
	} {
		if _, why := d.start(sessionsync.Dispatch{ID: "d", Kind: "start", Cwd: proj, Prompt: "hi", Mode: c.mode, Profile: c.profile}); why != c.want {
			t.Errorf("%s in %q: refusal %q, want %q", c.profile, c.mode, why, c.want)
		}
	}
	if len(*children) != 0 {
		t.Fatalf("a refused request started %d sessions", len(*children))
	}
}

// Code mode is a mode a web session can start in.
func TestStartAcceptsCodeMode(t *testing.T) {
	d, proj, children, _ := agentDaemon(t, nil)

	if _, why := d.start(sessionsync.Dispatch{ID: "d1", Kind: "start", Cwd: proj, Prompt: "hi", Mode: "code"}); why != "" {
		t.Fatalf("code mode refused: %s", why)
	}
	if got := (*children)[0].Mode; got != "code" {
		t.Fatalf("mode = %q", got)
	}
}

// A worker or crew started from the website runs on the model the request
// names, passed to `belai agent start` as flags, when this host holds the
// provider. Without a choice nothing is passed.
func TestWorkerStartCarriesTheRequestsModel(t *testing.T) {
	models := &sessionsync.RCModels{Providers: []sessionsync.RCProvider{{Name: "cloudflare-workers-ai"}}}
	d, proj, _, workers := agentDaemon(t, models)

	if _, why := d.startWorkers(sessionsync.Dispatch{Kind: "worker", Cwd: proj, Profile: "belai:builder", Provider: "cloudflare-workers-ai", Model: "@cf/zai-org/glm-4.7-flash"}); why != "" {
		t.Fatalf("refused: %s", why)
	}
	got := agentStartArgs((*workers)[0])
	if i := slices.Index(got, "-provider"); i < 0 || got[i+1] != "cloudflare-workers-ai" {
		t.Fatalf("argv = %v", got)
	}
	if i := slices.Index(got, "-model"); i < 0 || got[i+1] != "@cf/zai-org/glm-4.7-flash" {
		t.Fatalf("argv = %v", got)
	}
	if got[len(got)-1] != "belai:builder" {
		t.Fatalf("the profile is not last: %v", got)
	}

	if _, why := d.startWorkers(sessionsync.Dispatch{Kind: "crew", Cwd: proj, Crew: "crew1"}); why != "" {
		t.Fatalf("refused: %s", why)
	}
	plain := agentStartArgs((*workers)[1])
	if slices.Contains(plain, "-provider") || slices.Contains(plain, "-model") {
		t.Fatalf("a request with no model passed one: %v", plain)
	}
}

func TestWorkerStartRefusesAModelTheHostCannotRun(t *testing.T) {
	models := &sessionsync.RCModels{Providers: []sessionsync.RCProvider{{Name: "anthropic"}}}
	d, proj, _, workers := agentDaemon(t, models)

	for _, c := range []struct{ provider, model string }{{"openai", "gpt-5"}, {"anthropic", "--oops"}, {"-x", "m"}} {
		if _, why := d.startWorkers(sessionsync.Dispatch{Kind: "worker", Cwd: proj, Profile: "belai:builder", Provider: c.provider, Model: c.model}); why == "" {
			t.Errorf("%+v was accepted", c)
		}
	}
	if len(*workers) != 0 {
		t.Fatalf("a refused request started workers: %v", *workers)
	}
}

func TestSessionAgentsListsBuiltinsFirstAndNeverAFlagShapedName(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())

	got := SessionAgents()
	if len(got) == 0 || !got[0].Builtin {
		t.Fatalf("agents = %+v, want the built-ins first", got)
	}
	seen := map[string]bool{}
	for _, a := range got {
		if !ValidAgentName(a.Name) || seen[a.Name] {
			t.Errorf("bad or repeated agent %q", a.Name)
		}
		seen[a.Name] = true
	}
	if !seen["belai:debug"] {
		t.Errorf("belai:debug is not offered: %+v", got)
	}
}
