package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/repoindex"
)

func TestVulnetixArgv(t *testing.T) {
	root := t.TempDir()
	v := &Vulnetix{Root: root}
	for _, c := range []struct {
		in      string
		want    []string // substrings of the joined argv
		wantNot []string
		scan    bool
		noteHas string
	}{
		// A bare scan is hardened, scoped, memory-free and reachability-off.
		{in: "sca", want: []string{"--no-banner --no-progress --no-analytics sca", "--disable-memory", "--path " + root, "--reachability off"}, scan: true, noteHas: "reachability was off"},
		// The failure from the session: fix without --path hit the
		// multi-manifest error, and fix without --dry-run edited go.mod.
		{in: "fix --manifest go.mod", want: []string{"fix --manifest go.mod", "--path " + root, "--dry-run"}, scan: true, noteHas: "--dry-run"},
		{in: "vulnetix sca --path web --reachability direct", want: []string{"--path " + filepath.Join(root, "web"), "--reachability direct"}, wantNot: []string{"--reachability off"}, scan: true},
		// sbom takes neither --path nor --disable-memory; adding them fails.
		{in: "sbom", want: []string{"sbom"}, wantNot: []string{"--path", "--disable-memory", "--reachability"}, scan: true},
		// malscan has --path but no --reachability.
		{in: "malscan", want: []string{"--path " + root}, wantNot: []string{"--reachability"}, scan: true},
		{in: "sca -o json-cyclonedx", want: []string{"-o json-cyclonedx"}, scan: true},
		{in: "vdb vuln CVE-2021-44228", want: []string{"vdb vuln CVE-2021-44228", "--disable-memory"}, wantNot: []string{"--path"}},
		{in: "auth status", want: []string{"auth status"}},
		// Help is passed through untouched.
		{in: "fix --help", want: []string{"fix --help"}, wantNot: []string{"--dry-run", "--path"}},
	} {
		argv, scan, note, err := v.buildArgv(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		joined := strings.Join(argv, " ")
		for _, w := range c.want {
			if !strings.Contains(joined, w) {
				t.Errorf("%q: argv %q lacks %q", c.in, joined, w)
			}
		}
		for _, w := range c.wantNot {
			if strings.Contains(joined, w) {
				t.Errorf("%q: argv %q has %q", c.in, joined, w)
			}
		}
		if scan != c.scan {
			t.Errorf("%q: scan = %v", c.in, scan)
		}
		if c.noteHas != "" && !strings.Contains(note, c.noteHas) {
			t.Errorf("%q: note %q lacks %q", c.in, note, c.noteHas)
		}
	}
}

func TestVulnetixRefuses(t *testing.T) {
	v := &Vulnetix{Root: t.TempDir()}
	for in, why := range map[string]string{
		"":                            "missing",
		"fix --yes":                   "--yes",
		"sca --sca-autofix":           "--sca-autofix",
		"sca --jail":                  "--jail",
		"auth login":                  "not allowed",
		"upload results.sarif":        "not allowed",
		"update":                      "not allowed",
		"vdb cache clear":             "writes to disk",
		"vdb exploits download CVE-1": "writes to disk",
		"sca --path ../other":         "outside the working tree",
		"sca -o /tmp/x.cdx.json":      "outside the working tree",
		"sca && rm -rf /":             "metacharacters",
		`vdb packages search "a b"`:   "quotes",
		"--path web sca":              "subcommand first",
	} {
		if _, _, _, err := v.buildArgv(in); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%q: err = %v, want %q", in, err, why)
		}
	}
}

func TestCleanVulnetixOutput(t *testing.T) {
	raw := "\x1b[1mScanning /repo (depth: 3)...\x1b[0m\n" +
		"-  Scan  ░░░░░░░░░░ 0/7 (0%)  Parsing 11 detected file(s)\n" +
		"-  Scan  ██░░░░░░░░ 1/7 (14%)\n" +
		"spinner ⠋\rspinner ⠙\rResolved 42 packages\n\n\n\n" +
		"CVE-2021-23337  high  lodash 4.17.20\n"
	got := CleanVulnetixOutput(raw)
	if strings.Contains(got, "\x1b") || strings.Contains(got, "░") || strings.Contains(got, "0/7") || strings.Contains(got, "\n\n\n") {
		t.Fatalf("noise kept:\n%s", got)
	}
	for _, want := range []string{"Scanning /repo", "Resolved 42 packages", "CVE-2021-23337  high  lodash 4.17.20"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q:\n%s", want, got)
		}
	}
	big := strings.Repeat("row of findings text\n", 10000)
	if got := CleanVulnetixOutput(big); len(got) > maxVulnetixOutput+200 || !strings.Contains(got, "bytes omitted") {
		t.Fatalf("long output not bounded: %d bytes", len(got))
	}
}

func TestVulnetixInCommand(t *testing.T) {
	for cmd, want := range map[string]bool{
		"vulnetix sca":                                   true,
		"/home/u/.local/bin/vulnetix fix --yes":          true,
		"cd site && vulnetix sca --path .":               true,
		"VULNETIX_ORG_ID=x vulnetix sca 2>&1 | tail -60": true,
		"timeout 60 vulnetix sca":                        true,
		"grep -r vulnetix internal":                      false,
		"ls ~/GitHub/Vulnetix/cli":                       false,
		"echo $(vulnetix --version)":                     true,
	} {
		if got := VulnetixInCommand(cmd); got != want {
			t.Errorf("VulnetixInCommand(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestBashRedirectsVulnetix(t *testing.T) {
	b := &Bash{Root: t.TempDir(), Vulnetix: true}
	_, err := b.Execute(context.Background(), map[string]any{"command": "vulnetix fix --yes 2>&1 | tail -50"})
	if err == nil || !strings.Contains(err.Error(), "Vulnetix tool") {
		t.Fatalf("err = %v, want a pointer to the Vulnetix tool", err)
	}
	if _, err := b.Execute(context.Background(), map[string]any{"command": "echo vulnetix"}); err != nil {
		t.Fatalf("a mention is not a run: %v", err)
	}
}

// Execute runs the shaped argv and returns cleaned output with a footer that
// says what ran and what the exit status means.
func TestVulnetixExecute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "vulnetix")
	script := "#!/bin/sh\necho \"argv: $*\"\nprintf -- '-  Scan  ██░░ 1/7 (14%%)\\n'\necho 'CVE-2021-23337 high lodash'\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	v := &Vulnetix{Root: root, Binary: bin}
	res, err := v.Execute(context.Background(), map[string]any{"command": "sca"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindRemote {
		t.Fatalf("kind = %s, want remote (classified)", res.Kind)
	}
	for _, want := range []string{"argv: --no-banner --no-progress --no-analytics sca --disable-memory --path " + root + " --reachability off", "CVE-2021-23337", "exit status 1: a gate found something", "reachability was off"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("result lacks %q:\n%s", want, res.Content)
		}
	}
	if strings.Contains(res.Content, "██") {
		t.Errorf("progress kept:\n%s", res.Content)
	}
}

// The tool is offered when vulnetix is on PATH, and then Bash redirects.
func TestDefaultWithCapsWiresVulnetix(t *testing.T) {
	caps := Capabilities{local: map[string]bool{"Vulnetix": true}}
	reg := DefaultWithCaps(t.TempDir(), false, caps, repoindex.Index{})
	var sawTool, redirected bool
	for _, tl := range reg.tools {
		switch x := tl.(type) {
		case *Vulnetix:
			sawTool = true
		case *Bash:
			redirected = x.Vulnetix
		}
	}
	if !sawTool || !redirected {
		t.Fatalf("Vulnetix tool %v, Bash redirect %v", sawTool, redirected)
	}
}
