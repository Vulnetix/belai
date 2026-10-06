package agentprofile

import (
	"strings"
	"testing"
	"time"
)

var workerProfiles = []string{"belai:scout", "belai:builder", "belai:reviewer", "belai:vuln-scout", "belai:patcher", "belai:verifier"}

func TestBuiltinWorkersLoad(t *testing.T) {
	for _, name := range workerProfiles {
		p, err := Load(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if p.Mode != ModeWorker || p.Kanban == nil {
			t.Fatalf("%s is not a worker", name)
		}
		if p.Writes() && p.IsolationMode() == IsolationNone {
			t.Fatalf("%s writes without isolation", name)
		}
	}
	for _, name := range []string{"belai:delivery", "belai:security"} {
		c, err := LoadCrew(name)
		if err != nil {
			t.Fatalf("crew %s: %v", name, err)
		}
		if c.Workers() != 4 {
			t.Fatalf("crew %s has %d workers", name, c.Workers())
		}
	}
}

func worker() AgentProfile {
	return AgentProfile{
		Name: "w", Description: "d", SystemPrompt: "sp", Mode: ModeWorker,
		Tools:  []string{"Read"},
		Kanban: &KanbanSpec{OnSuccess: Route{List: "review"}},
	}
}

func TestWorkerValidation(t *testing.T) {
	if err := worker().Validate(); err != nil {
		t.Fatalf("minimal worker: %v", err)
	}
	f := false
	for name, mutate := range map[string]func(*AgentProfile){
		"no kanban":          func(p *AgentProfile) { p.Kanban = nil },
		"no on_success":      func(p *AgentProfile) { p.Kanban.OnSuccess = Route{} },
		"claims in_progress": func(p *AgentProfile) { p.Kanban.Lists = []string{"in_progress"} },
		"bad route":          func(p *AgentProfile) { p.Kanban.OnFailure = Route{List: "nowhere"} },
		"short lease":        func(p *AgentProfile) { p.Kanban.Lease = "10s" },
		"fast poll":          func(p *AgentProfile) { p.Kanban.Poll = "1s" },
		"bad handoff":        func(p *AgentProfile) { p.Kanban.HandoffTo = []string{"has space"} },
		"bad label":          func(p *AgentProfile) { p.Kanban.HandoffLabels = []string{"Build"} },
		"guardrails off":     func(p *AgentProfile) { p.Guardrails = &f },
		"autonomous no cap":  func(p *AgentProfile) { p.Autonomy = AutonomyAutonomous },
		"writes shared-less": func(p *AgentProfile) { p.Tools = []string{"Write"} },
		"full surface":       func(p *AgentProfile) { p.Tools = nil },
		"publish shared": func(p *AgentProfile) {
			p.Workspace = &WorkspaceSpec{Isolation: IsolationShared, Publish: PublishDraftPR}
		},
		"bad isolation": func(p *AgentProfile) { p.Workspace = &WorkspaceSpec{Isolation: "docker"} },
		"base option": func(p *AgentProfile) {
			p.Workspace = &WorkspaceSpec{Isolation: IsolationWorktree, Base: "--upload-pack=x"}
		},
		"bad cron":         func(p *AgentProfile) { p.Schedule = "cron: 99 * * * *" },
		"kanban on single": func(p *AgentProfile) { p.Mode = ModeSingle },
		"big memory":       func(p *AgentProfile) { p.Memory = &MemorySpec{MaxBytes: MaxMemoryBytes + 1} },
	} {
		p := worker()
		k := *p.Kanban
		p.Kanban = &k
		mutate(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	p := worker()
	p.Tools = []string{"Read", "Write"}
	p.Workspace = &WorkspaceSpec{Isolation: IsolationWorktree}
	p.Autonomy = AutonomyAutonomous
	p.Budget = &BudgetSpec{MaxPassesPerItem: 4}
	p.Schedule = "cron: */15 * * * *"
	if err := p.Validate(); err != nil {
		t.Fatalf("full worker: %v", err)
	}
	if !KnownTool("mcp__github__create_issue") || !KnownTool("mcp__github__*") || KnownTool("mcp__x") || KnownTool("Nope") {
		t.Fatal("KnownTool")
	}
}

func TestParseMarkdown(t *testing.T) {
	src := "---\nname: helper\ndescription: Helps\ntools: Read, Grep\nmodel: gpt-x\ncolor: blue\n---\nYou help.\n\nTwo paragraphs.\n"
	p, err := ParseMarkdown([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "helper" || p.Mode != ModeSingle || p.Autonomy != AutonomySupervised || len(p.Tools) != 2 || !strings.Contains(p.SystemPrompt, "Two paragraphs") {
		t.Fatalf("%+v", p)
	}
	worker := "---\nname: wk\ndescription: d\nmode: worker\ntools: [Read]\nkanban:\n  labels: [build]\n  on_success: {list: review, labels: [needs-review]}\n---\nWork.\n"
	w, err := ParseMarkdown([]byte(worker))
	if err != nil {
		t.Fatal(err)
	}
	if w.Kanban == nil || w.Kanban.OnSuccess.List != "review" || w.Kanban.Labels[0] != "build" {
		t.Fatalf("%+v", w.Kanban)
	}
	for name, bad := range map[string]string{
		"no front-matter": "name: x\n",
		"unclosed":        "---\nname: x\n",
		"unknown key":     "---\nname: x\ndescription: d\nguardrail: false\n---\nbody\n",
		"prompt twice":    "---\nname: x\ndescription: d\nsystem_prompt: a\n---\nbody\n",
	} {
		if _, err := ParseMarkdown([]byte(bad)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestSecurityCrewRunsOncePerRepo(t *testing.T) {
	c, err := LoadCrew("belai:security")
	if err != nil {
		t.Fatal(err)
	}
	if !c.OnePerRepo {
		t.Fatal("belai:security must refuse a second run in the same repository")
	}
	if d, err := LoadCrew("belai:delivery"); err != nil || !d.OnePerRepo {
		t.Fatalf("delivery runs once per repository too (err %v)", err)
	}
}

func TestSecurityCrewProfilesCarryTheirHarnessDuties(t *testing.T) {
	scout, err := Load("belai:vuln-scout")
	if err != nil {
		t.Fatal(err)
	}
	if s := scout.Kanban.Security; s == nil || !s.Sweep {
		t.Fatalf("vuln-scout must sweep: %+v", s)
	}
	if scout.Kanban.Survey != nil {
		t.Fatal("the security scout has no daily survey: its only gates are the crew and the HEAD evidence")
	}
	patcher, err := Load("belai:patcher")
	if err != nil {
		t.Fatal(err)
	}
	if s := patcher.Kanban.Security; s == nil || !s.Reconcile {
		t.Fatalf("patcher must reconcile: %+v", s)
	}
}

// An hourly start lands a fraction of a second short of the interval, so the
// survey tolerates a start two minutes early, and never more than a tenth of
// the interval.
func TestSurveyGraceIsTwoMinutesCappedAtATenth(t *testing.T) {
	for every, want := range map[string]time.Duration{
		"1h":  2 * time.Minute,
		"24h": 2 * time.Minute,
		"":    2 * time.Minute, // the 24h default
		"30m": 2 * time.Minute, // refused by Validate, but the arithmetic holds
		"10m": time.Minute,
		"1m":  6 * time.Second,
		"bad": 2 * time.Minute, // unparsable falls back to the default interval
		"-5h": 2 * time.Minute,
	} {
		if got := (SurveySpec{Every: every}).SurveyGrace(); got != want {
			t.Errorf("every %q: grace = %s, want %s", every, got, want)
		}
	}
}

// handoff_repos files a handoff under another repository, so it needs somewhere
// to hand off to, and it cannot be combined with gates, which name a test suite
// of the worker's own repository.
func TestHandoffReposRules(t *testing.T) {
	p := worker()
	p.Kanban.HandoffRepos = true
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "handoff_repos needs handoff_to or handoff_labels") {
		t.Fatalf("no handoff target: %v", err)
	}
	p.Kanban.HandoffLabels = []string{"infra"}
	if err := p.Validate(); err != nil {
		t.Fatalf("labels and handoff_repos: %v", err)
	}
	p.Kanban.HandoffLabels = nil
	p.Kanban.HandoffTo = []string{"terraform-builder"}
	if err := p.Validate(); err != nil {
		t.Fatalf("assignee and handoff_repos: %v", err)
	}
	p.Kanban.Gates = &GatesSpec{Require: true}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "cannot be combined with kanban.gates") {
		t.Fatalf("gates and handoff_repos: %v", err)
	}
}

func TestHandoffReposRoundTripsAndIsOffByDefault(t *testing.T) {
	p := worker()
	p.Kanban.HandoffLabels = []string{"infra"}
	data, err := MarshalMarkdown(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "handoff_repos") {
		t.Errorf("an unset handoff_repos should not be written:\n%s", data)
	}
	p.Kanban.HandoffRepos = true
	data, err = MarshalMarkdown(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseMarkdown(data)
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	if !got.Kanban.HandoffRepos {
		t.Fatal("handoff_repos did not survive a markdown round trip")
	}
	if ProfileHashChanges := p.Behavioural(); !ProfileHashChanges.Kanban.HandoffRepos {
		t.Error("handoff_repos changes what a worker may do, so a running worker must pin it")
	}
}
