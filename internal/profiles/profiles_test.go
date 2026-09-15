package profiles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestValidateRejectsInvalid(t *testing.T) {
	cases := []Profile{
		{Name: "", Content: "x"},
		{Name: "p", Content: ""},
		{Name: "///", Content: "x"},
	}
	for _, p := range cases {
		if err := Validate(p); err == nil {
			t.Fatalf("expected invalid profile to be rejected: %+v", p)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	want := Profile{Name: "security-expert", Content: "You are a security expert."}
	path, err := Save(want)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if filepath.Ext(path) != ".json" {
		t.Fatalf("profile file should be .json, got %q", path)
	}
	got, err := Load("security-expert")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip mismatch:\n want=%+v\n  got=%+v", want, got)
	}
}

func TestSwitch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if _, err := Save(Profile{Name: "go-expert", Content: "expert at Go"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	p, err := Switch("go-expert")
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if p.Name != "go-expert" || p.Content != "expert at Go" {
		t.Fatalf("Switch = %+v", p)
	}
	if _, err := Switch("missing"); err == nil {
		t.Fatalf("expected switch to missing profile to fail")
	}
}

func TestListSorted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	for _, n := range []string{"beta", "alpha"} {
		if _, err := Save(Profile{Name: n, Content: n}); err != nil {
			t.Fatalf("Save(%s): %v", n, err)
		}
	}
	profiles, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	names := []string{profiles[0].Name, profiles[1].Name}
	if !reflect.DeepEqual(names, []string{"alpha", "beta"}) {
		t.Fatalf("order = %v", names)
	}
}

func TestWizardWritesWellFormedProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	p, err := Run(WizardInput{Name: "code-reviewer", Content: "review code for bugs"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if p.Name != "code-reviewer" {
		t.Fatalf("wizard returned %+v", p)
	}

	// The file must be well-formed JSON and load back identically.
	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "code-reviewer.json"))
	if err != nil {
		t.Fatalf("read profile file: %v", err)
	}
	var parsed Profile
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("profile file is not well-formed JSON: %v", err)
	}
	if parsed.Content != "review code for bugs" {
		t.Fatalf("profile content = %q", parsed.Content)
	}
}

func TestWizardRejectsInvalidInput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := Run(WizardInput{Name: "", Content: "x"}); err == nil {
		t.Fatalf("expected wizard to reject empty name")
	}
}

func TestLoadRejectsInvalidProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{"name":"broken","content":""}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load("broken"); err == nil {
		t.Fatal("expected invalid profile to be rejected on load")
	}
}
