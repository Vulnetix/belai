package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// hub is a fake Hugging Face: files maps "<id>/resolve/<revision>/<file>" to
// its body, anything else is a 404. It records each request path and the
// Authorization header it was sent with.
type hub struct {
	files map[string]string
	mu    sync.Mutex
	paths []string
	auth  []string
}

func newHub(t *testing.T, files map[string]string) *hub {
	t.Helper()
	h := &hub{files: files}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		h.mu.Lock()
		h.paths = append(h.paths, key)
		h.auth = append(h.auth, r.Header.Get("Authorization"))
		h.mu.Unlock()
		body, ok := h.files[key]
		if !ok {
			http.Error(w, "Entry not found", http.StatusNotFound)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	old := hfBase
	hfBase = srv.URL
	t.Cleanup(func() { hfBase = old })
	return h
}

func (h *hub) requested() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.paths...)
}

var testSpec = modelSpec{id: "org/model", revision: "abc123", baseVocab: "org/base", assetsDir: "org_model"}

func TestFetchWritesTheFileAndSendsTheToken(t *testing.T) {
	h := newHub(t, map[string]string{"org/model/resolve/abc123/config.json": `{"vocab_size": 3}`})
	dir := t.TempDir()
	if err := fetch(http.DefaultClient, testSpec, "config.json", dir, "hf_secret"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "config.json")); string(b) != `{"vocab_size": 3}` {
		t.Fatalf("config.json = %q", b)
	}
	if h.auth[0] != "Bearer hf_secret" {
		t.Fatalf("Authorization = %q", h.auth[0])
	}
	// No token, no header.
	if err := fetch(http.DefaultClient, testSpec, "config.json", dir, ""); err != nil {
		t.Fatal(err)
	}
	if h.auth[1] != "" {
		t.Fatalf("Authorization without a token = %q", h.auth[1])
	}
}

func TestFetchNotFoundIsAnErrorNamingTheFile(t *testing.T) {
	newHub(t, nil)
	dir := t.TempDir()
	err := fetch(http.DefaultClient, testSpec, "vocab.txt", dir, "")
	if err == nil || !strings.Contains(err.Error(), "org/model/vocab.txt") || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "Entry not found") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vocab.txt")); !os.IsNotExist(err) {
		t.Fatal("a 404 left a file behind")
	}
}

func TestFetchWeightsPrefersPyTorch(t *testing.T) {
	cases := []struct {
		name     string
		files    map[string]string
		wantFile string
		wantConv bool
		wantErr  bool
	}{
		{"pytorch weights", map[string]string{"org/model/resolve/abc123/pytorch_model.bin": "pt"}, "pytorch_model.bin", false, false},
		{"safetensors only", map[string]string{"org/model/resolve/abc123/model.safetensors": "st"}, "model.safetensors", true, false},
		{"no weights", nil, "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newHub(t, tc.files)
			dir := t.TempDir()
			conv, err := fetchWeights(http.DefaultClient, testSpec, dir, "")
			if (err != nil) != tc.wantErr || conv != tc.wantConv {
				t.Fatalf("fetchWeights = %v, %v", conv, err)
			}
			if tc.wantFile != "" {
				if _, err := os.Stat(filepath.Join(dir, tc.wantFile)); err != nil {
					t.Fatalf("%s not written: %v", tc.wantFile, err)
				}
			}
		})
	}
}

func TestFetchIfAbsent(t *testing.T) {
	t.Run("a file already present is not fetched", func(t *testing.T) {
		h := newHub(t, nil)
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "vocab.txt"), []byte("a\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := fetchIfAbsent(http.DefaultClient, testSpec, dir, "vocab.txt", "org/base", ""); err != nil {
			t.Fatal(err)
		}
		if got := h.requested(); len(got) != 0 {
			t.Fatalf("fetched %v for a file already present", got)
		}
	})
	t.Run("the model's own copy wins", func(t *testing.T) {
		newHub(t, map[string]string{
			"org/model/resolve/abc123/vocab.txt": "model\n",
			"org/base/resolve/main/vocab.txt":    "base\n",
		})
		dir := t.TempDir()
		if err := fetchIfAbsent(http.DefaultClient, testSpec, dir, "vocab.txt", "org/base", ""); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "vocab.txt")); string(b) != "model\n" {
			t.Fatalf("vocab.txt = %q, want the model's", b)
		}
	})
	t.Run("falls back to the base model at main", func(t *testing.T) {
		h := newHub(t, map[string]string{"org/base/resolve/main/vocab.txt": "base\n"})
		dir := t.TempDir()
		if err := fetchIfAbsent(http.DefaultClient, testSpec, dir, "vocab.txt", "org/base", ""); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "vocab.txt")); string(b) != "base\n" {
			t.Fatalf("vocab.txt = %q, want the base model's", b)
		}
		if got := h.requested(); len(got) != 2 || got[1] != "org/base/resolve/main/vocab.txt" {
			t.Fatalf("requests = %v", got)
		}
	})
	t.Run("no base model is an error", func(t *testing.T) {
		newHub(t, nil)
		err := fetchIfAbsent(http.DefaultClient, testSpec, t.TempDir(), "vocab.txt", "", "")
		if err == nil || !strings.Contains(err.Error(), "no vocab.txt on org/model and no base model") {
			t.Fatalf("err = %v", err)
		}
	})
}

// moduleRoot makes a temporary module root with a nested working directory
// and changes into the nested one, so repoRoot has to walk up to find it.
func moduleRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "tools", "modelprep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	return root
}

func TestRepoRootWalksUpToGoMod(t *testing.T) {
	root := moduleRoot(t)
	got, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	if real, _ := filepath.EvalSymlinks(got); real != root {
		t.Fatalf("repoRoot = %q, want %q", got, root)
	}
}

func TestRepoRootWithoutGoMod(t *testing.T) {
	dir := t.TempDir()
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			t.Skipf("%s has a go.mod above it", dir)
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	t.Chdir(dir)
	if _, err := repoRoot(); err == nil || !strings.Contains(err.Error(), "go.mod not found") {
		t.Fatalf("err = %v", err)
	}
}

func metaPath(root string) string {
	return filepath.Join(root, "internal", "mlclassify", "assets_meta.go")
}

func TestWriteMetaStartsFromTheTemplate(t *testing.T) {
	root := moduleRoot(t)
	if err := os.MkdirAll(filepath.Dir(metaPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeMeta("phase2", 0); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(metaPath(root))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.HasPrefix(got, "// Code generated by tools/modelprep. DO NOT EDIT.") ||
		!strings.Contains(got, "const phase2AttackLabel = 0\n") || !strings.Contains(got, "const phase1AttackLabel = 1\n") {
		t.Fatalf("assets_meta.go =\n%s", got)
	}
}

func TestWriteMetaKeepsTheOtherPhase(t *testing.T) {
	root := moduleRoot(t)
	if err := os.MkdirAll(filepath.Dir(metaPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := strings.Replace(metaTemplate, "const phase2AttackLabel = 1", "const phase2AttackLabel = 0", 1)
	if err := os.WriteFile(metaPath(root), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	// Any phase name other than phase2 updates phase 1.
	if err := writeMeta("phase1", 0); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(metaPath(root))
	if got := string(b); !strings.Contains(got, "const phase1AttackLabel = 0\n") || !strings.Contains(got, "const phase2AttackLabel = 0\n") {
		t.Fatalf("assets_meta.go =\n%s", got)
	}
}

func TestWriteMetaWithoutTheConstantFails(t *testing.T) {
	root := moduleRoot(t)
	if err := os.MkdirAll(filepath.Dir(metaPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath(root), []byte("package mlclassify\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeMeta("phase2", 1); err == nil || !strings.Contains(err.Error(), "no phase2AttackLabel constant") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunPhaseSkipsCompleteAssets(t *testing.T) {
	root := moduleRoot(t)
	h := newHub(t, nil)
	out := filepath.Join(root, "internal", "mlclassify", "assets", testSpec.assetsDir)
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"vocab.txt", "tokenizer_config.json", "config.json", "spago_model.bin"} {
		if err := os.WriteFile(filepath.Join(out, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := runPhase(testSpec, "python3", "phase1", false); err != nil {
		t.Fatal(err)
	}
	if got := h.requested(); len(got) != 0 {
		t.Fatalf("complete assets were fetched again: %v", got)
	}
	if _, err := os.Stat(metaPath(root)); !os.IsNotExist(err) {
		t.Fatal("a skipped phase rewrote assets_meta.go")
	}

	// -force rebuilds: here the fetch of config.json is what fails.
	if err := runPhase(testSpec, "python3", "phase1", true); err == nil || !strings.Contains(err.Error(), "org/model/config.json") {
		t.Fatalf("forced run err = %v", err)
	}
}

func TestPrepareChecksTheVocabularyAgainstTheConfig(t *testing.T) {
	moduleRoot(t)
	newHub(t, map[string]string{
		"org/model/resolve/abc123/config.json":       `{"vocab_size": 3}`,
		"org/model/resolve/abc123/pytorch_model.bin": "pt",
		"org/base/resolve/main/vocab.txt":            "[PAD]\n[UNK]\n",
	})
	_, err := prepare(testSpec, "python3")
	if err == nil || !strings.Contains(err.Error(), "vocab.txt has 2 entries, config.vocab_size is 3") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareStopsWhenSafetensorsCannotConvert(t *testing.T) {
	falseBin, err := exec.LookPath("false")
	if err != nil {
		t.Skip("no false binary")
	}
	moduleRoot(t)
	newHub(t, map[string]string{
		"org/model/resolve/abc123/config.json":       `{"vocab_size": 2}`,
		"org/model/resolve/abc123/model.safetensors": "st",
	})
	_, err = prepare(testSpec, falseBin)
	if err == nil || !strings.Contains(err.Error(), "safetensors conversion failed") {
		t.Fatalf("err = %v", err)
	}
}
