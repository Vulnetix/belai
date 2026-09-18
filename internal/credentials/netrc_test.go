package credentials

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNetrcMultiMachineSelection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".netrc")
	data := `machine api.openai.com password openai-key
machine api.anthropic.com password anthropic-key
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write netrc: %v", err)
	}
	ns := &netrcStore{path: path}
	m, _, ok := ns.read("api.openai.com")
	if !ok {
		t.Fatalf("expected openai entry")
	}
	if m["api_key"] != "openai-key" {
		t.Fatalf("unexpected api_key: %q", m["api_key"])
	}

	m2, _, ok := ns.read("api.anthropic.com")
	if !ok {
		t.Fatalf("expected anthropic entry")
	}
	if m2["api_key"] != "anthropic-key" {
		t.Fatalf("unexpected api_key: %q", m2["api_key"])
	}
}

func TestNetrcDefaultOnlyAsFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".netrc")
	data := `machine api.openai.com password real-key
machine default password default-key
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write netrc: %v", err)
	}
	ns := &netrcStore{path: path}
	m, _, ok := ns.read("api.openai.com")
	if !ok {
		t.Fatalf("expected entry")
	}
	if m["api_key"] != "real-key" {
		t.Fatalf("expected machine entry to win, got %q", m["api_key"])
	}

	m2, _, ok := ns.read("unknown.host")
	if !ok {
		t.Fatalf("expected default fallback")
	}
	if m2["api_key"] != "default-key" {
		t.Fatalf("expected default key, got %q", m2["api_key"])
	}
}

func TestNetrcMacdefBodySkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".netrc")
	data := `machine api.openai.com password before-macdef
macdef upload
put file

machine api.openai.com password after-macdef
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write netrc: %v", err)
	}
	ns := &netrcStore{path: path}
	m, _, ok := ns.read("api.openai.com")
	if !ok {
		t.Fatalf("expected entry")
	}
	// The first machine entry should be used; the password inside macdef should not leak.
	if m["api_key"] != "before-macdef" {
		t.Fatalf("expected before-macdef, got %q", m["api_key"])
	}
}

func TestNetrcTokensSplitAcrossLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".netrc")
	data := `machine
api.openai.com
password
split-key
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write netrc: %v", err)
	}
	ns := &netrcStore{path: path}
	m, _, ok := ns.read("api.openai.com")
	if !ok {
		t.Fatalf("expected entry")
	}
	if m["api_key"] != "split-key" {
		t.Fatalf("expected split-key, got %q", m["api_key"])
	}
}

func TestNetrcMissingFileYieldsNoEntries(t *testing.T) {
	ns := &netrcStore{path: filepath.Join(t.TempDir(), "no-such-netrc")}
	m, _, ok := ns.read("api.openai.com")
	if ok {
		t.Fatalf("expected no entry for missing file")
	}
	if m != nil {
		t.Fatalf("expected nil map")
	}
}

func TestNetrcStoreErrors(t *testing.T) {
	ns := &netrcStore{}
	if err := ns.write(); err == nil {
		t.Fatalf("expected netrc write to error")
	}
	if err := ns.delete(); err == nil {
		t.Fatalf("expected netrc delete to error")
	}
}

func TestNetrcParsedPasswordRedacts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".netrc")
	data := `machine api.openai.com password secret-key
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write netrc: %v", err)
	}
	ns := &netrcStore{path: path}
	m, _, ok := ns.read("api.openai.com")
	if !ok {
		t.Fatalf("expected entry")
	}
	v := Value{Field: "api_key", Location: "~/.netrc", Source: SourceNetrc, Secret: true, value: m["api_key"]}
	if strings.Contains(fmt.Sprintf("%v", v), "secret-key") {
		t.Fatalf("Value formatting leaked netrc password")
	}
}

func TestNetrcLoginMapsToAccountID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".netrc")
	data := `machine api.cloudflare.com login cf-account password cf-key
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write netrc: %v", err)
	}
	ns := &netrcStore{path: path}
	m, _, ok := ns.read("api.cloudflare.com")
	if !ok {
		t.Fatalf("expected entry")
	}
	if m["account_id"] != "cf-account" {
		t.Fatalf("expected login to map to account_id, got %q", m["account_id"])
	}
	if m["api_key"] != "cf-key" {
		t.Fatalf("expected password to map to api_key, got %q", m["api_key"])
	}
}

func TestNetrcAccountOverridesLogin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".netrc")
	data := `machine api.cloudflare.com login cf-login account cf-account password cf-key
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write netrc: %v", err)
	}
	ns := &netrcStore{path: path}
	m, _, ok := ns.read("api.cloudflare.com")
	if !ok {
		t.Fatalf("expected entry")
	}
	if m["account_id"] != "cf-account" {
		t.Fatalf("expected account to win over login, got %q", m["account_id"])
	}
}
