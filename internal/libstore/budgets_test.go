package libstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
)

const budgetDoc = `{"name":"team","budgets":[{"provider":"anthropic","model":"claude-sonnet-5-5","scope":"day","tokens":1000000},{"provider":"openai","model":"gpt-5","scope":"session","tokens":50000}],"cycle_seconds":15,"warn":true}`

func TestBudgetInstallWritesTheGlobalSettingsAndExportsTheSameBytes(t *testing.T) {
	h := home(t)
	if err := os.WriteFile(filepath.Join(h, "settings.json"), []byte(`{"model":"gpt-5","ui":{"banner":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Install(libitem.Budget, []byte(budgetDoc), InstallOptions{Name: "team"})
	if err != nil || res.Replaced {
		t.Fatalf("install: %+v %v", res, err)
	}
	s, _ := config.LoadGlobal()
	if len(s.TokenBudgets) != 2 || s.TokenBudgets[0].Tokens != 1000000 || s.UI == nil || *s.UI.BudgetCycleSeconds != 15 || !*s.UI.BudgetWarn {
		t.Fatalf("settings = %+v ui %+v", s.TokenBudgets, s.UI)
	}
	// Other settings, in the same blocks, are kept.
	if s.Model != "gpt-5" || s.UI.Banner == nil || *s.UI.Banner {
		t.Fatalf("the rest of settings.json changed: %+v", s)
	}
	want, _ := libitem.Validate(libitem.Budget, []byte(budgetDoc))
	got, err := Get(libitem.Budget, "team")
	if err != nil || string(got.Doc) != string(want.Doc) || got.SHA256 != want.SHA256 {
		t.Fatalf("exported %q (%v), want %q", got.Doc, err, want.Doc)
	}
	if LocalName(libitem.Budget) != "team" {
		t.Errorf("local name = %q", LocalName(libitem.Budget))
	}
	// Asked for under another name, there is nothing: the host's set is "team".
	if _, err := Get(libitem.Budget, "other"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(other) = %v", err)
	}
}

func TestBudgetInstallReplacesTheWholeConfigurationOnlyWhenTold(t *testing.T) {
	home(t)
	if _, err := Install(libitem.Budget, []byte(budgetDoc), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	other := `{"name":"solo","budgets":[{"provider":"a","model":"m","scope":"month","tokens":9}]}`
	if _, err := Install(libitem.Budget, []byte(other), InstallOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v", err)
	}
	if s, _ := config.LoadGlobal(); len(s.TokenBudgets) != 2 {
		t.Fatal("a refused install changed the budgets")
	}
	res, err := Install(libitem.Budget, []byte(other), InstallOptions{Overwrite: true})
	if err != nil || !res.Replaced {
		t.Fatalf("overwrite: %+v %v", res, err)
	}
	s, _ := config.LoadGlobal()
	if len(s.TokenBudgets) != 1 || s.TokenBudgets[0].Provider != "a" {
		t.Fatalf("budgets after overwrite: %+v", s.TokenBudgets)
	}
	// The footer settings the new document does not name are cleared with the rest.
	if s.UI != nil && (s.UI.BudgetCycleSeconds != nil || s.UI.BudgetWarn != nil) {
		t.Fatalf("footer settings survived: %+v", s.UI)
	}
	if LocalName(libitem.Budget) != "solo" {
		t.Errorf("local name = %q", LocalName(libitem.Budget))
	}
	got, _ := Get(libitem.Budget, "solo")
	want, _ := libitem.Validate(libitem.Budget, []byte(other))
	if string(got.Doc) != string(want.Doc) {
		t.Fatalf("exported %q, want %q", got.Doc, want.Doc)
	}
}

func TestBudgetEmptySetClearsTheBudgetsAndWritesNoUIBlock(t *testing.T) {
	h := home(t)
	if _, err := Install(libitem.Budget, []byte(budgetDoc), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(libitem.Budget, []byte(`{"name":"none","budgets":[]}`), InstallOptions{Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(h, "settings.json"))
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(b, &raw)
	if _, ok := raw["token_budgets"]; ok {
		t.Errorf("token_budgets survived: %s", b)
	}
	// Nothing is configured, so there is no local item to export.
	if items, _, _ := List(libitem.Budget); len(items) != 0 {
		t.Errorf("items = %+v", items)
	}
}

func TestBudgetInstallRefusals(t *testing.T) {
	home(t)
	cases := map[string]string{
		`{"name":"x","budgets":[{"provider":"a","model":"m","scope":"week","tokens":1}]}`:                                                      "must be session, day or month",
		`{"name":"x","budgets":[{"provider":"a","model":"m","scope":"day","tokens":1},{"provider":"a","model":"m","scope":"day","tokens":2}]}`: "is already set",
		`{"name":"x","budgets":[],"warn":0.8}`: "must be true or false",
		`{"name":"x"}`:                         "budgets is required",
	}
	for doc, want := range cases {
		if _, err := Install(libitem.Budget, []byte(doc), InstallOptions{}); err == nil || !IsRefusal(err) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want a refusal naming %q", doc, err, want)
		}
	}
	if s, _ := config.LoadGlobal(); len(s.TokenBudgets) != 0 {
		t.Fatal("a refused install wrote budgets")
	}
}

func TestBudgetExportAsNamesTheSet(t *testing.T) {
	home(t)
	if _, err := ExportAs(libitem.Budget, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nothing configured: %v", err)
	}
	if _, err := Install(libitem.Budget, []byte(budgetDoc), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := ExportAs(libitem.Budget, "renamed")
	if err != nil || got.Name != "renamed" || !strings.Contains(string(got.Doc), `"name":"renamed"`) {
		t.Fatalf("ExportAs: %+v %v", got, err)
	}
	if _, err := ExportAs(libitem.Budget, "Bad Name"); err == nil || !IsRefusal(err) {
		t.Errorf("a bad name: %v", err)
	}
	// The host's own name is unchanged by an export.
	if LocalName(libitem.Budget) != "team" {
		t.Errorf("local name = %q", LocalName(libitem.Budget))
	}
}

func TestLocalNameDefaultsAndIgnoresADamagedFile(t *testing.T) {
	h := home(t)
	if LocalName(libitem.Budget) != DefaultSetName || LocalName(libitem.Provider) != DefaultSetName || LocalName(libitem.Rewrite) != "bash_rewrite" {
		t.Fatal("defaults")
	}
	_ = os.MkdirAll(filepath.Join(h, "library"), 0o700)
	_ = os.WriteFile(filepath.Join(h, "library", "names.json"), []byte(`{"budget":"Not A Valid Name"}`), 0o600)
	if LocalName(libitem.Budget) != DefaultSetName {
		t.Error("an invalid stored name was used")
	}
	_ = os.WriteFile(filepath.Join(h, "library", "names.json"), []byte(`{broken`), 0o600)
	if LocalName(libitem.Budget) != DefaultSetName {
		t.Error("a damaged file was used")
	}
}

// The project layer's budgets are never read.
func TestBudgetProjectSettingsAreNeverRead(t *testing.T) {
	home(t)
	proj := t.TempDir()
	_ = os.MkdirAll(filepath.Join(proj, ".vulnetix"), 0o755)
	_ = os.WriteFile(filepath.Join(proj, ".vulnetix", "settings.json"), []byte(`{"token_budgets":[{"provider":"a","model":"m","scope":"day","tokens":1}]}`), 0o600)
	if items, _, err := List(libitem.Budget); err != nil || len(items) != 0 {
		t.Fatalf("items = %+v %v", items, err)
	}
}
