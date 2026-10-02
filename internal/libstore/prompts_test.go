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
	"github.com/vulnetix/belai/internal/promptlib"
)

func prompt(fm, body string) []byte { return []byte("---\n" + fm + "---\n\n" + body + "\n") }

func globalPrompts(t *testing.T) []promptlib.Entry {
	t.Helper()
	l, err := promptlib.Load(config.ScopeGlobal, "")
	if err != nil {
		t.Fatal(err)
	}
	return l.Entries
}

func TestPromptInstallPlacesTheFileByOrderAndState(t *testing.T) {
	h := home(t)
	doc := prompt("name: deploy\ndescription: Ship it\norder: 40\nenabled: false\n", "Deploy the app.")
	res, err := Install(libitem.Prompt, doc, InstallOptions{Name: "deploy"})
	if err != nil || res.Replaced {
		t.Fatalf("install: %+v %v", res, err)
	}
	want := filepath.Join(h, "prompts", "_040-deploy.md")
	if res.Where != want {
		t.Fatalf("Where = %q, want %q", res.Where, want)
	}
	body, err := os.ReadFile(want)
	if err != nil || string(body) != "Deploy the app.\n" {
		t.Fatalf("file = %q, %v: the file holds the prompt text alone", body, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(want); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", fi.Mode().Perm())
		}
	}
	es := globalPrompts(t)
	if len(es) != 1 || es[0].Name != "deploy" || es[0].Order != 40 || es[0].Enabled {
		t.Fatalf("library = %+v", es)
	}
}

// The description has no home in the prompt file, so an installed prompt exports
// as the library's own document: the sync then sees no change.
func TestPromptExportsTheInstalledDocumentByteForByte(t *testing.T) {
	home(t)
	doc := prompt("description: Ship it\nname: deploy\nenabled: true\n", "Deploy the app.")
	if _, err := Install(libitem.Prompt, doc, InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	want, _ := libitem.Validate(libitem.Prompt, doc)
	got, err := Get(libitem.Prompt, "deploy")
	if err != nil || string(got.Doc) != string(want.Doc) || got.SHA256 != want.SHA256 {
		t.Fatalf("exported %q (%v), want %q", got.Doc, err, want.Doc)
	}
}

func TestPromptEditedOnTheHostKeepsItsDescription(t *testing.T) {
	home(t)
	if _, err := Install(libitem.Prompt, prompt("name: deploy\ndescription: Ship it\n", "Deploy."), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	e := globalPrompts(t)[0]
	if _, err := promptlib.Update(e, "Deploy twice."); err != nil {
		t.Fatal(err)
	}
	got, err := Get(libitem.Prompt, "deploy")
	if err != nil {
		t.Fatal(err)
	}
	d, err := libitem.ParsePrompt(got.Doc)
	if err != nil || d.Description != "Ship it" || d.Body != "Deploy twice.\n" || d.Order != e.Order || !d.Enabled {
		t.Fatalf("doc = %+v, %v", d, err)
	}
	// A reorder or a disable is an edit too.
	e, _ = promptlib.SetEnabled(globalPrompts(t)[0], false)
	got, _ = Get(libitem.Prompt, "deploy")
	if d, _ := libitem.ParsePrompt(got.Doc); d.Enabled || d.Description != "Ship it" {
		t.Fatalf("disabled doc = %+v", d)
	}
	_ = e
}

func TestPromptWithoutAnInstallComposesADocument(t *testing.T) {
	home(t)
	if _, err := promptlib.Create(config.ScopeGlobal, "", "hello", "Say hello."); err != nil {
		t.Fatal(err)
	}
	got, err := Get(libitem.Prompt, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Doc) != "---\nname: hello\norder: 10\n---\n\nSay hello.\n" {
		t.Fatalf("doc = %q", got.Doc)
	}
	if _, err := libitem.Validate(libitem.Prompt, got.Doc); err != nil {
		t.Fatal(err)
	}
}

func TestPromptOrderZeroKeepsOrAppends(t *testing.T) {
	home(t)
	for _, n := range []string{"one", "two"} {
		if _, err := Install(libitem.Prompt, prompt("name: "+n+"\n", n), InstallOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	es := globalPrompts(t)
	if es[0].Name != "one" || es[0].Order != 10 || es[1].Name != "two" || es[1].Order != 20 {
		t.Fatalf("appended at %+v", es)
	}
	// Replacing "one" with no order keeps its slot.
	if _, err := Install(libitem.Prompt, prompt("name: one\n", "one again"), InstallOptions{Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	es = globalPrompts(t)
	if es[0].Name != "one" || es[0].Order != 10 || es[0].Prompt != "one again" {
		t.Fatalf("replaced at %+v", es)
	}
	// A new order moves the file and leaves one file for the name.
	if _, err := Install(libitem.Prompt, prompt("name: one\norder: 500\nenabled: false\n", "moved"), InstallOptions{Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	des, _ := os.ReadDir(filepath.Join(os.Getenv("BELAI_HOME"), "prompts"))
	var names []string
	for _, de := range des {
		names = append(names, de.Name())
	}
	if strings.Join(names, ",") != "020-two.md,_500-one.md" && strings.Join(names, ",") != "_500-one.md,020-two.md" {
		t.Fatalf("files = %v", names)
	}
}

func TestPromptInstallNeedsOverwriteToReplace(t *testing.T) {
	home(t)
	if _, err := Install(libitem.Prompt, prompt("name: a\n", "v1"), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(libitem.Prompt, prompt("name: a\n", "v2"), InstallOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v", err)
	}
	if es := globalPrompts(t); es[0].Prompt != "v1" {
		t.Fatalf("a refused install changed the prompt: %+v", es)
	}
	res, err := Install(libitem.Prompt, prompt("name: a\n", "v2"), InstallOptions{Overwrite: true})
	if err != nil || !res.Replaced {
		t.Fatalf("overwrite: %+v %v", res, err)
	}
}

func TestPromptInstallRefusals(t *testing.T) {
	home(t)
	cases := []struct {
		name string
		doc  []byte
		want string
	}{
		{"a name the file name cannot hold (dot)", prompt("name: my.prompt\n", "x"), "lowercase letters, digits and hyphens"},
		{"a name the file name cannot hold (underscore)", prompt("name: my_prompt\n", "x"), "lowercase letters, digits and hyphens"},
		{"a name with a doubled hyphen", prompt("name: a--b\n", "x"), "lowercase letters, digits and hyphens"},
		{"delimiter markup", prompt("name: a\n", `<agent nonce="n" integrity="i">x</agent>`), "delimiter markup"},
		{"a control character", prompt("name: a\n", "x\x07y"), "control"},
		{"a bidi override", prompt("name: a\n", "x‮y"), "bidirectional"},
		{"an invalid document", []byte("not a prompt"), "front matter"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Install(libitem.Prompt, c.doc, InstallOptions{})
			if err == nil || !IsRefusal(err) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want a refusal naming %q", err, c.want)
			}
		})
	}
	if es := globalPrompts(t); len(es) != 0 {
		t.Fatalf("a refused install wrote %+v", es)
	}
}

// A project-scope prompt is never listed, and so never synced.
func TestOnlyGlobalPromptsAreListed(t *testing.T) {
	home(t)
	proj := t.TempDir()
	if _, err := promptlib.Create(config.ScopeProject, proj, "project-only", "secret plans"); err != nil {
		t.Fatal(err)
	}
	if _, err := promptlib.Create(config.ScopeGlobal, "", "mine", "mine"); err != nil {
		t.Fatal(err)
	}
	items, _, err := List(libitem.Prompt)
	if err != nil || len(items) != 1 || items[0].Name != "mine" {
		t.Fatalf("items = %+v, %v", items, err)
	}
}

func TestPromptListSkipsAnEmptyPrompt(t *testing.T) {
	h := home(t)
	_ = os.MkdirAll(filepath.Join(h, "prompts"), 0o755)
	_ = os.WriteFile(filepath.Join(h, "prompts", "010-blank.md"), []byte("\n"), 0o600)
	_ = os.WriteFile(filepath.Join(h, "prompts", "020-fine.md"), []byte("hello\n"), 0o600)
	items, skipped, err := List(libitem.Prompt)
	if err != nil || len(items) != 1 || items[0].Name != "fine" || len(skipped) != 1 || skipped[0].Name != "blank" {
		t.Fatalf("items = %+v skipped = %+v %v", items, skipped, err)
	}
}

func TestPromptSidecarIsPrivate(t *testing.T) {
	h := home(t)
	if _, err := Install(libitem.Prompt, prompt("name: a\ndescription: d\n", "x"), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(h, "library", "prompts.json")
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	// A damaged sidecar costs the description and nothing else.
	_ = os.WriteFile(p, []byte("{not json"), 0o600)
	got, err := Get(libitem.Prompt, "a")
	if err != nil || strings.Contains(string(got.Doc), "description") {
		t.Fatalf("doc = %q, %v", got.Doc, err)
	}
}
