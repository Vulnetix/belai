package libstore

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/processlib"
)

const procDoc = `{"name":"web","command":"python3","args":["-m","http.server"],"options":[{"name":"--bind","value":"127.0.0.1"}],"env":{"PORT":"8080","TOKEN":"env:WEB_TOKEN"},"cwd":"~/srv","stdout":{"mode":"file","path":"/tmp/web.log"},"order":40}`

func globalProcesses(t *testing.T) []processlib.Entry {
	t.Helper()
	l, err := processlib.Load(config.ScopeGlobal, "")
	if err != nil {
		t.Fatal(err)
	}
	return l.Entries
}

func TestProcessInstallWritesTheDocumentAsAFile(t *testing.T) {
	h := home(t)
	res, err := Install(libitem.Process, []byte(procDoc), InstallOptions{Name: "web"})
	if err != nil || res.Replaced {
		t.Fatalf("install: %+v %v", res, err)
	}
	want := filepath.Join(h, "processes", "040-web.json")
	if res.Where != want {
		t.Fatalf("Where = %q, want %q", res.Where, want)
	}
	b, err := os.ReadFile(want)
	if err != nil || !strings.Contains(string(b), "\"command\": \"python3\"") {
		t.Fatalf("file = %q, %v: want indented JSON", b, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(want); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", fi.Mode().Perm())
		}
	}
	es := globalProcesses(t)
	if len(es) != 1 || !es[0].Structured() || es[0].Order != 40 || !es[0].Enabled || es[0].Spec.Command != "python3" {
		t.Fatalf("library = %+v", es)
	}
}

// What is installed exports as the library's own bytes: the next sync sees no edit.
func TestProcessExportsTheInstalledDocumentByteForByte(t *testing.T) {
	home(t)
	// An explicit empty list and a spelling the host would not write itself.
	doc := `{ "name": "web", "command": "x", "args": [], "env": {"B":"2","A":"1"}, "enabled": true }`
	if _, err := Install(libitem.Process, []byte(doc), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	want, _ := libitem.Validate(libitem.Process, []byte(doc))
	got, err := Get(libitem.Process, "web")
	if err != nil || string(got.Doc) != string(want.Doc) || got.SHA256 != want.SHA256 {
		t.Fatalf("exported %q (%v), want %q", got.Doc, err, want.Doc)
	}
}

func TestProcessDisabledAndOrderedFollowTheFileName(t *testing.T) {
	home(t)
	if _, err := Install(libitem.Process, []byte(`{"name":"off","command":"x","enabled":false,"order":70}`), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	es := globalProcesses(t)
	if len(es) != 1 || es[0].Enabled || es[0].Order != 70 || filepath.Base(es[0].Path) != "_070-off.json" {
		t.Fatalf("entry = %+v", es)
	}
	// Enabling it by hand (the file name) is an edit: the exported document says so.
	if _, err := processlib.SetEnabled(es[0], true); err != nil {
		t.Fatal(err)
	}
	got, _ := Get(libitem.Process, "off")
	d, _ := libitem.ParseProcess(got.Doc)
	if !d.IsEnabled() {
		t.Fatalf("doc after enabling = %s", got.Doc)
	}
	if strings.Contains(string(got.Doc), `"enabled"`) {
		t.Errorf("an enabled process still says enabled:false: %s", got.Doc)
	}
}

func TestProcessLegacyShellFileSyncsAsShDashC(t *testing.T) {
	home(t)
	if _, err := processlib.CreateUnique(config.ScopeGlobal, "", "python3 -m http.server --bind 127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	items, skipped, err := List(libitem.Process)
	if err != nil || len(items) != 1 || len(skipped) != 0 {
		t.Fatalf("list: %+v %+v %v", items, skipped, err)
	}
	d, err := libitem.ParseProcess(items[0].Doc)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Name != "python3" || d.Command != "sh" || len(d.Args) != 2 || d.Args[0] != "-c" || d.Args[1] != "python3 -m http.server --bind 127.0.0.1" || d.OrderOr(0) != 10 {
		t.Fatalf("doc = %s", items[0].Doc)
	}
	// Installing a structured document over it, with replace, takes its place.
	res, err := Install(libitem.Process, []byte(`{"name":"python3","command":"python3","args":["-V"]}`), InstallOptions{Overwrite: true})
	if err != nil || !res.Replaced {
		t.Fatalf("replace: %+v %v", res, err)
	}
	es := globalProcesses(t)
	if len(es) != 1 || !es[0].Structured() || es[0].Order != 10 {
		t.Fatalf("after replace: %+v", es)
	}
}

func TestProcessLegacyTooLongForTheLibraryIsSkipped(t *testing.T) {
	home(t)
	long := "echo " + strings.Repeat("x", 1100)
	if _, err := processlib.CreateUnique(config.ScopeGlobal, "", long); err != nil {
		t.Fatal(err)
	}
	items, skipped, err := List(libitem.Process)
	if err != nil || len(items) != 0 || len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "cannot be a library item") {
		t.Fatalf("items %+v skipped %+v %v", items, skipped, err)
	}
}

func TestProcessInstallNeedsOverwriteToReplace(t *testing.T) {
	home(t)
	if _, err := Install(libitem.Process, []byte(`{"name":"web","command":"v1"}`), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(libitem.Process, []byte(`{"name":"web","command":"v2"}`), InstallOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v", err)
	}
	if es := globalProcesses(t); es[0].Spec.Command != "v1" {
		t.Fatal("a refused install changed the process")
	}
	res, err := Install(libitem.Process, []byte(`{"name":"web","command":"v2"}`), InstallOptions{Overwrite: true})
	if err != nil || !res.Replaced {
		t.Fatalf("overwrite: %+v %v", res, err)
	}
	if es := globalProcesses(t); len(es) != 1 || es[0].Spec.Command != "v2" {
		t.Fatalf("after overwrite: %+v", es)
	}
}

func TestProcessInstallRefusals(t *testing.T) {
	home(t)
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"a name the file name cannot hold", `{"name":"my.web","command":"x"}`, "lowercase letters, digits and hyphens"},
		{"a name with an underscore", `{"name":"my_web","command":"x"}`, "lowercase letters, digits and hyphens"},
		{"a literal secret", `{"name":"web","command":"x","env":{"API_KEY":"sk-live"}}`, "looks like a secret"},
		{"an unknown key", `{"name":"web","command":"x","restart":"always"}`, "unknown field"},
		{"a shell string as the command", `{"name":"web","command":"a\nb"}`, "control character"},
		{"not JSON", `echo hi`, "not valid JSON"},
		{"a redirect without a path", `{"name":"web","command":"x","stdout":{"mode":"file"}}`, "path is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Install(libitem.Process, []byte(c.doc), InstallOptions{})
			if err == nil || !IsRefusal(err) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want a refusal naming %q", err, c.want)
			}
		})
	}
	if es := globalProcesses(t); len(es) != 0 {
		t.Fatalf("a refused install wrote %+v", es)
	}
}

// A project-scope process is never listed, and so never synced.
func TestOnlyGlobalProcessesAreListed(t *testing.T) {
	home(t)
	proj := t.TempDir()
	if _, err := processlib.CreateUnique(config.ScopeProject, proj, "secret-project-service --flag"); err != nil {
		t.Fatal(err)
	}
	if _, err := processlib.CreateUnique(config.ScopeGlobal, "", "mine"); err != nil {
		t.Fatal(err)
	}
	items, _, err := List(libitem.Process)
	if err != nil || len(items) != 1 || items[0].Name != "mine" {
		t.Fatalf("items = %+v, %v", items, err)
	}
}

func TestProcessListReportsABrokenStructuredFile(t *testing.T) {
	h := home(t)
	if err := os.MkdirAll(filepath.Join(h, "processes"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(h, "processes", "010-broken.json"), []byte(`{"command":"x","bogus":1}`), 0o600)
	_ = os.WriteFile(filepath.Join(h, "processes", "020-fine.json"), []byte(`{"command":"x"}`), 0o600)
	_ = os.WriteFile(filepath.Join(h, "processes", "README.md"), []byte("not a process"), 0o600)
	items, skipped, err := List(libitem.Process)
	if err != nil || len(items) != 1 || items[0].Name != "fine" {
		t.Fatalf("items = %+v %v", items, err)
	}
	if len(skipped) != 1 || skipped[0].Name != "010-broken" || !strings.Contains(skipped[0].Reason, "unknown field") {
		t.Fatalf("skipped = %+v", skipped)
	}
}

func TestProcessFileNameWinsOverADocumentThatDisagrees(t *testing.T) {
	h := home(t)
	if err := os.MkdirAll(filepath.Join(h, "processes"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Hand-edited: the file says it is process 030, the document says name other, order 99.
	_ = os.WriteFile(filepath.Join(h, "processes", "030-hand.json"), []byte(`{"name":"other","command":"x","order":99}`), 0o600)
	got, err := Get(libitem.Process, "hand")
	if err != nil {
		t.Fatal(err)
	}
	d, _ := libitem.ParseProcess(got.Doc)
	if d.Name != "hand" || d.OrderOr(0) != 30 {
		t.Fatalf("doc = %s", got.Doc)
	}
}
