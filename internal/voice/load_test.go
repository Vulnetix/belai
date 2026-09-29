package voice

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/voice/asr/asrtest"
)

// pinFixture makes the tiny model the pinned model for the test and puts it
// where ModelPath finds it.
func pinFixture(t *testing.T, content []byte) string {
	t.Helper()
	models := isolate(t)
	sum := sha256.Sum256(content)
	old := pinnedSHA
	pinnedSHA = hex.EncodeToString(sum[:])
	t.Cleanup(func() { pinnedSHA = old })
	dest := filepath.Join(models, strings.ReplaceAll(ModelRepo, "/", "--"), ModelFile)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return dest
}

func TestLoadModelFromDisk(t *testing.T) {
	path := pinFixture(t, asrtest.TinyModel("", asrtest.EnglishVocab))
	if got := ModelSource(); got != path && got != "built in" {
		t.Fatalf("ModelSource = %q, want %q", got, path)
	}
	if Embedded() {
		t.Skip("this build embeds the model, which LoadModel prefers")
	}
	m, err := LoadModel()
	if err != nil || m == nil {
		t.Fatalf("LoadModel = %v, %v", m, err)
	}
}

func TestLoadModelRefusesAChangedFile(t *testing.T) {
	if Embedded() {
		t.Skip("this build embeds the model")
	}
	path := pinFixture(t, asrtest.TinyModel("", asrtest.EnglishVocab))
	if err := os.WriteFile(path, []byte("swapped"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadModel(); !errors.Is(err, ErrModelChanged) {
		t.Fatalf("err = %v, want ErrModelChanged", err)
	}
}

func TestLoadModelWithNoModel(t *testing.T) {
	if Embedded() {
		t.Skip("this build embeds the model")
	}
	isolate(t)
	if ModelSource() != "" {
		t.Fatalf("ModelSource = %q with nothing on disk", ModelSource())
	}
	if _, err := LoadModel(); !errors.Is(err, ErrNoModel) {
		t.Fatalf("err = %v, want ErrNoModel", err)
	}
}

func TestEmbeddedModelIsTheModelWeName(t *testing.T) {
	if !Embedded() {
		t.Skip("built without the belai_voice tag: nothing embedded")
	}
	sum := sha256.Sum256(embeddedModel)
	if hex.EncodeToString(sum[:]) != ModelSHA256 {
		t.Fatalf("the embedded model hashes to %x, not the pinned %s", sum, ModelSHA256)
	}
	if len(embeddedModel) != ModelSize {
		t.Fatalf("the embedded model is %d bytes, want %d", len(embeddedModel), ModelSize)
	}
	if ModelSource() != "built in" {
		t.Fatalf("ModelSource = %q", ModelSource())
	}
	if _, err := LoadModel(); err != nil {
		t.Fatalf("the embedded model does not load: %v", err)
	}
}

func TestNothingEmbeddedWithoutTheTag(t *testing.T) {
	// Run under -tags belai_voice, this is TestEmbeddedModelIsTheModelWeName's
	// mirror; without the tag the default build must stay download-only.
	if len(embeddedModel) == 0 && Embedded() {
		t.Fatal("Embedded reports a model that is not there")
	}
}
