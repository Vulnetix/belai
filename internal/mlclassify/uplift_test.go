package mlclassify

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/vulnetix/signet/internal/rolemanager"
)

// ---- embeddedSpecFor / Embedded build-variant probes ----------------------

func TestEmbeddedSpecForMissing(t *testing.T) {
	// The vanilla build embeds no specs, so any id must miss without panicking.
	if _, ok := embeddedSpecFor("org/not-embedded"); ok {
		t.Fatal("embeddedSpecFor(unknown) = ok, want not ok")
	}
}

// TestEmbeddedBuildVariants pins the invariant that holds in every build
// variant: Embedded() agrees with EmbeddedPhase1, and any id reported as
// embedded is non-empty.
func TestEmbeddedBuildVariants(t *testing.T) {
	p1, ok1 := EmbeddedPhase1()
	p2, ok2 := EmbeddedPhase2()
	if Embedded() != ok1 {
		t.Fatalf("Embedded() = %v, want %v (EmbeddedPhase1 ok)", Embedded(), ok1)
	}
	if ok1 && p1 == "" {
		t.Fatal("embedded phase-1 id must be non-empty")
	}
	if ok2 && p2 == "" {
		t.Fatal("embedded phase-2 id must be non-empty")
	}
}

// ---- extract.go -----------------------------------------------------------

func TestExtractEmbeddedWritesAndIsIdempotent(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	spec := embeddedSpec{
		id: "org/model",
		fsys: fstest.MapFS{
			"spago_model.bin":       {Data: []byte("weights")},
			"vocab.txt":             {Data: []byte("vocab")},
			"tokenizer_config.json": {Data: []byte(`{"do_lower_case": true}`)},
			"nested":                {Mode: fs.ModeDir}, // directories are skipped
		},
	}
	dir, err := extractEmbedded(spec)
	if err != nil {
		t.Fatalf("extractEmbedded: %v", err)
	}
	for _, f := range []string{"spago_model.bin", "vocab.txt", "tokenizer_config.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("expected extracted file %s: %v", f, err)
		}
	}
	// A second extraction must be a no-op returning the same directory.
	dir2, err := extractEmbedded(spec)
	if err != nil || dir2 != dir {
		t.Fatalf("second extractEmbedded = (%q, %v), want (%q, nil)", dir2, err, dir)
	}
}

func TestExtractEmbeddedIncompleteIsHardError(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	spec := embeddedSpec{
		id:   "org/incomplete",
		fsys: fstest.MapFS{"vocab.txt": {Data: []byte("v")}},
	}
	if _, err := extractEmbedded(spec); err == nil {
		t.Fatal("missing spago_model.bin must fail extraction")
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")
	if err := writeFileAtomic(path, []byte("hello")); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "hello" {
		t.Fatalf("read back = %q, %v", got, err)
	}
	// CreateTemp in a missing directory must error.
	if err := writeFileAtomic(filepath.Join(dir, "missing", "out.bin"), nil); err == nil {
		t.Fatal("writeFileAtomic into a missing dir should fail")
	}
}

// ---- registry.go ----------------------------------------------------------

func TestNewGateUnknownSource(t *testing.T) {
	if _, err := newGate(Phase1, ModelConfig{Source: "bogus"}, nil); err == nil {
		t.Fatal("unknown model source must error")
	}
}

func TestNewGateEmbeddedUnknownID(t *testing.T) {
	if _, err := newGate(Phase1, ModelConfig{Source: SourceEmbedded, ID: "org/nope"}, nil); err == nil {
		t.Fatal("embedded source with unknown id must error")
	}
}

func TestNewGateHuggingFaceUsesCachedFiles(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	id := "fake/remote"
	dir, err := modelCacheDir(id)
	if err != nil {
		t.Fatalf("modelCacheDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vocab.txt"), []byte(vocabStub), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tokenizer_config.json"), []byte(`{"do_lower_case": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := newGate(Phase1, ModelConfig{ID: id, Source: SourceHuggingFace, AttackLabel: "LABEL_1"},
		func() (string, error) { return "", nil })
	if err != nil || g == nil {
		t.Fatalf("newGate(huggingface) = (%v, %v)", g, err)
	}
}

// ---- windowTokenizer / accessor methods -----------------------------------

type tokGate struct{ tok tokenizeFunc }

func (t *tokGate) phase() Phase { return Phase1 }
func (t *tokGate) fire(context.Context, string) (rolemanager.Sentinel, float64, error) {
	return "", 0, nil
}
func (t *tokGate) tokenizer() tokenizeFunc { return t.tok }

func TestWindowTokenizer(t *testing.T) {
	tf := func(string) []span { return nil }
	if got := (&Classifier{phase1: &tokGate{tok: tf}}).windowTokenizer(); got == nil {
		t.Fatal("phase-1 tokenizer must be preferred")
	}
	if got := (&Classifier{phase2: &tokGate{tok: tf}}).windowTokenizer(); got == nil {
		t.Fatal("phase-2 tokenizer must be the fallback")
	}
	if got := (&Classifier{phase1: &fakeGate{ph: Phase1}, phase2: &fakeGate{ph: Phase2}}).windowTokenizer(); got != nil {
		t.Fatal("no gate tokenizer must yield nil")
	}
}

func TestGateAccessors(t *testing.T) {
	tf := func(string) []span { return nil }
	lm := &localModel{spec: embeddedSpec{phase: Phase2}, tokFn: tf}
	if lm.phase() != Phase2 || lm.tokenizer() == nil {
		t.Fatalf("localModel accessors = (%q, %v)", lm.phase(), lm.tokenizer())
	}
	rm := &remoteModel{ph: Phase1, tokFn: tf}
	if rm.phase() != Phase1 || rm.tokenizer() == nil {
		t.Fatalf("remoteModel accessors = (%q, %v)", rm.phase(), rm.tokenizer())
	}
}

func TestSentinelFor(t *testing.T) {
	if sentinelFor(Phase1) != rolemanager.SentinelPromptInjection {
		t.Fatalf("sentinelFor(phase1) = %q", sentinelFor(Phase1))
	}
	if sentinelFor(Phase2) != rolemanager.SentinelJailbreak {
		t.Fatalf("sentinelFor(phase2) = %q", sentinelFor(Phase2))
	}
	if sentinelFor("unknown") != rolemanager.SentinelJailbreak {
		t.Fatal("sentinelFor(unknown) must default to jailbreak")
	}
}

func TestClassifierIdentityMethod(t *testing.T) {
	c := &Classifier{ident: "models;windowing=v2"}
	if c.Identity() != "models;windowing=v2" {
		t.Fatalf("Identity() = %q", c.Identity())
	}
}

// ---- offset / slicing clamps ---------------------------------------------

func TestRuneToByteClamps(t *testing.T) {
	byteAt := []int{0, 1, 2, 3}
	cases := []struct {
		r    int
		want int
	}{
		{-5, 0}, {0, 0}, {1, 1}, {10, 3}, {4, 3},
	}
	for _, c := range cases {
		if got := runeToByte(byteAt, c.r); got != c.want {
			t.Errorf("runeToByte(byteAt, %d) = %d, want %d", c.r, got, c.want)
		}
	}
}

func TestSliceTextGuardsInvertedSpans(t *testing.T) {
	byteAt := []int{0, 5, 10, 15}
	// An inverted span (end < start) must clamp to an empty slice, not a
	// negative-length one.
	toks := []span{{start: 2, end: 1}}
	if got := sliceText("hello world foo", byteAt, toks, 0, 1); got != "" {
		t.Fatalf("sliceText(inverted) = %q, want empty", got)
	}
}

// ---- tokenizer ------------------------------------------------------------

func TestNewWordPieceTokenizerMissingVocab(t *testing.T) {
	if _, err := newWordPieceTokenizer(filepath.Join(t.TempDir(), "nope.txt")); err == nil {
		t.Fatal("missing vocab.txt must error")
	}
}

func TestNewWordPieceTokenizerTokenizes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vocab.txt")
	if err := os.WriteFile(path, []byte(vocabStub), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, err := newWordPieceTokenizer(path)
	if err != nil {
		t.Fatalf("newWordPieceTokenizer: %v", err)
	}
	spans := tok("the a is")
	if len(spans) != 3 {
		t.Fatalf("tokenizer produced %d spans, want 3", len(spans))
	}
}

// ---- remote token resolution ---------------------------------------------

func TestNewRemoteGateRequiresAttackLabel(t *testing.T) {
	_, err := newRemoteGate(Phase1, ModelConfig{ID: "org/not-curated", Source: SourceHuggingFace},
		func() (string, error) { return "", nil })
	if err == nil {
		t.Fatal("remote model without a resolvable attack label must error")
	}
}

func TestNewRemoteGateNilTokenResolver(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	id := "fake/nil-token"
	dir, err := modelCacheDir(id)
	if err != nil {
		t.Fatalf("modelCacheDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vocab.txt"), []byte(vocabStub), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tokenizer_config.json"), []byte(`{"do_lower_case": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := newRemoteGate(Phase2, ModelConfig{ID: id, Source: SourceHuggingFace, AttackLabel: "unsafe"}, nil)
	if err != nil || g == nil {
		t.Fatalf("newRemoteGate(nil resolver) = (%v, %v), want a gate", g, err)
	}
}

// TestFetchHFFileCancelledContextCovers the client.Do error branch without
// touching the network: an already-cancelled context fails before dialing.
func TestFetchHFFileCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fetchHFFile(ctx, &http.Client{}, "org/model", "main", "vocab.txt", ""); err == nil {
		t.Fatal("cancelled context must fail the fetch")
	}
}
