package mlclassify

import (
	"os"
	"path/filepath"
	"testing"
)

// vocabStub is enough for newWordPieceTokenizer to load successfully.
const vocabStub = `
[PAD]
[UNK]
[CLS]
[SEP]
[MASK]
the
a
is
`

// TestRemoteGateUsesCachedTokenizerFiles verifies that newRemoteGate does not
// need to reach HuggingFace when vocab.txt and tokenizer_config.json already
// exist in the on-disk model cache.
func TestRemoteGateUsesCachedTokenizerFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SIGNET_HOME", home)

	modelID := "fake-org/fake-model"
	cacheDir, err := modelCacheDir(modelID)
	if err != nil {
		t.Fatalf("modelCacheDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "vocab.txt"), []byte(vocabStub), 0o600); err != nil {
		t.Fatalf("write vocab.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "tokenizer_config.json"), []byte(`{"do_lower_case": true}`), 0o600); err != nil {
		t.Fatalf("write tokenizer_config.json: %v", err)
	}

	mc := ModelConfig{
		ID:          modelID,
		Source:      SourceHuggingFace,
		AttackLabel: "LABEL_1",
	}
	g, err := newRemoteGate(Phase1, mc, func() (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("newRemoteGate: %v", err)
	}
	if g == nil {
		t.Fatal("newRemoteGate returned nil gate")
	}
}

// TestRemoteGateCreatesCacheDir verifies that newRemoteGate creates the
// model cache directory when it does not yet exist.
func TestRemoteGateCreatesCacheDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SIGNET_HOME", home)

	modelID := "another-org/another-model"
	root, err := modelsRoot()
	if err != nil {
		t.Fatalf("modelsRoot: %v", err)
	}
	cacheDir := filepath.Join(root, modelFileName(modelID))
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Fatalf("cache dir should not exist yet, got %v", err)
	}

	mc := ModelConfig{
		ID:          modelID,
		Source:      SourceHuggingFace,
		AttackLabel: "LABEL_1",
	}
	// It will fail to fetch from HuggingFace, but newRemoteGate must create
	// the cache directory before attempting any fetch.
	newRemoteGate(Phase1, mc, func() (string, error) { return "", nil }) //nolint:errcheck
	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		t.Fatal("newRemoteGate did not create the cache directory")
	}
}

// TestRemoteGateSeedsEmbeddedModelIntoCache verifies that when a remote gate
// is configured for a model that is embedded in this binary, the tokenizer
// files are seeded from the embedded assets instead of failing on a 404 from
// the upstream repo (some embedded models, like the jailbreak detector, do not
// ship vocab.txt).
func TestRemoteGateSeedsEmbeddedModelIntoCache(t *testing.T) {
	id, ok := EmbeddedPhase2()
	if !ok {
		t.Skip("no phase-2 model embedded in this build variant")
	}

	home := t.TempDir()
	t.Setenv("SIGNET_HOME", home)

	mc := ModelConfig{
		ID:          id,
		Source:      SourceHuggingFace,
		AttackLabel: "unsafe",
	}
	g, err := newRemoteGate(Phase2, mc, func() (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("newRemoteGate: %v", err)
	}
	if g == nil {
		t.Fatal("newRemoteGate returned nil gate")
	}

	// The embedded assets should now be in the cache so later invocations do
	// not need network access.
	cacheDir, err := modelCacheDir(id)
	if err != nil {
		t.Fatalf("modelCacheDir: %v", err)
	}
	for _, f := range []string{"vocab.txt", "tokenizer_config.json", "spago_model.bin"} {
		if _, err := os.Stat(filepath.Join(cacheDir, f)); err != nil {
			t.Fatalf("expected embedded file %s in cache: %v", f, err)
		}
	}
}
