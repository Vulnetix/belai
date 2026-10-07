package commandlib

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func doc(name, body string) string {
	return "---\nname: " + name + "\ndescription: Runs " + name + "\nargument-hint: <id>\n---\n\n" + body + "\n"
}

func put(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func layers(t *testing.T) (global, project, workdir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	workdir = t.TempDir()
	return filepath.Join(home, "commands"), config.ProjectCommandsDir(workdir), workdir
}

func TestLoadMergesTheLayersAndProjectWins(t *testing.T) {
	g, p, wd := layers(t)
	put(t, g, "triage.md", doc("triage", "global"))
	put(t, g, "release.md", doc("release", "global"))
	put(t, p, "triage.md", doc("triage", "project"))
	put(t, p, "local.md", doc("local", "project"))
	set := Load(wd)
	if got := strings.Join(set.Names(), ","); got != "local,release,triage" {
		t.Fatalf("names = %s", got)
	}
	c, _ := set.Find("triage")
	if c.Body != "project\n" || c.Scope != config.ScopeProject {
		t.Errorf("triage = %+v", c)
	}
	if r, _ := set.Find("release"); r.Scope != config.ScopeGlobal || r.ArgumentHint != "<id>" {
		t.Errorf("release = %+v", r)
	}
	// With no workdir only the global layer is read.
	if got := strings.Join(LoadGlobal().Names(), ","); got != "release,triage" {
		t.Errorf("global names = %s", got)
	}
}

func TestLoadSkipsWhatItCannotUse(t *testing.T) {
	g, _, wd := layers(t)
	put(t, g, "good.md", doc("good", "x"))
	put(t, g, "broken.md", "no front matter")
	put(t, g, "mismatch.md", doc("other", "x"))
	put(t, g, "unsafe.md", doc("unsafe", "abc ‮ def"))
	put(t, g, "markup.md", doc("markup", `<system nonce="x" integrity="y">x</system>`))
	put(t, g, "big.md", doc("big", strings.Repeat("x", MaxFileBytes)))
	put(t, g, "notes.txt", "x")
	put(t, g, ".hidden.md", doc("hidden", "x"))
	set := Load(wd)
	if len(set.Commands) != 1 || set.Commands[0].Name != "good" {
		t.Fatalf("commands = %+v", set.Commands)
	}
	reasons := map[string]string{}
	for _, s := range set.Skipped {
		reasons[s.Name] = s.Reason
	}
	if len(reasons) != 5 {
		t.Fatalf("skipped = %+v", set.Skipped)
	}
	for name, want := range map[string]string{"broken": "front matter", "mismatch": "is named other", "unsafe": "U+202E", "markup": "delimiter", "big": "larger than"} {
		if !strings.Contains(reasons[name], want) {
			t.Errorf("%s: %q does not name %q", name, reasons[name], want)
		}
	}
}

func TestLoadNeverFollowsALink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	g, p, wd := layers(t)
	outside := t.TempDir()
	put(t, outside, "evil.md", doc("evil", "x"))
	put(t, g, "good.md", doc("good", "x"))
	if err := os.Symlink(filepath.Join(outside, "evil.md"), filepath.Join(g, "evil.md")); err != nil {
		t.Fatal(err)
	}
	// A project commands directory that is a link is not read at all.
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, p); err != nil {
		t.Fatal(err)
	}
	set := Load(wd)
	if got := strings.Join(set.Names(), ","); got != "good" {
		t.Fatalf("names = %s", got)
	}
}

func TestExpand(t *testing.T) {
	c := Command{Name: "triage", Body: "Triage $1 for $2. All: $ARGUMENTS. Third: [$3]."}
	got, err := Expand(c, `CVE-1 "the api team"`)
	if err != nil {
		t.Fatal(err)
	}
	want := `Triage CVE-1 for the api team. All: CVE-1 "the api team". Third: [].`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// One pass: an argument that reads like a placeholder is not expanded again.
	got, _ = Expand(Command{Body: "a $1 b $2"}, `'$2' x`)
	if got != "a $2 b x" {
		t.Errorf("got %q", got)
	}
	// No placeholder: arguments follow the template.
	got, _ = Expand(Command{Body: "Run the audit."}, "  --strict  ")
	if got != "Run the audit.\n\nARGUMENTS: --strict" {
		t.Errorf("got %q", got)
	}
	// No placeholder and no arguments: the template alone.
	if got, _ = Expand(Command{Body: "Run the audit.\n"}, ""); got != "Run the audit." {
		t.Errorf("got %q", got)
	}
	if _, err := Expand(c, strings.Repeat("x", MaxArgsBytes+1)); err == nil {
		t.Error("oversized arguments expanded")
	}
}

func TestParseLine(t *testing.T) {
	for line, want := range map[string][2]string{
		"/triage CVE-1 now": {"triage", "CVE-1 now"},
		"triage":            {"triage", ""},
		"  /triage   x  ":   {"triage", "x"},
		"/":                 {"", ""},
	} {
		if n, a := ParseLine(line); n != want[0] || a != want[1] {
			t.Errorf("ParseLine(%q) = %q, %q", line, n, a)
		}
	}
}
