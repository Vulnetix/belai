package processlib

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func put(t *testing.T, dir, name, body string) {
	t.Helper()
	d := filepath.Join(dir, ".vulnetix", "processes")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func names(es []Entry) string {
	var out []string
	for _, e := range es {
		kind := "sh"
		if e.Structured() {
			kind = "json"
		}
		out = append(out, fmt.Sprintf("%s:%s:%d:%v", e.Name, kind, e.Order, e.Enabled))
	}
	return strings.Join(out, ",")
}

func TestLoadReadsBothFormsAndStraysTheBrokenOnes(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "010-legacy.sh", "python3 -m http.server\n")
	put(t, dir, "020-web.json", `{"command":"python3","args":["-m","http.server"],"options":[{"name":"--bind","value":"127.0.0.1"}]}`+"\n")
	put(t, dir, "_030-off.json", `{"command":"sleep","args":["30"]}`+"\n")
	put(t, dir, "040-broken.json", `{"command":`)
	put(t, dir, "050-unknown.json", `{"command":"x","bogus":1}`)
	put(t, dir, "060-nocmd.json", `{"name":"nocmd"}`)
	l, err := Load(config.ScopeProject, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(l.Entries); got != "legacy:sh:10:true,web:json:20:true,off:json:30:false" {
		t.Fatalf("entries = %s", got)
	}
	if fmt.Sprint(l.Strays) != "[040-broken.json 050-unknown.json 060-nocmd.json]" {
		t.Fatalf("strays = %v", l.Strays)
	}
	web := l.Entries[1]
	if web.Spec == nil || web.Spec.Name != "web" || web.Command != "python3 --bind 127.0.0.1 -m http.server" {
		t.Fatalf("web = %+v %+v", web.Spec, web.Command)
	}
	if !strings.Contains(web.Body, `"command":"python3"`) {
		t.Errorf("Body is not the file: %q", web.Body)
	}
	if legacy := l.Entries[0]; legacy.Spec != nil || legacy.Command != "python3 -m http.server" || legacy.Structured() {
		t.Fatalf("legacy = %+v", legacy)
	}
}

func TestMergeEnabledFilterAndReorderKeepTheStructuredForm(t *testing.T) {
	global := t.TempDir()
	t.Setenv("BELAI_HOME", global)
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(global, "processes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(global, "processes", "010-api.json"), []byte(`{"command":"api"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	put(t, work, "010-web.json", `{"command":"web"}`+"\n")
	put(t, work, "_020-off.json", `{"command":"off"}`+"\n")
	put(t, work, "030-old.sh", "old\n")
	g, _ := Load(config.ScopeGlobal, work)
	p, _ := Load(config.ScopeProject, work)
	merged := Merge(g.Entries, p.Entries)
	if got := names(merged); got != "api:json:10:true,web:json:10:true,off:json:20:false,old:sh:30:true" {
		t.Fatalf("merged = %s", got)
	}
	for _, e := range merged {
		if e.Structured() != (e.Name != "old") || (e.Structured() && e.Spec.Command == "") {
			t.Errorf("%s lost its form: %+v", e.Name, e)
		}
	}
	en := Enabled(merged)
	if names(en) != "api:json:10:true,web:json:10:true,old:sh:30:true" || en[0].Spec == nil || en[1].Spec == nil || en[2].Spec != nil {
		t.Fatalf("enabled = %s", names(en))
	}
	f := Filter(merged, "web")
	if len(f) != 1 || f[0].Spec == nil || f[0].Name != "web" {
		t.Fatalf("filter = %+v", f)
	}
	moved, err := Reorder(config.ScopeProject, work, p.Entries, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(moved); got != "old:sh:10:true,web:json:20:true,off:json:30:false" {
		t.Fatalf("reordered = %s", got)
	}
	if moved[1].Spec == nil || moved[2].Spec == nil || moved[0].Spec != nil {
		t.Error("a reorder lost or invented a structured form")
	}
	// The files kept their own extensions.
	l, _ := Load(config.ScopeProject, work)
	if got := names(l.Entries); got != "old:sh:10:true,web:json:20:true,off:json:30:false" {
		t.Fatalf("after reload = %s", got)
	}
}

func TestSetEnabledAndDeleteOnAStructuredEntry(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "010-web.json", `{"command":"web"}`+"\n")
	l, _ := Load(config.ScopeProject, dir)
	off, err := SetEnabled(l.Entries[0], false)
	if err != nil || off.Enabled || off.Spec == nil || filepath.Base(off.Path) != "_010-web.json" {
		t.Fatalf("SetEnabled: %+v %v", off, err)
	}
	// The JSON itself did not change: the file name carries the state.
	b, _ := os.ReadFile(off.Path)
	if string(b) != `{"command":"web"}`+"\n" {
		t.Errorf("body = %q", b)
	}
	if err := Delete(off); err != nil {
		t.Fatal(err)
	}
	if l, _ := Load(config.ScopeProject, dir); len(l.Entries) != 0 {
		t.Fatalf("left %+v", l.Entries)
	}
}

func TestUpdateValidatesAStructuredBody(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "010-web.json", `{"command":"web"}`+"\n")
	l, _ := Load(config.ScopeProject, dir)
	if _, err := Update(l.Entries[0], `{"command":"web","bogus":true}`); err == nil {
		t.Fatal("an invalid body was written")
	}
	if b, _ := os.ReadFile(l.Entries[0].Path); string(b) != `{"command":"web"}`+"\n" {
		t.Fatalf("a refused update changed the file: %q", b)
	}
	up, err := Update(l.Entries[0], `{"command":"web2","args":["-x"]}`)
	if err != nil || up.Command != "web2 -x" || up.Spec == nil || up.Spec.Command != "web2" {
		t.Fatalf("Update: %+v %v", up, err)
	}
	// A legacy entry's body is still any shell text.
	put(t, dir, "020-old.sh", "old\n")
	l, _ = Load(config.ScopeProject, dir)
	old := l.Entries[1]
	if up, err := Update(old, "new && command"); err != nil || up.Command != "new && command" || up.Spec != nil {
		t.Fatalf("legacy Update: %+v %v", up, err)
	}
}

func TestPutDocReplacesALegacyEntryAndKeepsTheDocument(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	if _, err := CreateUnique(config.ScopeGlobal, "", "web --old"); err != nil {
		t.Fatal(err)
	}
	doc := []byte(`{"name":"web","command":"web","args":[],"options":[{"name":"--new"}],"enabled":false,"order":50}`)
	e, err := PutDoc(config.ScopeGlobal, "", doc)
	if err != nil {
		t.Fatal(err)
	}
	if e.Order != 50 || e.Enabled || !e.Structured() || filepath.Base(e.Path) != "_050-web.json" || e.Command != "web --new" {
		t.Fatalf("entry = %+v", e)
	}
	l, _ := Load(config.ScopeGlobal, "")
	if len(l.Entries) != 1 || len(l.Strays) != 0 {
		t.Fatalf("the legacy file was not replaced: %+v strays %v", l.Entries, l.Strays)
	}
	// The file is readable JSON, and holds the same document: an empty args list the
	// library sent stays, so the host exports the library's own bytes.
	b, _ := os.ReadFile(e.Path)
	if !strings.Contains(string(b), "\n  \"args\": []") {
		t.Errorf("file = %s", b)
	}
	if fi, _ := os.Stat(e.Path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	// Order 0 keeps an existing entry's slot.
	e2, err := PutDoc(config.ScopeGlobal, "", []byte(`{"name":"web","command":"web2","enabled":false}`))
	if err != nil || e2.Order != 50 {
		t.Fatalf("replace: %+v %v", e2, err)
	}
	for _, bad := range []string{`{"name":"Web","command":"x"}`, `{"name":"web"}`, `not json`} {
		if _, err := PutDoc(config.ScopeGlobal, "", []byte(bad)); err == nil {
			t.Errorf("%s was written", bad)
		}
	}
	if _, err := PutDoc(config.ScopeGlobal, "", []byte(`{"name":"my.web","command":"x"}`)); err == nil {
		t.Error("a name that is not a slug was written")
	}
}

func TestParseStructuredUsesTheFileNameForWhatItCanSay(t *testing.T) {
	d, err := ParseStructured(`{"command":"x","enabled":true}`, "web", 10, false)
	if err != nil || d.IsEnabled() || d.Name != "web" {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := ParseStructured(`{}`, "web", 10, true); err == nil {
		t.Error("an empty document parsed")
	}
}
