package libstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
)

const mcpDoc = `{"name":"Acme","command":"acme-mcp","env":{"ACME_TOKEN":"cred:token"},"secrets":{"token":"ACME_VAULT"}}`

func TestMCPInstallWritesTheEntryAndExportsTheSameBytes(t *testing.T) {
	h := home(t)
	if err := os.WriteFile(filepath.Join(h, "settings.json"), []byte(`{"model":"gpt-5"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Install(libitem.MCP, []byte(mcpDoc), InstallOptions{Name: "Acme"})
	if err != nil || res.Replaced {
		t.Fatalf("install: %+v %v", res, err)
	}
	s, _ := config.LoadGlobal()
	srv := s.MCP.Servers["Acme"]
	if srv.Command != "acme-mcp" || srv.Env["ACME_TOKEN"] != "cred:token" || srv.Secrets["token"] != "ACME_VAULT" || s.Model != "gpt-5" {
		t.Fatalf("settings = %+v", s)
	}
	want, _ := libitem.Validate(libitem.MCP, []byte(mcpDoc))
	got, err := Get(libitem.MCP, "Acme")
	if err != nil || string(got.Doc) != string(want.Doc) || got.SHA256 != want.SHA256 {
		t.Fatalf("exported %q (%v), want %q", got.Doc, err, want.Doc)
	}
	if b, _ := os.ReadFile(filepath.Join(h, "settings.json")); strings.Contains(string(b), "secret-value") {
		t.Fatal("no value is ever written")
	}
}

// The library folds case: installing acme over Acme is the same item and needs the
// request's say-so; with it the host's spelling follows the library's.
func TestMCPInstallFoldsCaseAndReplacesOnlyWhenTold(t *testing.T) {
	home(t)
	if _, err := Install(libitem.MCP, []byte(mcpDoc), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	other := `{"name":"acme","command":"other"}`
	if _, err := Install(libitem.MCP, []byte(other), InstallOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v", err)
	}
	if s, _ := config.LoadGlobal(); s.MCP.Servers["Acme"].Command != "acme-mcp" || len(s.MCP.Servers) != 1 {
		t.Fatal("a refused install changed the servers")
	}
	res, err := Install(libitem.MCP, []byte(other), InstallOptions{Overwrite: true})
	if err != nil || !res.Replaced {
		t.Fatalf("replace: %+v %v", res, err)
	}
	s, _ := config.LoadGlobal()
	if _, ok := s.MCP.Servers["Acme"]; ok || s.MCP.Servers["acme"].Command != "other" || len(s.MCP.Servers) != 1 {
		t.Fatalf("servers = %+v", s.MCP.Servers)
	}
}

// A hand-written entry the library's rules refuse is listed as skipped with the
// reason, never silently dropped and never sent; the built-in name is never listed.
func TestMCPListSkipsAndExplainsStrays(t *testing.T) {
	h := home(t)
	settings := `{"mcp":{"servers":{
		"good":{"command":"g"},
		"stray":{"command":"s","env":{"API_TOKEN":"literal"}},
		"clef":{"command":"ignored"}}}}`
	if err := os.WriteFile(filepath.Join(h, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	items, skipped, err := List(libitem.MCP)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "good" {
		t.Fatalf("items = %+v", items)
	}
	if len(skipped) != 1 || skipped[0].Name != "stray" || !strings.Contains(skipped[0].Reason, "looks like a secret") {
		t.Fatalf("skipped = %+v", skipped)
	}
}

func TestMCPInstallRefusesTheBuiltInName(t *testing.T) {
	home(t)
	if _, err := Install(libitem.MCP, []byte(`{"name":"clef","command":"c"}`), InstallOptions{}); !IsRefusal(err) {
		t.Fatalf("err = %v", err)
	}
}
