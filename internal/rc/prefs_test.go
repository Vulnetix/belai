package rc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sessionsync"
)

func prefsDaemon(t *testing.T, on bool) (*Daemon, string) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	dir, _ := Normalize(t.TempDir())
	client, err := sessionsync.NewClient("http://127.0.0.1:1", func() (string, error) { return "ApiKey o:k", nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(Options{Client: client, HostID: "h", Dirs: []Dir{{Path: dir, Name: "proj", Source: SourceArg}}, ProjectSettings: on})
	if err != nil {
		t.Fatal(err)
	}
	return d, dir
}

func TestProjectPrefsRequestWritesHostPrefsOnly(t *testing.T) {
	d, dir := prefsDaemon(t, true)
	report, why := d.setProjectPrefs(sessionsync.Dispatch{Kind: "project_prefs", Cwd: dir,
		PrefsSet: map[string]any{"caveman": true, "tests.post_end": "goal", "jev.thresholds.simple_at": 0.85, "lsp.languages.go": false}})
	if why != "" {
		t.Fatalf("refused: %s", why)
	}
	if !strings.Contains(report, "set 4") {
		t.Fatalf("report = %q", report)
	}
	p, err := config.LoadProjectPrefs(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := p.Flat()
	if f["caveman"] != true || f["tests.post_end"] != "goal" || f["jev.thresholds.simple_at"] != 0.85 || f["lsp.languages.go"] != false {
		t.Fatalf("prefs = %v", f)
	}
	path, _ := config.ProjectPrefsPath(dir)
	if st, err := os.Stat(path); err != nil || st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("prefs file mode: %v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".vulnetix")); !os.IsNotExist(err) {
		t.Fatal("the repository was written")
	}
	// The advertisement carries what was set and where each value comes from.
	got := localPrefs(d.o.Dirs)[dir]
	if got.Prefs["caveman"] != true || got.Effective["caveman"].Origin != string(config.SourceProjectPrefs) {
		t.Fatalf("advertised %+v", got.Effective["caveman"])
	}
	// Clearing returns the key to the next layer.
	if _, why := d.setProjectPrefs(sessionsync.Dispatch{Cwd: dir, PrefsUnset: []string{"caveman"}}); why != "" {
		t.Fatal(why)
	}
	if p, _ := config.LoadProjectPrefs(dir); p.Caveman != nil {
		t.Fatal("caveman not cleared")
	}
}

func TestProjectPrefsRequestRefusals(t *testing.T) {
	d, dir := prefsDaemon(t, true)
	cases := []sessionsync.Dispatch{
		{Cwd: t.TempDir(), PrefsSet: map[string]any{"caveman": true}},        // not offered
		{Cwd: dir, PrefsSet: map[string]any{"permissions": "Bash(*)"}},       // not a key
		{Cwd: dir, PrefsSet: map[string]any{"tests.command": "rm"}},          // not a key
		{Cwd: dir, PrefsSet: map[string]any{"caveman": "yes"}},               // wrong shape
		{Cwd: dir, PrefsSet: map[string]any{"jev.thresholds.allow_at": 0.9}}, // breaks the band
		{Cwd: dir, PrefsSet: map[string]any{"lsp.languages.cobol": true}},    // unknown language
		{Cwd: dir}, // nothing
	}
	for i, c := range cases {
		if _, why := d.setProjectPrefs(c); why == "" {
			t.Fatalf("case %d accepted", i)
		}
	}
	if p, _ := config.LoadProjectPrefs(dir); len(p.Flat()) != 0 {
		t.Fatalf("a refused request wrote %v", p.Flat())
	}
	off, dir2 := prefsDaemon(t, false)
	if _, why := off.setProjectPrefs(sessionsync.Dispatch{Cwd: dir2, PrefsSet: map[string]any{"caveman": true}}); why == "" {
		t.Fatal("accepted without --web-project-settings")
	}
}
