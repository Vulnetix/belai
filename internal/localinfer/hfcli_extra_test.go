package localinfer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseGGUFPathPreference(t *testing.T) {
	out := strings.Join([]string{
		"Downloading ...",
		"/tmp/one.gguf",
		"relative/path.gguf", // relative: ignored
		"~/two.gguf",         // tilde: accepted
		"/tmp/three.GGUF",    // case-insensitive suffix
	}, "\n")
	// Last absolute/tilde line wins.
	if got := parseGGUFPath(out); got != "/tmp/three.GGUF" {
		t.Fatalf("parseGGUFPath = %q, want /tmp/three.GGUF", got)
	}
	if got := parseGGUFPath("no paths here"); got != "" {
		t.Fatalf("parseGGUFPath = %q, want empty", got)
	}
}

func TestGuessRepoFromPath(t *testing.T) {
	cases := map[string]string{
		filepath.Join("x", "models--org--repo", "snapshots", "f"): "org/repo",
		filepath.Join("x", "models--justname", "snapshots", "f"):  "",
		filepath.Join("x", "not-a-model", "f"):                    "",
	}
	for in, want := range cases {
		if got := guessRepoFromPath(in); got != want {
			t.Errorf("guessRepoFromPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindHubGGUF(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HF_HOME", home)

	// No matching files.
	if _, err := findHubGGUF("org/model", "Q8"); err == nil {
		t.Fatal("findHubGGUF should fail when no match exists")
	}

	// Create a matching cache entry.
	snap := filepath.Join(home, "hub", "models--org--model", "snapshots", "abc")
	if err := os.MkdirAll(snap, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(snap, "model-Q4_K_M.gguf")
	if err := os.WriteFile(want, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findHubGGUF("org/model", "Q4_K_M")
	if err != nil {
		t.Fatalf("findHubGGUF: %v", err)
	}
	if got != want {
		t.Fatalf("findHubGGUF = %q, want %q", got, want)
	}

	// Wrong quant: no match.
	if _, err := findHubGGUF("org/model", "Q8_0"); err == nil {
		t.Fatal("findHubGGUF should fail for wrong quant")
	}

	// Wrong repo: no match.
	if _, err := findHubGGUF("other/model", "Q4_K_M"); err == nil {
		t.Fatal("findHubGGUF should fail for wrong repo")
	}
}

func TestScanHubCacheWalk(t *testing.T) {
	home := t.TempDir()
	// scanHubCache(nil) walks $HOME/.cache/huggingface/hub.
	t.Setenv("HOME", home)
	snap := filepath.Join(home, ".cache", "huggingface", "hub", "models--org--model", "snapshots", "abc")
	if err := os.MkdirAll(snap, 0o755); err != nil {
		t.Fatal(err)
	}
	gguf := filepath.Join(snap, "model-Q4_K_M.gguf")
	if err := os.WriteFile(gguf, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A non-gguf file must be ignored.
	if err := os.WriteFile(filepath.Join(snap, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	models, err := scanHubCache(nil)
	if err != nil {
		t.Fatalf("scanHubCache(nil): %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("models = %+v, want exactly one", models)
	}
	m := models[0]
	if m.Path != gguf || m.File != "model-Q4_K_M.gguf" || m.Repo != "org/model" {
		t.Fatalf("model = %+v", m)
	}
}

func TestScanHubCacheParsesOutput(t *testing.T) {
	out := []byte("REPO REVISION SIZE LAST_MODIFIED\n" +
		"org/model abc 4.0K 2024-01-01\n" +
		"  /cache/models--org--model/snapshots/abc/model-Q4_K_M.gguf\n")
	models, err := scanHubCache(out)
	if err != nil {
		t.Fatalf("scanHubCache(output): %v", err)
	}
	if len(models) != 1 || models[0].File != "model-Q4_K_M.gguf" {
		t.Fatalf("models = %+v", models)
	}
	if models[0].Repo != "org/model" {
		t.Fatalf("repo = %q, want org/model", models[0].Repo)
	}
}

func TestHFWhoamiEdges(t *testing.T) {
	if who, ok := HFWhoami(context.Background(), ""); ok || who != "" {
		t.Fatalf("HFWhoami(empty) = (%q, %v)", who, ok)
	}

	dir := t.TempDir()
	// Binary exits non-zero: not authenticated.
	bad := filepath.Join(dir, "hf-fail")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if who, ok := HFWhoami(context.Background(), bad); ok || who != "" {
		t.Fatalf("HFWhoami(failing) = (%q, %v)", who, ok)
	}

	// Binary prints only blank lines: no user.
	blank := filepath.Join(dir, "hf-blank")
	if err := os.WriteFile(blank, []byte("#!/bin/sh\nprintf '\\n\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if who, ok := HFWhoami(context.Background(), blank); ok || who != "" {
		t.Fatalf("HFWhoami(blank) = (%q, %v)", who, ok)
	}
}

func TestHFDownloadRequiresBinary(t *testing.T) {
	if _, err := HFDownload(context.Background(), "", "org/model", "Q4_K_M", "", nil); err == nil {
		t.Fatal("HFDownload with empty binary should fail")
	}
}

func TestHFDownloadFallsBackToCacheScan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HF_HOME", home)
	snap := filepath.Join(home, "hub", "models--org--model", "snapshots", "abc")
	if err := os.MkdirAll(snap, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(snap, "model-Q4_K_M.gguf")
	if err := os.WriteFile(want, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	binary := filepath.Join(dir, "hf")
	// Binary succeeds but prints no .gguf path, forcing the cache-scan fallback.
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho downloaded\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := HFDownload(context.Background(), binary, "org/model", "Q4_K_M", "", nil)
	if err != nil {
		t.Fatalf("HFDownload: %v", err)
	}
	if got != want {
		t.Fatalf("HFDownload = %q, want %q", got, want)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "", "x", "y"); got != "x" {
		t.Fatalf("firstNonEmpty = %q, want x", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Fatalf("firstNonEmpty = %q, want empty", got)
	}
}
