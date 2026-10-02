package bgproc

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

func specManager(t *testing.T) (*Manager, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("these tests run /bin/sh")
	}
	work := t.TempDir()
	t.Setenv("BELAI_HOME", t.TempDir())
	// A cap of one recovery attempt, already spent by the first exit, keeps a
	// finished process from starting a recovery turn: these tests care about what
	// ran, not about what happens after.
	settings := config.Settings{
		Resilience: &config.ResilienceSettings{MaxProcessRecoveries: 1},
		// The sandbox has its own tests; here it would hide the temp directories these
		// tests write to.
		Sandbox: &config.SandboxSettings{Mode: "off"},
	}
	return NewManager(work, run.Config{}, nil, settings, posture.Defaults(), tools.Capabilities{}), work
}

func s(v string) *string { return &v }

// doc builds a document running a /bin/sh script with the given arguments.
func shDoc(script string, args ...string) libitem.ProcessDoc {
	return libitem.ProcessDoc{Command: "sh", Args: append([]string{"-c", script, "sh"}, args...)}
}

// waitFor polls until cond is true.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func fileHas(path, want string) func() bool {
	return func() bool {
		b, err := os.ReadFile(path)
		return err == nil && string(b) == want
	}
}

func logOf(t *testing.T, m *Manager, id string) string {
	t.Helper()
	p, ok := m.Lookup(id)
	if !ok {
		t.Fatalf("process %s is gone", id)
	}
	b, _ := os.ReadFile(p.LogPath)
	return string(b)
}

// The command and its arguments are an argv. Nothing is split, quoted or
// expanded, so a value is one argument whatever it holds.
func TestStartSpecRunsAnArgvWithNoShell(t *testing.T) {
	m, work := specManager(t)
	out := filepath.Join(work, "out.txt")
	doc := libitem.ProcessDoc{
		Command: "printf",
		Args:    []string{"[%s]", "a b", "$HOME", "x;touch pwned", "*", "`id`", "$(id)", "", "-n"},
		Stdout:  &libitem.Redirect{Mode: "file", Path: &out},
	}
	p, err := m.StartSpec("argv", doc)
	if err != nil {
		t.Fatal(err)
	}
	want := "[a b][$HOME][x;touch pwned][*][`id`][$(id)][][-n]"
	waitFor(t, "the output", fileHas(out, want))
	if _, err := os.Stat(filepath.Join(work, "pwned")); !os.IsNotExist(err) {
		t.Fatal("a shell metacharacter in an argument ran")
	}
	if p.Command != "printf '[%s]' 'a b' '$HOME' 'x;touch pwned' '*' '`id`' '$(id)' '' -n" {
		t.Errorf("display command = %q", p.Command)
	}
	if p.Name != "argv" || p.Dir != work {
		t.Errorf("process = %+v", p)
	}
}

func TestStartSpecRendersOptionsBeforeArgs(t *testing.T) {
	m, work := specManager(t)
	out := filepath.Join(work, "out.txt")
	doc := libitem.ProcessDoc{
		Command: "sh",
		Options: []libitem.ProcessOption{{Name: "-o", Value: s("errexit")}, {Name: "-u"}},
		Args:    []string{"-c", `for a in "$@"; do printf '[%s]' "$a"; done; case $- in *e*u*|*u*e*) printf ' errexit+nounset';; esac`, "sh", "x", "y z"},
		Stdout:  &libitem.Redirect{Mode: "file", Path: &out},
	}
	if _, err := m.StartSpec("opts", doc); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the output", fileHas(out, "[x][y z] errexit+nounset"))
}

func TestStartSpecEnvironment(t *testing.T) {
	m, work := specManager(t)
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	t.Setenv("HOST_TOKEN", "copied-from-host")
	t.Setenv("BELAI_SOMETHING", "belai-internal")
	out := filepath.Join(work, "out.txt")
	doc := shDoc(`printf '%s|%s|%s|%s|%s|%s' "$LITERAL" "$MY_TOKEN" "${OPENAI_API_KEY-unset}" "${BELAI_SOMETHING-unset}" "$PATH_COPY" "$EMPTY"`)
	doc.Env = map[string]string{"LITERAL": "plain value", "MY_TOKEN": "env:HOST_TOKEN", "PATH_COPY": "env:HOST_TOKEN", "EMPTY": ""}
	doc.Stdout = &libitem.Redirect{Mode: "file", Path: &out}
	if _, err := m.StartSpec("env", doc); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the output", fileHas(out, "plain value|copied-from-host|unset|unset|copied-from-host|"))
}

// A value that env:OTHER names comes from the host's own environment, even for
// a name the scrubber strips from every other child.
func TestStartSpecCopiesAScrubbedVariableOnlyWhenAsked(t *testing.T) {
	m, work := specManager(t)
	m.lookupEnv = func(k string) (string, bool) {
		if k == "ANTHROPIC_API_KEY" {
			return "sk-test", true
		}
		return "", false
	}
	out := filepath.Join(work, "out.txt")
	doc := shDoc(`printf '%s' "${KEY-unset}"`)
	doc.Env = map[string]string{"KEY": "env:ANTHROPIC_API_KEY"}
	doc.Stdout = &libitem.Redirect{Mode: "file", Path: &out}
	if _, err := m.StartSpec("copy", doc); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the output", fileHas(out, "sk-test"))
}

func TestStartSpecRefusesAnUnsetCopiedVariable(t *testing.T) {
	m, work := specManager(t)
	m.lookupEnv = func(string) (string, bool) { return "", false }
	out := filepath.Join(work, "out.txt")
	doc := shDoc("true")
	doc.Env = map[string]string{"SOME_TOKEN": "env:NOT_SET_ANYWHERE"}
	doc.Stdout = &libitem.Redirect{Mode: "file", Path: &out}
	_, err := m.StartSpec("missing", doc)
	if err == nil || !strings.Contains(err.Error(), "NOT_SET_ANYWHERE is not set") || !strings.Contains(err.Error(), "SOME_TOKEN") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("a file was opened for a process that did not start")
	}
	if len(m.List()) != 0 {
		t.Errorf("a refused start left a process: %+v", m.List())
	}
	// Nothing is left locked: the same name starts once the variable is set.
	m.lookupEnv = func(string) (string, bool) { return "v", true }
	if _, err := m.StartSpec("missing", doc); err != nil {
		t.Fatalf("restart after the refusal: %v", err)
	}
}

func TestStartSpecWorkingDirectory(t *testing.T) {
	m, work := specManager(t)
	sub := filepath.Join(work, "services", "api")
	other := filepath.Join(work, "elsewhere")
	home := filepath.Join(work, "home")
	for _, d := range []string{other, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for name, c := range map[string]struct{ cwd, want string }{
		"default":     {"", resolved(work)},
		"relative":    {"services/api", resolved(sub)},
		"absolute":    {other, resolved(other)},
		"home":        {"~", resolved(home)},
		"home subdir": {"~/app", resolved(filepath.Join(home, "app"))},
	} {
		out := filepath.Join(t.TempDir(), "out.txt")
		doc := shDoc(`pwd -P`)
		doc.Cwd = c.cwd
		doc.Stdout = &libitem.Redirect{Mode: "file", Path: &out}
		if _, err := m.StartSpec("cwd-"+strings.ReplaceAll(name, " ", "-"), doc); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		waitFor(t, name, fileHas(out, c.want+"\n"))
	}
	for _, bad := range []string{"no/such/dir", "../escape", "~other/x"} {
		doc := shDoc("true")
		doc.Cwd = bad
		if _, err := m.StartSpec("bad-cwd", doc); err == nil {
			t.Errorf("cwd %q started", bad)
		}
	}
	// The sandbox roots stay the project directory: a document cannot widen them
	// by naming a working directory.
	p, err := m.StartSpec("roots", func() libitem.ProcessDoc { d := shDoc("sleep 5"); d.Cwd = other; return d }())
	if err != nil || p.Dir != work {
		t.Fatalf("Dir = %q, %v; want the project directory", p.Dir, err)
	}
}

func TestStartSpecRedirects(t *testing.T) {
	m, work := specManager(t)
	dir := t.TempDir()
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(dir, name)); return string(b) }
	path := func(name string) *string { return s(filepath.Join(dir, name)) }
	script := `printf 'out1\n'; printf 'err1\n' >&2`

	// stdout to a file, stderr merged into it (the default).
	d1 := shDoc(script)
	d1.Stdout = &libitem.Redirect{Mode: "file", Path: path("merged.txt")}
	p1, err := m.StartSpec("merged", d1)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "merged", func() bool { return strings.Count(read("merged.txt"), "\n") == 2 })
	if got := read("merged.txt"); !strings.Contains(got, "out1\n") || !strings.Contains(got, "err1\n") {
		t.Errorf("merged = %q", got)
	}
	if logOf(t, m, p1.ID) != "" {
		t.Errorf("the log holds output that went to a file: %q", logOf(t, m, p1.ID))
	}

	// stdout and stderr to different files.
	d2 := shDoc(script)
	d2.Stdout = &libitem.Redirect{Mode: "file", Path: path("o.txt")}
	d2.Stderr = &libitem.Redirect{Mode: "file", Path: path("e.txt")}
	if _, err := m.StartSpec("split", d2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "split", func() bool { return read("o.txt") == "out1\n" && read("e.txt") == "err1\n" })

	// stdout to a file, stderr to the log.
	d3 := shDoc(script)
	d3.Stdout = &libitem.Redirect{Mode: "file", Path: path("only-out.txt")}
	d3.Stderr = &libitem.Redirect{Mode: "log"}
	p3, err := m.StartSpec("errlog", d3)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "errlog", func() bool { return read("only-out.txt") == "out1\n" && strings.Contains(logOf(t, m, p3.ID), "err1") })
	if strings.Contains(logOf(t, m, p3.ID), "out1") {
		t.Errorf("the log holds stdout, which went to a file")
	}

	// discard: nothing anywhere.
	d4 := shDoc(script + `; printf done > ` + filepath.Join(dir, "d4.done"))
	d4.Stdout = &libitem.Redirect{Mode: "discard"}
	d4.Stderr = &libitem.Redirect{Mode: "discard"}
	p4, err := m.StartSpec("quiet", d4)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "quiet", fileHas(filepath.Join(dir, "d4.done"), "done"))
	if got := logOf(t, m, p4.ID); got != "" {
		t.Errorf("a discarded stream reached the log: %q", got)
	}

	// The default sends both to the log.
	p5, err := m.StartSpec("default", shDoc(script))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "default", func() bool {
		l := logOf(t, m, p5.ID)
		return strings.Contains(l, "out1") && strings.Contains(l, "err1")
	})

	// Two streams naming one path share one file: no clobbering.
	d6 := shDoc(script)
	d6.Stdout = &libitem.Redirect{Mode: "file", Path: path("same.txt")}
	d6.Stderr = &libitem.Redirect{Mode: "file", Path: path("same.txt")}
	if _, err := m.StartSpec("same", d6); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "same", func() bool { return strings.Count(read("same.txt"), "\n") == 2 })

	// A path relative to the project directory.
	d7 := shDoc(script)
	d7.Stdout = &libitem.Redirect{Mode: "file", Path: s("rel.txt")}
	if _, err := m.StartSpec("rel", d7); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "rel", func() bool { return strings.Count(readFile(filepath.Join(work, "rel.txt")), "\n") == 2 })
}

func readFile(p string) string { b, _ := os.ReadFile(p); return string(b) }

func TestStartSpecFileTruncatesAndAppendAppends(t *testing.T) {
	m, work := specManager(t)
	f := filepath.Join(work, "log.txt")
	if err := os.WriteFile(f, []byte("old contents\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(mode, name string) {
		d := shDoc(`printf 'new\n'`)
		d.Stdout = &libitem.Redirect{Mode: mode, Path: &f}
		p, err := m.StartSpec(name, d)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, name+" to exit", func() bool {
			q, ok := m.Lookup(p.ID)
			return !ok || q.State != StateRunning
		})
	}
	run("append", "app1")
	if got := readFile(f); got != "old contents\nnew\n" {
		t.Fatalf("after append: %q", got)
	}
	run("append", "app2")
	if got := readFile(f); got != "old contents\nnew\nnew\n" {
		t.Fatalf("after a second append: %q", got)
	}
	run("file", "trunc")
	if got := readFile(f); got != "new\n" {
		t.Fatalf("after file: %q", got)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(f); fi.Mode().Perm() != 0o644 {
			t.Logf("an existing file keeps its mode: %v", fi.Mode().Perm())
		}
		fresh := filepath.Join(work, "fresh.txt")
		d := shDoc("true")
		d.Stdout = &libitem.Redirect{Mode: "file", Path: &fresh}
		if _, err := m.StartSpec("fresh", d); err != nil {
			t.Fatal(err)
		}
		if fi, err := os.Stat(fresh); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("a new redirect file is %v, %v; want 0600", fi.Mode().Perm(), err)
		}
	}
}

func TestStartSpecRefusesUnsafeRedirectTargets(t *testing.T) {
	m, work := specManager(t)
	outside := t.TempDir()
	target := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(work, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	dirAsFile := filepath.Join(work, "adir")
	if err := os.Mkdir(dirAsFile, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ path, want string }{
		"a symbolic link":      {link, "symbolic link"},
		"a directory":          {dirAsFile, "not a regular file"},
		"a missing directory":  {filepath.Join(work, "no", "such", "dir", "f"), "no such file"},
		"escaping the project": {"../out", "leaves the project directory"},
	} {
		d := shDoc("true")
		d.Stdout = &libitem.Redirect{Mode: "file", Path: s(c.path)}
		if _, err := m.StartSpec("unsafe", d); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one naming %q", name, err, c.want)
		}
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Errorf("the link target was written: %q", b)
	}
	if len(m.List()) != 0 {
		t.Errorf("a refused start left %d processes", len(m.List()))
	}
}

// A process that names a user is refused unless Belai runs as root, and is never
// started as the user who runs Belai in its place.
func TestStartSpecWithAUserNeverFallsBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("this test is for a process that is not root")
	}
	m, work := specManager(t)
	ran := filepath.Join(work, "ran.txt")
	d := shDoc("printf ran > " + ran)
	d.User = "nobody"
	out := filepath.Join(work, "out.txt")
	d.Stdout = &libitem.Redirect{Mode: "file", Path: &out}
	_, err := m.StartSpec("asuser", d)
	if err == nil || !strings.Contains(err.Error(), "not running as root") {
		t.Fatalf("err = %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(ran); !os.IsNotExist(err) {
		t.Fatal("the process ran as the current user")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("a redirect file was opened for a refused process")
	}
	if len(m.List()) != 0 {
		t.Errorf("a refused start left %d processes", len(m.List()))
	}
}

func TestStartSpecAsAnotherUserWhenRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("running as another user needs root")
	}
	m, work := specManager(t)
	if err := os.Chmod(work, 0o777); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(work, "out.txt")
	d := shDoc(`printf '%s %s' "$(id -u)" "$USER"`)
	d.User = "nobody"
	d.Stdout = &libitem.Redirect{Mode: "file", Path: &out}
	if _, err := m.StartSpec("asnobody", d); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the output", func() bool {
		b, _ := os.ReadFile(out)
		return strings.HasSuffix(string(b), " nobody") && !strings.HasPrefix(string(b), "0 ")
	})
	if _, err := m.StartSpec("nouser", func() libitem.ProcessDoc { d := shDoc("true"); d.User = "no-such-user-xyz"; return d }()); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("a missing user: %v", err)
	}
}

func TestStartSpecRefusesWhatItCannotRun(t *testing.T) {
	m, _ := specManager(t)
	for name, c := range map[string]struct {
		name string
		doc  libitem.ProcessDoc
		want string
	}{
		"no name":               {"", shDoc("true"), "needs a name"},
		"no command":            {"x", libitem.ProcessDoc{}, "command is required"},
		"a secret literal":      {"x", func() libitem.ProcessDoc { d := shDoc("true"); d.Env = map[string]string{"DB_PASSWORD": "x"}; return d }(), "looks like a secret"},
		"a bad option":          {"x", libitem.ProcessDoc{Command: "true", Options: []libitem.ProcessOption{{Name: "nodash"}}}, "is not an option name like -v or --verbose"},
		"a bad name":            {"X", shDoc("true"), "lowercase"},
		"stdout merged to self": {"x", func() libitem.ProcessDoc { d := shDoc("true"); d.Stdout = &libitem.Redirect{Mode: "stdout"}; return d }(), "only valid for stderr"},
	} {
		if _, err := m.StartSpec(c.name, c.doc); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one naming %q", name, err, c.want)
		}
	}
	// A command that is not there is the OS's error, with nothing left behind.
	if _, err := m.StartSpec("nothere", libitem.ProcessDoc{Command: "/no/such/binary-xyz"}); err == nil {
		t.Error("a missing binary started")
	}
	if len(m.List()) != 0 {
		t.Errorf("refused starts left %d processes", len(m.List()))
	}
}

func TestSpecProcessStopsAndRelaunches(t *testing.T) {
	m, work := specManager(t)
	out := filepath.Join(work, "starts.txt")
	d := shDoc(`printf 'start\n' >> ` + out + `; sleep 30`)
	p, err := m.StartSpec("again", d)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first start", func() bool { return strings.Count(readFile(out), "start") == 1 })
	p2, err := m.Relaunch(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second start", func() bool { return strings.Count(readFile(out), "start") == 2 })
	if p2.Name != "again" || p2.Command != p.Command {
		t.Errorf("relaunched %+v, was %+v", p2, p)
	}
	if err := m.Stop(p2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Relaunch("p999"); err == nil {
		t.Error("relaunched a process that does not exist")
	}
	// A legacy process relaunches too.
	lp, err := m.Start("legacy", "sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	if lp2, err := m.Relaunch(lp.ID); err != nil || lp2.Command != "sleep 30" {
		t.Fatalf("legacy relaunch: %+v %v", lp2, err)
	} else {
		_ = m.Stop(lp2.ID)
	}
}

// The recovery subagent may amend the flags of a shell command a user typed. It
// may not change a structured process, which restarts as it was defined.
func TestRecoveryCannotAmendAStructuredProcess(t *testing.T) {
	m, _ := specManager(t)
	p, err := m.StartSpec("fixed", libitem.ProcessDoc{Command: "sleep", Args: []string{"30"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RestartProcess(p.ID, "sleep 1; touch /tmp/never"); err != nil {
		t.Fatal(err)
	}
	if cmd, ok := m.ProcessCommand(p.ID); !ok || cmd != "sleep 30" {
		t.Fatalf("command after a recovery restart = %q, %v; want it unchanged", cmd, ok)
	}
	// A legacy process takes the amended command, as before.
	lp, err := m.Start("legacy2", "sleep 31")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RestartProcess(lp.ID, "sleep 32"); err != nil {
		t.Fatal(err)
	}
	if cmd, _ := m.ProcessCommand(lp.ID); cmd != "sleep 32" {
		t.Fatalf("legacy command = %q", cmd)
	}
	_ = m.Stop(p.ID)
	_ = m.Stop(lp.ID)
}

func TestResolveSpecPath(t *testing.T) {
	cases := []struct {
		in, want, err string
	}{
		{"", "", ""},
		{"~", "/home/u", ""},
		{"~/a/b", "/home/u/a/b", ""},
		{"~other/x", "", "only ~ and ~/"},
		{"/abs/../x", "/x", ""},
		{"rel/x", "/proj/rel/x", ""},
		{"./rel", "/proj/rel", ""},
		{"../up", "", "leaves the project directory"},
		{"a/../../up", "", "leaves the project directory"},
	}
	for _, c := range cases {
		got, err := resolveSpecPath(c.in, "/proj", "/home/u")
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: err = %v, want one naming %q", c.in, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if _, err := resolveSpecPath("~", "/proj", ""); err == nil {
		t.Error("~ with no home resolved")
	}
}

func TestSpecEnvOrderAndOverride(t *testing.T) {
	m, _ := specManager(t)
	t.Setenv("SPEC_BASE", "base")
	m.lookupEnv = func(k string) (string, bool) { return "from-" + k, true }
	env, err := m.specEnv(libitem.ProcessDoc{Env: map[string]string{"SPEC_BASE": "override", "B": "2", "A": "env:HOSTA"}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	var order []string
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
		order = append(order, k)
	}
	if got["SPEC_BASE"] != "override" || got["B"] != "2" || got["A"] != "from-HOSTA" {
		t.Errorf("env = %v", got)
	}
	// An override replaces the entry in place; new names follow in name order.
	var tail []string
	for _, k := range order {
		if k == "A" || k == "B" {
			tail = append(tail, k)
		}
	}
	if strings.Join(tail, ",") != "A,B" {
		t.Errorf("new names are not in order: %v", tail)
	}
	count := 0
	for _, k := range order {
		if k == "SPEC_BASE" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("SPEC_BASE appears %d times", count)
	}
}

// exec.LookPath is the OS's own rule for a bare command name.
func TestStartSpecFindsACommandOnPath(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("no true on PATH")
	}
	m, _ := specManager(t)
	if _, err := m.StartSpec("t", libitem.ProcessDoc{Command: "true"}); err != nil {
		t.Fatal(err)
	}
}

// Under the default sandbox a structured process runs like any other command.
func TestStartSpecRunsUnderTheSandbox(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix only")
	}
	work := t.TempDir()
	t.Setenv("BELAI_HOME", t.TempDir())
	settings := config.Settings{Resilience: &config.ResilienceSettings{MaxProcessRecoveries: 1}}
	m := NewManager(work, run.Config{}, nil, settings, posture.Defaults(), tools.Capabilities{})
	out := filepath.Join(work, "out.txt")
	d := shDoc(`printf '%s' "$1"`, "in the sandbox")
	d.Stdout = &libitem.Redirect{Mode: "file", Path: &out}
	if _, err := m.StartSpec("boxed", d); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the output", fileHas(out, "in the sandbox"))
}
