package plugins

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// A plugin name is at most 40 characters and may not start with a hyphen.
func TestPluginNameBoundaries(t *testing.T) {
	for name, want := range map[string]bool{
		strings.Repeat("a", 40):        true,
		strings.Repeat("a", 41):        false,
		"-leading":                     false,
		"a-b-1":                        true,
		"0day":                         true,
		"with_underscore":              false,
		"belai":                        false,
		"builtin":                      false,
		"user":                         false,
		"belai-tools":                  true,
		strings.Repeat("a", 39) + "-":  true,
		strings.Repeat("a", 40) + "-b": false,
	} {
		if got := ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, got, want)
		}
	}
}

// A prompt name is at most 64 characters, and a file of exactly 64 KiB is read.
func TestPromptNameAndSizeBoundaries(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, strings.Repeat("a", 64)+".md", "ok")
	write(t, dir, strings.Repeat("a", 65)+".md", "too long a name")
	write(t, dir, "-lead.md", "leading hyphen")
	write(t, dir, "edge.md", strings.Repeat("a", 64*1024))
	write(t, dir, "notes.txt", "not markdown")
	got := map[string]bool{}
	for _, p := range readPrompts(dir) {
		got[p.Name] = true
	}
	if len(got) != 2 || !got[strings.Repeat("a", 64)] || !got["edge"] {
		t.Fatalf("prompts read = %v, want the 64 character name and the 64 KiB file only", got)
	}
}

// Installing a name that is already installed is refused, and nothing changes.
func TestInstallRefusesAnInstalledName(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	src := samplePlugin(t)
	if _, err := Install(context.Background(), src, yes); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), src, yes); err == nil {
		t.Fatal("a second install of the same name succeeded")
	}
	recs, err := List()
	if err != nil || len(recs) != 1 {
		t.Fatalf("registry after the refused install: %v, %v", recs, err)
	}
}

// TestPluginsPageStatesTheRules pins the name and size rules the page states.
func TestPluginsPageStatesTheRules(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/plugins.md")), " ")
	for _, want := range []string{
		"(at most 40, not starting with a hyphen), and not `belai`, `user` or `builtin`",
		"at most 64, not starting with a hyphen",
		"larger than 64 KiB (exactly 64 KiB is read)",
		"Installing a plugin whose name is already installed is refused",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/plugins.md does not say %q", want)
		}
	}
	for _, key := range []string{"skills", "hooks", "prompts", "agents"} {
		if !strings.Contains(doc, "`"+key+"`") {
			t.Errorf("docs/plugins.md does not document the manifest key %q", key)
		}
	}
}

// TestManifestKeysAreAllDocumented keeps the manifest table equal to the keys
// the strict decoder accepts.
func TestManifestKeysAreAllDocumented(t *testing.T) {
	doc := docparity.Read(t, "docs/plugins.md")
	typ := reflect.TypeOf(Manifest{})
	for i := 0; i < typ.NumField(); i++ {
		key := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if key == "" {
			t.Fatalf("Manifest.%s has no json tag", typ.Field(i).Name)
		}
		if !strings.Contains(doc, "`"+key+"`") {
			t.Errorf("docs/plugins.md does not document the manifest key %q", key)
		}
	}
}
