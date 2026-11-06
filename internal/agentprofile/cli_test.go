package agentprofile

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/clijobs"
)

func cliSrc(kanban string) string {
	return `{"name":"t-cli","description":"d","system_prompt":"sp","mode":"worker","tools":["Read"],"kanban":{"on_success":{"list":"done"},` + kanban + `}}`
}

func TestCLIBlockParsesAndValidatesAgainstTheTable(t *testing.T) {
	resetDir(t)
	good := cliSrc(`"cli":{"jobs":["sca","scout"]}`)
	p, err := ParseFile("w.json", []byte(good))
	if err != nil || p.Kanban.CLI == nil || len(p.Kanban.CLI.Jobs) != 2 {
		t.Fatalf("parse: %v %+v", err, p.Kanban)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("a read-only list is valid: %v", err)
	}
	for name, kb := range map[string]string{
		"unknown id":        `"cli":{"jobs":["rm"]}`,
		"empty":             `"cli":{"jobs":[]}`,
		"duplicate":         `"cli":{"jobs":["sca","sca"]}`,
		"mutating no write": `"cli":{"jobs":["fix"]}`,
		"write no mutating": `"cli":{"jobs":["sca"],"write":true}`,
		"write no worktree": `"cli":{"jobs":["fix"],"write":true}`,
		"argv key":          `"cli":{"jobs":["sca"],"argv":["x"]}`,
	} {
		p, err := ParseFile("w.json", []byte(cliSrc(kb)))
		if err == nil {
			err = p.Validate()
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCLIWriteProfileNeedsAWorktree(t *testing.T) {
	resetDir(t)
	src := strings.Replace(cliSrc(`"cli":{"jobs":["fix"],"write":true}`), `"write":true}}`, `"write":true}},"workspace":{"isolation":"worktree"}`, 1)
	p, err := ParseFile("w.json", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("a write profile in a worktree is valid: %v", err)
	}
}

// The two built-in runners list exactly the table's read-only and mutating ids,
// so a job added to the table is on a profile or the test says why not.
func TestBuiltinCLIRunnersListTheWholeTable(t *testing.T) {
	resetDir(t)
	ro, err := Load("belai:cli-runner")
	if err != nil {
		t.Fatal(err)
	}
	rw, err := Load("belai:cli-runner-write")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ro.Kanban.CLI.Jobs, ","); got != strings.Join(clijobs.ReadOnlyIDs(), ",") || ro.Kanban.CLI.Write {
		t.Fatalf("cli-runner jobs %v write %v", ro.Kanban.CLI.Jobs, ro.Kanban.CLI.Write)
	}
	if got := strings.Join(rw.Kanban.CLI.Jobs, ","); got != strings.Join(clijobs.MutatingIDs(), ",") || !rw.Kanban.CLI.Write {
		t.Fatalf("cli-runner-write jobs %v write %v", rw.Kanban.CLI.Jobs, rw.Kanban.CLI.Write)
	}
	for _, p := range []AgentProfile{ro, rw} {
		if p.Autonomy != AutonomySupervised || len(p.Tools) != 1 || p.Tools[0] != "Read" {
			t.Errorf("%s: autonomy %q tools %v", p.Name, p.Autonomy, p.Tools)
		}
	}
	if !slicesContains(ro.Kanban.CLI.Jobs, "scout") {
		t.Fatal("the read-only runner must list scout")
	}
}

func slicesContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
