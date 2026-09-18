package localinfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbeRunningFindsHealthyServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	got := ProbeRunning(context.Background(), []string{"http://127.0.0.1:1", srv.URL})
	if got != srv.URL {
		t.Fatalf("ProbeRunning = %q, want %q", got, srv.URL)
	}
}

// TestProbeRunningAcceptsV1SuffixedBase pins that a base already carrying the
// OpenAI surface suffix probes /v1/models, not /v1/v1/models — every in-tree
// caller holds the suffixed form.
func TestProbeRunningAcceptsV1SuffixedBase(t *testing.T) {
	var probed []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probed = append(probed, r.URL.Path)
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	base := srv.URL + "/v1"
	if got := ProbeRunning(context.Background(), []string{base}); got != base {
		t.Fatalf("ProbeRunning = %q, want %q", got, base)
	}
	for _, p := range probed {
		if p != "/v1/models" {
			t.Fatalf("probed %q, want /v1/models", p)
		}
	}
}

func TestProbeRunningNoServer(t *testing.T) {
	got := ProbeRunning(context.Background(), []string{"http://127.0.0.1:1"})
	if got != "" {
		t.Fatalf("ProbeRunning = %q, want empty", got)
	}
}

func TestArgsTargetLoopback(t *testing.T) {
	args := Args("org/model", 18080)
	joined := strings.Join(args, " ")
	for _, want := range []string{"-hf", "org/model:Q4_K_M", "--host", "127.0.0.1", "--port", "18080", "--ctx-size", "16384"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %v", want, args)
		}
	}
}

func TestResolveModelPicksLargestGGUF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"siblings":[
			{"filename":"README.md","size":10},
			{"filename":"model-q4_k_m.gguf","size":7000,"sha256":"aa"},
			{"filename":"model-f16.gguf","size":14000,"sha256":"bb"}
		]}`))
	}))
	defer srv.Close()
	t.Setenv("SIGNET_HF_BASE_URL", srv.URL)

	mf, err := ResolveModel(context.Background(), "org/model", "")
	if err != nil {
		t.Fatalf("ResolveModel: %v", err)
	}
	if mf.Name != "model-f16.gguf" || mf.Size != 14000 || mf.SHA256 != "bb" {
		t.Fatalf("ModelFile = %+v, want f16 gguf", mf)
	}
}

func TestResolveModelNoGGUF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"siblings":[{"filename":"README.md","size":10}]}`))
	}))
	defer srv.Close()
	t.Setenv("SIGNET_HF_BASE_URL", srv.URL)

	if _, err := ResolveModel(context.Background(), "org/model", ""); err == nil || !strings.Contains(err.Error(), "no GGUF") {
		t.Fatalf("expected no-GGUF error, got %v", err)
	}
}

func TestDetectFindsOrReportsAbsent(t *testing.T) {
	_, ok := Detect()
	// Either a binary is present (ok) or none is (false); both are valid.
	_ = ok
}

func TestDownloadChecksumVerifiedAndAtomic(t *testing.T) {
	payload := "gguf-bytes"
	sum := sha256.Sum256([]byte(payload))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()
	t.Setenv("SIGNET_HF_BASE_URL", srv.URL)

	dir := t.TempDir()
	dest, err := Download(context.Background(), "org/model", ModelFile{
		Name:   "model.gguf",
		SHA256: hex.EncodeToString(sum[:]),
	}, "", dir)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != payload {
		t.Fatalf("downloaded = %q err=%v", got, err)
	}
	// No .part temp files left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".part") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestDownloadChecksumMismatchRemovesPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("wrong"))
	}))
	defer srv.Close()
	t.Setenv("SIGNET_HF_BASE_URL", srv.URL)

	dir := t.TempDir()
	if _, err := Download(context.Background(), "org/model", ModelFile{
		Name:   "model.gguf",
		SHA256: strings.Repeat("a", 64),
	}, "", dir); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("partial files not cleaned up: %v", entries)
	}
}

func TestDownloadResumesPartial(t *testing.T) {
	payload := "0123456789abcdef"
	sum := sha256.Sum256([]byte(payload))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rng := r.Header.Get("range")
		if rng == "bytes=4-" {
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte(payload[4:]))
			return
		}
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()
	t.Setenv("SIGNET_HF_BASE_URL", srv.URL)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model.gguf.part"), []byte("0123"), 0o600); err != nil {
		t.Fatalf("seed partial: %v", err)
	}

	dest, err := Download(context.Background(), "org/model", ModelFile{
		Name:   "model.gguf",
		SHA256: hex.EncodeToString(sum[:]),
	}, "", dir)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != payload {
		t.Fatalf("resumed download = %q err=%v, want %q", got, err, payload)
	}
}

func TestDownloadReportsProgress(t *testing.T) {
	payload := strings.Repeat("x", 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()
	t.Setenv("SIGNET_HF_BASE_URL", srv.URL)

	dir := t.TempDir()
	var sawFinal bool
	_, err := Download(context.Background(), "org/model", ModelFile{
		Name: "model.gguf", Size: int64(len(payload)),
	}, "", dir, func(done, total int64) {
		if total != int64(len(payload)) {
			t.Fatalf("total = %d, want %d", total, len(payload))
		}
		if done >= total {
			sawFinal = true
		}
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !sawFinal {
		t.Fatal("progress callback never reached completion")
	}
}
