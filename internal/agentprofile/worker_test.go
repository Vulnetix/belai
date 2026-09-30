package agentprofile

import (
	"strings"
	"testing"
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
	if d, err := LoadCrew("belai:delivery"); err != nil || d.OnePerRepo {
		t.Fatalf("delivery must stay unrestricted (err %v)", err)
	}
}
