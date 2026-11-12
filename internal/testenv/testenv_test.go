package testenv

import (
	"os"
	"testing"
)

func TestIsolateSetsEveryVariableToItsOwnDir(t *testing.T) {
	e := Isolate(t)
	for k, want := range map[string]string{
		"HOME":             e.Home,
		"BELAI_HOME":       e.BelaiHome,
		"BELAI_MODELS_DIR": e.ModelsDir,
		"VULNETIX_WEB_URL": OfflineWebURL,
	} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	seen := map[string]bool{}
	for _, d := range []string{e.Home, e.BelaiHome, e.ModelsDir, e.WorkDir} {
		if seen[d] {
			t.Fatalf("directory %s is shared between two roles: %+v", d, e)
		}
		seen[d] = true
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Fatalf("%s is not a directory: %v", d, err)
		}
	}
}

func TestIsolateRestoresTheEnvironment(t *testing.T) {
	const key = "VULNETIX_WEB_URL"
	t.Setenv(key, "https://example.invalid")
	t.Run("inner", func(t *testing.T) {
		Isolate(t)
		if got := os.Getenv(key); got != OfflineWebURL {
			t.Fatalf("%s = %q inside the subtest", key, got)
		}
	})
	if got := os.Getenv(key); got != "https://example.invalid" {
		t.Fatalf("%s = %q after the subtest, want it restored", key, got)
	}
}
