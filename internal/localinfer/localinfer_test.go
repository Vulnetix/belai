package localinfer

import (
	"context"
	"net/http"
	"net/http/httptest"
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
