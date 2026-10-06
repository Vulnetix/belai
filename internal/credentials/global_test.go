package credentials

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewGlobalResolverUsesOnlyTheUsersLayers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	// A project in the working directory with its own credentials file.
	proj := t.TempDir()
	t.Chdir(proj)
	if err := os.MkdirAll(filepath.Join(proj, ".vulnetix", "belai"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".vulnetix", "belai", "credentials.json"),
		[]byte(`{"version":1,"providers":{"openai":{"api_key":{"source":"inline","value":"from-the-project"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "")
	r, err := NewGlobalResolver()
	if err != nil {
		t.Fatal(err)
	}
	if v, _, ok := r.Lookup("openai", "api_key"); ok && strings.Contains(v, "from-the-project") {
		t.Fatal("the project's credentials file was read")
	}
	// A key stored into the user file resolves, and lands in the user's file at 0600.
	if err := r.Store("openai", "api_key", "sk-user-key", SourceUserFile); err != nil {
		t.Fatal(err)
	}
	if v, origin, ok := r.Lookup("openai", "api_key"); !ok || v != "sk-user-key" || !strings.Contains(origin, "credentials.json") {
		t.Fatalf("lookup = %q %q %v", v, origin, ok)
	}
	p := filepath.Join(home, "credentials.json")
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("credentials.json mode = %v", fi.Mode().Perm())
	}
	// Nothing was written to the project.
	b, _ := os.ReadFile(filepath.Join(proj, ".vulnetix", "belai", "credentials.json"))
	if strings.Contains(string(b), "sk-user-key") {
		t.Error("the key was written to the project's file")
	}
}
