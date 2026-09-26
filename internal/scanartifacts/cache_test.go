package scanartifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// cacheHome points config.GlobalDir at a throwaway directory so the global
// scan-cache is isolated per test.
func cacheHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("BELAI_HOME", dir)
	return dir
}

func TestCachePathUsesGlobalDir(t *testing.T) {
	home := cacheHome(t)
	got, err := CachePath("some/workdir")
	if err != nil {
		t.Fatalf("CachePath: %v", err)
	}
	abs, err := filepath.Abs("some/workdir")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "scan-cache", workdirKeyTail(abs)+".json")
	if got != want {
		t.Fatalf("CachePath = %q, want %q", got, want)
	}
}

// workdirKeyTail mirrors config.WorkdirKey's "<basename>-<8 hex>" derivation.
func workdirKeyTail(abs string) string {
	clean := filepath.Clean(abs)
	sum := sha256.Sum256([]byte(clean))
	return filepath.Base(clean) + "-" + hex.EncodeToString(sum[:4])
}

func TestLoadCachedMissingIsFalse(t *testing.T) {
	cacheHome(t)
	_, ok, err := LoadCached("no-such-project")
	if err != nil || ok {
		t.Fatalf("LoadCached(missing) = (_, %v, %v), want (_, false, nil)", ok, err)
	}
}

func TestLoadCachedParseFailureDegrades(t *testing.T) {
	cacheHome(t)
	path, err := CachePath("parse-bad")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, ok, err := LoadCached("parse-bad")
	if err != nil || ok {
		t.Fatalf("LoadCached(parse-bad) = (_, %v, %v), want (_, false, nil)", ok, err)
	}
}

func TestLoadCachedSchemaMismatchIsFalse(t *testing.T) {
	cacheHome(t)
	path, err := CachePath("schema-bad")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(Summary{SchemaVersion: 999, Fingerprint: "x"})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, ok, err := LoadCached("schema-bad")
	if err != nil || ok {
		t.Fatalf("LoadCached(schema-bad) = (_, %v, %v), want (_, false, nil)", ok, err)
	}
}

func TestRefreshWritesAndLoadsCache(t *testing.T) {
	home := cacheHome(t)
	workdir := t.TempDir()
	// One memory artifact under .vulnetix so the fingerprint is non-trivial.
	dir := filepath.Join(workdir, ".vulnetix")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	mem := "last_scan:\n  critical: 2\n  high: 1\n"
	if err := os.WriteFile(filepath.Join(dir, "memory.yaml"), []byte(mem), 0o600); err != nil {
		t.Fatal(err)
	}

	summary, err := Refresh(context.Background(), workdir)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if summary.Fingerprint == "" || summary.SchemaVersion != SchemaVersion {
		t.Fatalf("summary missing cache metadata: %+v", summary)
	}
	if summary.Union.Critical != 2 || summary.Union.High != 1 {
		t.Fatalf("union = %+v", summary.Union)
	}

	// The cached summary must now round-trip.
	path, err := CachePath(workdir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cache not written to %s: %v", path, err)
	}
	loaded, ok, err := LoadCached(workdir)
	if err != nil || !ok {
		t.Fatalf("LoadCached = (_, %v, %v), want ok", ok, err)
	}
	if loaded.Fingerprint != summary.Fingerprint || loaded.Union.Critical != 2 {
		t.Fatalf("round-trip mismatch: %+v vs %+v", loaded, summary)
	}
	_ = home
}
