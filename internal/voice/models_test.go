package voice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vulnetix/belai/internal/localinfer"
)

// isolate points every model cache at a temp dir so no host file can satisfy
// a lookup.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("HF_HOME", filepath.Join(dir, "hf"))
	t.Setenv("HUGGINGFACE_HUB_CACHE", filepath.Join(dir, "hub"))
	t.Setenv("LLAMA_CACHE", filepath.Join(dir, "llama"))
	models := filepath.Join(dir, "models")
	t.Setenv("BELAI_MODELS_DIR", models)
	return models
}

// hfServer serves the API listing and the file for payload.
func hfServer(t *testing.T, payload []byte, hits *atomic.Int32) {
	t.Helper()
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if strings.HasPrefix(r.URL.Path, "/api/models/") {
			type lfs struct {
				SHA256 string `json:"sha256"`
				Size   int64  `json:"size"`
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"siblings": []map[string]any{{
				"rfilename": ModelFile, "size": len(payload),
				"lfs": lfs{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(payload))},
			}}})
			return
		}
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	old := localinfer.HFBase
	localinfer.HFBase = srv.URL
	t.Cleanup(func() { localinfer.HFBase = old })
	oldPin := pinnedSHA
	pinnedSHA = hex.EncodeToString(sum[:])
	t.Cleanup(func() { pinnedSHA = oldPin })
}

func TestPinnedConstantsAreShaped(t *testing.T) {
	if len(ModelSHA256) != 64 || strings.ToLower(ModelSHA256) != ModelSHA256 {
		t.Fatalf("ModelSHA256 = %q", ModelSHA256)
	}
	if ModelSize < 30_000_000 || ModelSize > 40_000_000 {
		t.Fatalf("ModelSize = %d, not the 32 MB q5_1 file", ModelSize)
	}
}

func TestEnsureDownloadsOnlyAfterConfirmation(t *testing.T) {
	isolate(t)
	payload := []byte(strings.Repeat("whisper-weights-", 2048))
	hfServer(t, payload, nil)

	var offer Offer
	declined := func(o Offer) bool { offer = o; return false }
	if _, err := Ensure(context.Background(), nil, "", declined, nil); !errors.Is(err, ErrDeclined) {
		t.Fatalf("declined: err = %v, want ErrDeclined", err)
	}
	if offer.Size != int64(len(payload)) || offer.File != ModelFile || offer.Repo != ModelRepo || offer.Dest == "" {
		t.Fatalf("offer = %+v", offer)
	}
	if ModelPath() != "" {
		t.Fatal("a declined download left a file behind")
	}
	if _, err := Ensure(context.Background(), nil, "", nil, nil); !errors.Is(err, ErrDeclined) {
		t.Fatalf("nil confirm must decline, err = %v", err)
	}

	path, err := Ensure(context.Background(), nil, "", func(Offer) bool { return true }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(payload) {
		t.Fatal("downloaded bytes differ")
	}
}

func TestEnsureUsesAFileOnDiskWithoutTheNetwork(t *testing.T) {
	models := isolate(t)
	payload := []byte("already here")
	var hits atomic.Int32
	hfServer(t, payload, &hits)
	dest := filepath.Join(models, strings.ReplaceAll(ModelRepo, "/", "--"), ModelFile)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := Ensure(context.Background(), nil, "", func(Offer) bool { t.Fatal("asked to confirm"); return false }, nil)
	if err != nil || path != dest {
		t.Fatalf("path = %q, err = %v", path, err)
	}
	if hits.Load() != 0 {
		t.Fatalf("%d network requests for a file already on disk", hits.Load())
	}
}

func TestEnsureRefusesAFileThatFailsThePinnedChecksum(t *testing.T) {
	models := isolate(t)
	hfServer(t, []byte("the pinned bytes"), nil)
	dest := filepath.Join(models, strings.ReplaceAll(ModelRepo, "/", "--"), ModelFile)
	_ = os.MkdirAll(filepath.Dir(dest), 0o755)
	_ = os.WriteFile(dest, []byte("swapped in a shared cache"), 0o600)
	if _, err := Ensure(context.Background(), nil, "", func(Offer) bool { return true }, nil); !errors.Is(err, ErrModelChanged) {
		t.Fatalf("err = %v, want ErrModelChanged", err)
	}
}

func TestEnsureRemovesADownloadThatFailsThePinnedChecksum(t *testing.T) {
	isolate(t)
	payload := []byte(strings.Repeat("upstream-replaced-", 64))
	hfServer(t, payload, nil)
	pinnedSHA = strings.Repeat("a", 64) // Hugging Face agrees with the file; the pin does not
	_, err := Ensure(context.Background(), nil, "", func(Offer) bool { return true }, nil)
	if !errors.Is(err, ErrModelChanged) {
		t.Fatalf("err = %v, want ErrModelChanged", err)
	}
	if ModelPath() != "" {
		t.Fatal("a file that failed the pin was kept")
	}
}

func TestVerifyMissingFile(t *testing.T) {
	if err := Verify(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("Verify of a missing file succeeded")
	}
}
