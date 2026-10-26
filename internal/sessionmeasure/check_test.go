package sessionmeasure

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/testrun"
)

func TestBashExit(t *testing.T) {
	cases := []struct {
		name    string
		content string
		exit    int
		ok      bool
	}{
		{"clean exit", "ok  \tgithub.com/x/y\t0.4s\n", 0, true},
		{"failure", "FAIL\nexit status 1", 1, true},
		{"other code", "boom\nexit status 127", 127, true},
		{"sandbox note after the status", "boom\nexit status 2\nsandbox: network denied", 2, true},
		{"an exit line in the output is read", "--- FAIL: TestX\nexit status 1\nPASS on retry", 1, true},
		{"timeout", "partial output\n… command timed out after 30s", -1, true},
		{"timeout with no output", "… command timed out after 30s", -1, true},
		{"withheld", "tool result withheld: unsafe", 0, false},
		{"empty output", "", 0, true},
		{"the words in a sentence", "the process printed exit status 3 once", 0, true},
	}
	for _, c := range cases {
		exit, ok := BashExit(c.content)
		if exit != c.exit || ok != c.ok {
			t.Errorf("%s: BashExit = %d, %v; want %d, %v", c.name, exit, ok, c.exit, c.ok)
		}
	}
}

func TestFromBash(t *testing.T) {
	cases := []struct {
		name    string
		command string
		content string
		dur     int64
		want    Check
		ok      bool
	}{
		{"passing test", "go test ./...", "ok\n", 4210, Check{KindTest, "go", "test", 0, 4210}, true},
		{"failing build", "cargo build", "error[E0425]\nexit status 101", 900, Check{KindBuild, "cargo", "build", 101, 900}, true},
		{"timed out lint", "npm run lint", "…\n… command timed out after 30s", 30000, Check{KindLint, "npm", "lint", -1, 30000}, true},
		{"not a check", "git status", "clean\n", 5, Check{}, false},
		{"withheld result", "go test ./...", "tool result withheld: unsafe", 5, Check{}, false},
		{"negative duration clamps", "make test", "", -5, Check{KindTest, "make", "test", 0, 0}, true},
	}
	for _, c := range cases {
		got, ok := FromBash(c.command, c.content, c.dur)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: FromBash = %+v, %v; want %+v, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestFromBashArgs(t *testing.T) {
	got, ok := FromBashArgs(`{"command":"pytest -x"}`, "1 passed", 800)
	if !ok || got != (Check{KindTest, "pytest", "", 0, 800}) {
		t.Fatalf("FromBashArgs = %+v, %v", got, ok)
	}
	for _, args := range []string{``, `not json`, `{}`, `{"command":"  "}`, `{"command":7}`} {
		if _, ok := FromBashArgs(args, "", 1); ok {
			t.Errorf("FromBashArgs(%q) reported a check", args)
		}
	}
}

func TestFromTestrun(t *testing.T) {
	argv := []string{"go", "test", "./..."}
	cases := []struct {
		name string
		res  testrun.Result
		kind Kind
		want Check
		ok   bool
	}{
		{"passed", testrun.Result{Command: argv, Status: testrun.Passed, Duration: 2 * time.Second}, KindTest, Check{KindTest, "go", "test", 0, 2000}, true},
		{"failed", testrun.Result{Command: argv, Status: testrun.Failed, ExitCode: 1, Duration: time.Second}, KindTest, Check{KindTest, "go", "test", 1, 1000}, true},
		{"timed out", testrun.Result{Command: argv, Status: testrun.TimedOut, ExitCode: -1, Duration: time.Minute}, KindTest, Check{KindTest, "go", "test", -1, 60000}, true},
		{"gate keeps its kind", testrun.Result{Command: argv, Status: testrun.Passed, Duration: time.Second}, KindGate, Check{KindGate, "go", "test", 0, 1000}, true},
		{"unknown program keeps the suite kind", testrun.Result{Command: []string{"./scripts/ci.sh", "unit"}, Status: testrun.Passed}, KindTest, Check{KindTest, "ci.sh", "", 0, 0}, true},
		{"denied never ran", testrun.Result{Command: argv, Status: testrun.Denied}, KindTest, Check{}, false},
		{"needs approval never ran", testrun.Result{Command: argv, Status: testrun.NeedsApproval}, KindTest, Check{}, false},
		{"errored never ran", testrun.Result{Command: argv, Status: testrun.Errored}, KindTest, Check{}, false},
		{"no command", testrun.Result{Status: testrun.Passed}, KindTest, Check{}, false},
		{"no kind and unknown program", testrun.Result{Command: []string{"./scripts/ci.sh"}, Status: testrun.Passed}, "", Check{}, false},
		{"no kind and a known program", testrun.Result{Command: []string{"cargo", "clippy"}, Status: testrun.Passed}, "", Check{KindLint, "cargo", "clippy", 0, 0}, true},
	}
	for _, c := range cases {
		got, ok := FromTestrun(c.res, c.kind)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: FromTestrun = %+v, %v; want %+v, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestCheckEntryShape(t *testing.T) {
	e := Check{KindTest, "go", "test", 1, 4210}.Entry()
	if e.Type != "check_run" || e.Role != "system" || e.Content != "" {
		t.Fatalf("entry = %+v", e)
	}
	b, err := json.Marshal(e.Meta)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"kind": "test", "program": "go", "subcommand": "test", "exit_code": 1.0, "duration_ms": 4210.0, "ok": false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("meta = %v; want %v", got, want)
	}
	if m := (Check{KindBuild, "make", "", 0, 5}).Meta(); m["ok"] != true {
		t.Fatalf("a zero exit is ok: %v", m)
	}
}
