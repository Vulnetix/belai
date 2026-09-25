package depwatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/vulnetixcli"
)

// A manifest edited several times in one turn is checked once, against the
// turn's net change; a non-manifest, a deleted manifest and an edit that was
// undone within the turn are not checked.
func TestCoalescerNetsATurn(t *testing.T) {
	var c Coalescer
	if c.Observe("main.go", "a", "b", false, false, false) {
		t.Fatal("main.go is not a manifest")
	}
	c.Observe("package.json", `{"a":1}`, `{"a":1,"b":2}`, false, false, false)
	c.Observe("package.json", `{"a":1,"b":2}`, `{"a":1,"b":3}`, false, false, false)
	c.Observe("go.mod", "module x", "module x\nrequire y v1", false, false, false)
	c.Observe("go.mod", "module x\nrequire y v1", "module x", false, false, false) // undone
	c.Observe("Cargo.toml", "", "[package]", true, false, false)
	c.Observe("Cargo.toml", "[package]", "", false, true, false) // deleted

	got := c.Drain()
	if len(got) != 1 || got[0].Path != "package.json" {
		t.Fatalf("drained %+v, want only package.json", got)
	}
	if got[0].Old != `{"a":1}` || got[0].New != `{"a":1,"b":3}` {
		t.Fatalf("net change = %q -> %q", got[0].Old, got[0].New)
	}
	if len(c.Drain()) != 0 {
		t.Fatal("Drain must reset the coalescer")
	}
}

func TestDigest(t *testing.T) {
	ch := Change{
		Path: "package.json",
		Info: Info{Type: "package.json", Ecosystem: "npm"},
		Old:  "{\n  \"keep\": \"2\",\n  \"a\": \"1.0.0\"\n}",
		New:  "{\n  \"keep\": \"2\",\n  \"a\": \"1.1.0\",\n  \"left-pad\": \"1.3.0\"\n}",
	}
	d := Digest(ch)
	for _, want := range []string{"manifest: package.json (type package.json, ecosystem npm)", "- ", `+   "a": "1.1.0",`, `"left-pad"`} {
		if !strings.Contains(d, want) {
			t.Errorf("digest lacks %q:\n%s", want, d)
		}
	}
	if strings.Contains(d, `"keep"`) {
		t.Errorf("digest carries an unchanged line:\n%s", d)
	}

	var big strings.Builder
	for i := range 500 {
		big.WriteString("line")
		big.WriteString(strings.Repeat("x", i%7))
		big.WriteString(string(rune('a' + i%26)))
		big.WriteString("\n")
	}
	d = Digest(Change{Path: "yarn.lock", Info: Info{Type: "yarn.lock", Ecosystem: "npm", Lock: true}, New: big.String()})
	if n := strings.Count(d, "\n+ "); n > maxDigestLines || !strings.Contains(d, "more changed lines not shown") {
		t.Errorf("large digest not bounded: %d lines", n)
	}
	if got := Digest(Change{Truncated: true}); !strings.Contains(got, "too large") {
		t.Errorf("truncated digest = %q", got)
	}
}

type fakeExec struct {
	calls [][]string
	scan  ExecResult
	cdx   string
	fix   ExecResult
	err   error
}

func (f *fakeExec) run(_ context.Context, _ string, args ...string) (ExecResult, error) {
	f.calls = append(f.calls, args)
	if args[0] == "fix" {
		return f.fix, nil
	}
	if f.cdx != "" {
		for i, a := range args {
			if a == "-o" {
				_ = os.WriteFile(args[i+1], []byte(f.cdx), 0o600)
			}
		}
	}
	return f.scan, f.err
}

const cdxWithVuln = `{"bomFormat":"CycloneDX","vulnerabilities":[{"id":"CVE-2021-23337","ratings":[{"severity":"high"}],"affects":[{"ref":"pkg:npm/lodash@4.17.20"}]}]}`

func change() Change {
	return Change{Path: "web/package.json", Info: Info{Type: "package.json", Ecosystem: "npm"}, Old: "{}", New: `{"dependencies":{"lodash":"4.17.20"}}`}
}

func TestCheckSkipsWhenNoDependencyChanged(t *testing.T) {
	f := &fakeExec{}
	c := Checker{Workdir: t.TempDir(), Exec: f.run, Decide: func(context.Context, string) (rolemanager.DepSentinel, error) {
		return rolemanager.DepsUnchanged, nil
	}}
	r := c.Check(context.Background(), change())
	if !r.Skipped || len(f.calls) != 0 || r.Clean() {
		t.Fatalf("report = %+v, calls = %v; want skipped with no CLI run", r, f.calls)
	}
}

// An evaluator that fails, or a change too large to digest, is checked
// anyway: the check is cheap, a missed vulnerable dependency is not.
func TestCheckRunsWhenTheEvaluatorCannotAnswer(t *testing.T) {
	for name, ch := range map[string]Change{"error": change(), "truncated": func() Change { c := change(); c.Truncated = true; return c }()} {
		f := &fakeExec{}
		asked := false
		c := Checker{Workdir: t.TempDir(), Exec: f.run, Decide: func(context.Context, string) (rolemanager.DepSentinel, error) {
			asked = true
			return "", errors.New("offline")
		}}
		r := c.Check(context.Background(), ch)
		if r.Skipped || len(f.calls) != 1 || !r.Clean() {
			t.Fatalf("%s: report = %+v, calls = %v", name, r, f.calls)
		}
		if name == "truncated" && asked {
			t.Fatal("a truncated change must not be sent to the evaluator")
		}
	}
}

func TestCheckFindingsAndSafeHarbour(t *testing.T) {
	wd := t.TempDir()
	f := &fakeExec{scan: ExecResult{Stdout: "lodash 4.17.20 CVE-2021-23337 high", ExitCode: 1}, cdx: cdxWithVuln, fix: ExecResult{Stdout: "lodash 4.17.20 -> 4.17.21 (Safe Harbour)"}}
	c := Checker{Workdir: wd, Exec: f.run, Plan: vulnetixcli.PlanPro}
	r := c.Check(context.Background(), change())
	if r.Err != nil || r.Clean() || r.ExitCode != 1 {
		t.Fatalf("report = %+v", r)
	}
	if len(r.Findings) != 1 || !strings.Contains(r.Findings[0], "CVE-2021-23337") {
		t.Fatalf("findings = %v", r.Findings)
	}
	if !strings.Contains(r.FixPlan, "Safe Harbour") || len(f.calls) != 2 {
		t.Fatalf("Pro plan must run the dry-run fix: plan %q, calls %v", r.FixPlan, f.calls)
	}
	scan := strings.Join(f.calls[0], " ")
	for _, want := range []string{"sca --disable-memory --path " + filepath.Join(wd, "web") + " --depth 1", "--block-malware", "--block-eol", "--exploits poc", "-o " + OutputPath(wd, "web/package.json")} {
		if !strings.Contains(scan, want) {
			t.Errorf("scan argv %q lacks %q", scan, want)
		}
	}
	if fix := strings.Join(f.calls[1], " "); !strings.Contains(fix, "fix --dry-run") || !strings.HasSuffix(fix, "--manifest package.json") {
		t.Errorf("fix argv = %q", fix)
	}
	if p := TaskPrompt(r); !strings.Contains(p, "web/package.json") || !strings.Contains(p, "Safe Harbour") || !strings.Contains(p, "tripped") {
		t.Errorf("task prompt = %q", p)
	}

	// A community plan never runs the fix plan and says Safe Harbour is a Pro feature.
	f = &fakeExec{scan: ExecResult{ExitCode: 1}, cdx: cdxWithVuln}
	r = Checker{Workdir: wd, Exec: f.run, Plan: vulnetixcli.PlanCommunity}.Check(context.Background(), change())
	if len(f.calls) != 1 || r.FixPlan != "" || !strings.Contains(TaskPrompt(r), "Pro would name Safe Harbour") {
		t.Fatalf("community: calls %v, plan %q", f.calls, r.FixPlan)
	}
}

// A lockfile's fix plan is not restricted by name: the fix lands in the
// manifest that declares the dependency.
func TestFixArgsLockfile(t *testing.T) {
	args := strings.Join(FixArgs("/w", Change{Path: "package-lock.json", Info: Info{Lock: true}}), " ")
	if strings.Contains(args, "--manifest") {
		t.Fatalf("lockfile fix argv = %q", args)
	}
}

func TestCheckCLIError(t *testing.T) {
	f := &fakeExec{scan: ExecResult{ExitCode: 2}}
	r := Checker{Workdir: t.TempDir(), Exec: f.run}.Check(context.Background(), change())
	if r.Err == nil || r.Clean() {
		t.Fatalf("exit 2 must be an error: %+v", r)
	}
}
