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

const providerDoc = `{"name":"mine","providers":{"my-llm":{"base_url":"https://llm.example.com/v1","api":"openai-chat","auth":"bearer","api_key_env":"MY_LLM_KEY","models":[{"id":"m1","name":"M one","context_window":128000,"max_tokens":4096,"images":true}]}},"firewall":{"enabled":true,"active":"gw","instances":{"gw":{"adapter":"custom","url":"https://gw.example.com/{provider}","mode":"header","header":"X-Gw-Key","providers":["my-llm"]}}}}`

func TestProviderInstallWritesTheSettingsAndExportsTheSameBytes(t *testing.T) {
	h := home(t)
	if err := os.WriteFile(filepath.Join(h, "settings.json"), []byte(`{"model":"gpt-5","provider_labels":{"my-llm":"My LLM"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Install(libitem.Provider, []byte(providerDoc), InstallOptions{Name: "mine"})
	if err != nil || res.Replaced {
		t.Fatalf("install: %+v %v", res, err)
	}
	s, _ := config.LoadGlobal()
	p := s.Providers["my-llm"]
	if p.BaseURL != "https://llm.example.com/v1" || p.APIKeyEnv != "MY_LLM_KEY" || len(p.Models) != 1 || s.Firewall == nil || s.Firewall.Active != "gw" || s.Model != "gpt-5" {
		t.Fatalf("settings = %+v", s)
	}
	want, _ := libitem.Validate(libitem.Provider, []byte(providerDoc))
	got, err := Get(libitem.Provider, "mine")
	if err != nil || string(got.Doc) != string(want.Doc) || got.SHA256 != want.SHA256 {
		t.Fatalf("exported %q (%v), want %q", got.Doc, err, want.Doc)
	}
	if LocalName(libitem.Provider) != "mine" {
		t.Errorf("local name = %q", LocalName(libitem.Provider))
	}
	// The file holds no key, and kept what it held.
	b, _ := os.ReadFile(filepath.Join(h, "settings.json"))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil || string(raw["model"]) != `"gpt-5"` || !strings.Contains(string(raw["provider_labels"]), "My LLM") {
		t.Fatalf("settings.json = %s", b)
	}
}

func TestProviderInstallReplacesTheWholeConfigurationOnlyWhenTold(t *testing.T) {
	home(t)
	if _, err := Install(libitem.Provider, []byte(providerDoc), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	other := `{"name":"solo","providers":{"other-llm":{"base_url":"https://o.example.com","api":"anthropic-messages"}}}`
	if _, err := Install(libitem.Provider, []byte(other), InstallOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v", err)
	}
	if s, _ := config.LoadGlobal(); len(s.Providers) != 1 || s.Providers["my-llm"].BaseURL == "" {
		t.Fatal("a refused install changed the providers")
	}
	res, err := Install(libitem.Provider, []byte(other), InstallOptions{Overwrite: true})
	if err != nil || !res.Replaced {
		t.Fatalf("overwrite: %+v %v", res, err)
	}
	s, _ := config.LoadGlobal()
	if _, kept := s.Providers["my-llm"]; kept || s.Providers["other-llm"].BaseURL == "" {
		t.Fatalf("providers after overwrite: %+v", s.Providers)
	}
	// The firewall the new document does not name goes with the rest.
	if s.Firewall != nil {
		t.Fatalf("a firewall survived: %+v", s.Firewall)
	}
	got, _ := Get(libitem.Provider, "solo")
	want, _ := libitem.Validate(libitem.Provider, []byte(other))
	if string(got.Doc) != string(want.Doc) {
		t.Fatalf("exported %q, want %q", got.Doc, want.Doc)
	}
}

func TestProviderInstallRefusals(t *testing.T) {
	home(t)
	cases := map[string]string{
		`{"name":"x","providers":{"openai":{"base_url":"https://x.example.com","api":"openai-chat"}}}`:                   "collides with a built-in provider",
		`{"name":"x","providers":{"a":{"base_url":"https://x.example.com","api":"openai-chat","api_key":"sk-live"}}}`:    `unknown field "api_key"`,
		`{"name":"x","providers":{"a":{"base_url":"https://k@x.example.com","api":"openai-chat"}}}`:                      "must not carry credentials",
		`{"name":"x","providers":{"a":{"base_url":"https://x.example.com","api":"openai-chat","api_key_env":"sk-abc"}}}`: "never a key",
		`{"name":"x","providers":{"1st":{"base_url":"https://x.example.com","api":"openai-chat"}}}`:                      "invalid provider name",
		`{"name":"x","providers":{},"firewall":{"active":"nope"}}`:                                                       "not a configured firewall",
	}
	for doc, want := range cases {
		if _, err := Install(libitem.Provider, []byte(doc), InstallOptions{}); err == nil || !IsRefusal(err) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want a refusal naming %q", doc, err, want)
		}
	}
	if s, _ := config.LoadGlobal(); len(s.Providers) != 0 {
		t.Fatal("a refused install wrote providers")
	}
}

// An install is validated against the whole settings file it would write: a file
// Belai already cannot load is not made worse, and nothing is written.
func TestProviderInstallNeverLeavesAFileBelaiCannotLoad(t *testing.T) {
	h := home(t)
	broken := `{"routing":{"kind":"sideways"}}`
	if err := os.WriteFile(filepath.Join(h, "settings.json"), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Install(libitem.Provider, []byte(`{"name":"x","providers":{"a":{"base_url":"https://x.example.com","api":"openai-chat"}}}`), InstallOptions{Overwrite: true})
	if err == nil || !IsRefusal(err) || !strings.Contains(err.Error(), "would not be valid after this install") || !strings.Contains(err.Error(), "routing.kind") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(h, "settings.json")); string(b) != broken {
		t.Fatalf("settings.json was rewritten: %s", b)
	}
}

func TestProviderEmptySetIsNotAnItem(t *testing.T) {
	home(t)
	if items, _, _ := List(libitem.Provider); len(items) != 0 {
		t.Fatalf("items = %+v", items)
	}
	if _, err := Install(libitem.Provider, []byte(`{"name":"empty","providers":{}}`), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if items, _, _ := List(libitem.Provider); len(items) != 0 {
		t.Fatalf("an empty set is an item: %+v", items)
	}
}

func TestProviderExportAsNamesTheSet(t *testing.T) {
	home(t)
	if _, err := Install(libitem.Provider, []byte(providerDoc), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := ExportAs(libitem.Provider, "copy")
	if err != nil || got.Name != "copy" || !strings.Contains(string(got.Doc), `"name":"copy"`) {
		t.Fatalf("ExportAs: %+v %v", got, err)
	}
	// Nothing in an exported provider document is a key.
	for _, bad := range []string{"sk-", "api_key\":", "Bearer"} {
		if strings.Contains(string(got.Doc), bad) {
			t.Errorf("the document holds %q", bad)
		}
	}
}

func TestProviderProjectSettingsAreNeverRead(t *testing.T) {
	home(t)
	proj := t.TempDir()
	_ = os.MkdirAll(filepath.Join(proj, ".vulnetix"), 0o755)
	_ = os.WriteFile(filepath.Join(proj, ".vulnetix", "settings.json"), []byte(`{"providers":{"evil":{"base_url":"https://evil.example.com","api":"openai-chat"}}}`), 0o600)
	if items, _, err := List(libitem.Provider); err != nil || len(items) != 0 {
		t.Fatalf("items = %+v %v", items, err)
	}
}
