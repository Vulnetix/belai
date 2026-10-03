package localinfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary double as a fake llama-server: with
// LOCALINFER_FAKE_SERVER set it serves /health on --port, or exits early the
// way a failed model load does.
func TestMain(m *testing.M) {
	switch os.Getenv("LOCALINFER_FAKE_SERVER") {
	case "serve":
		fakeServe()
		return
	case "exit":
		fmt.Fprintln(os.Stderr, "llama_model_load: error loading model: unknown model architecture: 'qwen35'")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func fakeServe() {
	port := "0"
	for i, a := range os.Args {
		if a == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	if out := os.Getenv("LOCALINFER_ENV_OUT"); out != "" {
		_ = os.WriteFile(out, []byte(strings.Join(os.Environ(), "\n")), 0o600)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	_ = http.ListenAndServe("127.0.0.1:"+port, mux)
}

func fakeBinary(t *testing.T, mode string) Binary {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake server relies on process groups")
	}
	t.Setenv("LOCALINFER_FAKE_SERVER", mode)
	return Binary{Name: "llama-server", Path: os.Args[0]}
}

func TestLaunchedServerOutlivesStartupContext(t *testing.T) {
	bin := fakeBinary(t, "serve")
	port, err := FreePort()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	stop, err := Launch(ctx, bin, []string{"--port", strconv.Itoa(port)}, BaseURL("127.0.0.1", port), LaunchOptions{Deadline: 5 * time.Second})
	if err != nil {
		cancel()
		t.Fatalf("launch: %v", err)
	}
	defer stop()
	// The startup context ends as soon as Launch returns, as it does in the
	// TUI. The server must keep answering.
	cancel()
	time.Sleep(300 * time.Millisecond)
	if !serverReady(context.Background(), BaseURL("127.0.0.1", port)) {
		t.Fatal("server died when the startup context was cancelled")
	}
	_ = stop()
	if serverReady(context.Background(), BaseURL("127.0.0.1", port)) {
		t.Fatal("server still answering after stop")
	}
}

func TestLaunchReportsEarlyExitWithClass(t *testing.T) {
	bin := fakeBinary(t, "exit")
	start := time.Now()
	_, err := Launch(context.Background(), bin, []string{"--port", "1"}, BaseURL("127.0.0.1", 1), LaunchOptions{Deadline: 20 * time.Second})
	if err == nil {
		t.Fatal("expected an error")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("early exit took %s to report; it should not wait for the deadline", time.Since(start))
	}
	var le *LaunchError
	if !errors.As(err, &le) {
		t.Fatalf("error %T is not a *LaunchError: %v", err, err)
	}
	if le.Class != LogUpgrade || le.ExitCode != 1 {
		t.Fatalf("class=%q exit=%d, want upgrade/1", le.Class, le.ExitCode)
	}
}

func TestLaunchScrubsEnvironment(t *testing.T) {
	bin := fakeBinary(t, "serve")
	out := filepath.Join(t.TempDir(), "env")
	t.Setenv("LOCALINFER_ENV_OUT", out)
	t.Setenv("OPENAI_API_KEY", "sk-leak")
	t.Setenv("GITHUB_TOKEN", "ghp-leak")
	port, _ := FreePort()
	stop, err := Launch(context.Background(), bin, []string{"--port", strconv.Itoa(port)}, BaseURL("127.0.0.1", port), LaunchOptions{Deadline: 5 * time.Second})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer stop()
	env, _ := os.ReadFile(out)
	for _, leak := range []string{"sk-leak", "ghp-leak"} {
		if strings.Contains(string(env), leak) {
			t.Fatalf("server environment carries %q", leak)
		}
	}
}

func TestServerReadyWaitsOutLoading(t *testing.T) {
	loading := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" && loading {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if serverReady(context.Background(), srv.URL+"/v1") {
		t.Fatal("503 on /health must read as not ready even though /v1/models answers")
	}
	loading = false
	if !serverReady(context.Background(), srv.URL+"/v1") {
		t.Fatal("200 on /health must read as ready")
	}
}

func TestDecisionArgs(t *testing.T) {
	args := DecisionArgs(DecisionServerOptions{ModelPath: "/m/decider.gguf", Alias: "decider-4b", Port: 18097, NGL: 0})
	for _, bad := range []string{"--jinja", "--no-mmap", "--temp", "-hf"} {
		if slices.Contains(args, bad) {
			t.Fatalf("decision argv must not carry %s: %v", bad, args)
		}
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-m /m/decider.gguf", "--alias decider-4b", "--host 127.0.0.1", "--port 18097", "-np 1", "--n-gpu-layers 0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv %q lacks %q", joined, want)
		}
	}
	if all := strings.Join(DecisionArgs(DecisionServerOptions{ModelPath: "m", NGL: -1}), " "); !strings.Contains(all, "--n-gpu-layers 99") {
		t.Fatalf("negative NGL should offload all layers: %s", all)
	}
	if strings.Contains(joined, "--ubatch-size") {
		t.Fatalf("a letter-readout model needs no batch sizing: %s", joined)
	}
	if one := strings.Join(DecisionArgs(DecisionServerOptions{ModelPath: "m", OneBatch: true}), " "); !strings.Contains(one, "--batch-size 8192 --ubatch-size 8192") {
		t.Fatalf("a /v1/systemone model evaluates a prompt in one micro-batch: %s", one)
	}
}

func TestClassifyLog(t *testing.T) {
	cases := map[string]LogClass{
		"llama_model_load: error loading model: unknown model architecture: 'gemma4'": LogUpgrade,
		"gguf_init_from_file_impl: invalid magic characters":                          LogCorrupt,
		"ggml_backend_cpu_buffer_type_alloc_buffer: failed to allocate buffer":        LogOOM,
		"CUDA error: no kernel image is available":                                    LogGPU,
		"error: couldn't bind HTTP server socket, Address already in use":             LogPortBusy,
		"llama_model_load_from_file: failed to load model":                            LogCorrupt,
		"main: server is listening":                                                   LogUnknown,
	}
	for in, want := range cases {
		if got := ClassifyLog(in); got != want {
			t.Errorf("ClassifyLog(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindModelFileExactNameOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HF_HOME", "")
	t.Setenv("HF_HUB_CACHE", "")
	t.Setenv("BELAI_MODELS_DIR", filepath.Join(home, "own"))
	snap := filepath.Join(home, ".cache", "huggingface", "hub", "models--Mapika--decider-4b-GGUF", "snapshots", "abc")
	if err := os.MkdirAll(snap, 0o755); err != nil {
		t.Fatal(err)
	}
	// A sibling quant of the same repo must not satisfy the lookup.
	_ = os.WriteFile(filepath.Join(snap, "decider-4b-v2-Q4_K_M.gguf"), []byte("x"), 0o644)
	if p := FindModelFile("Mapika/decider-4b-GGUF", "decider-4b-v2.1-Q4_K_M.gguf"); p != "" {
		t.Fatalf("found %q for a file that is not there", p)
	}
	want := filepath.Join(snap, "decider-4b-v2.1-Q4_K_M.gguf")
	_ = os.WriteFile(want, []byte("gguf"), 0o644)
	if p := FindModelFile("Mapika/decider-4b-GGUF", "decider-4b-v2.1-Q4_K_M.gguf"); p != want {
		t.Fatalf("FindModelFile = %q, want %q", p, want)
	}
}

func TestDownloadFileResumesAndVerifies(t *testing.T) {
	payload := []byte(strings.Repeat("decision-weights-", 4096))
	sum := sha256.Sum256(payload)
	var sawRange, sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRange = r.Header.Get("Range")
		sawAuth = r.Header.Get("Authorization")
		http.ServeContent(w, r, "f.gguf", time.Time{}, strings.NewReader(string(payload)))
	}))
	defer srv.Close()
	old := HFBase
	HFBase = srv.URL
	defer func() { HFBase = old }()
	dir := t.TempDir()
	t.Setenv("BELAI_MODELS_DIR", dir)

	// A partial file from an interrupted run is resumed, not restarted.
	part := filepath.Join(dir, "org--repo", "f.gguf.part")
	_ = os.MkdirAll(filepath.Dir(part), 0o755)
	_ = os.WriteFile(part, payload[:1000], 0o644)

	var last int64
	path, err := DownloadFile(context.Background(), srv.Client(), "org/repo", "f.gguf", "hf_tok",
		RemoteFile{Size: int64(len(payload)), SHA256: hex.EncodeToString(sum[:])},
		func(done, total int64) {
			if done < last {
				t.Errorf("progress went backwards: %d after %d", done, last)
			}
			last = done
		})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if sawRange != "bytes=1000-" {
		t.Fatalf("Range = %q, want resume from 1000", sawRange)
	}
	if sawAuth != "Bearer hf_tok" {
		t.Fatalf("Authorization = %q", sawAuth)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(payload) {
		t.Fatal("downloaded bytes differ")
	}
	if last != int64(len(payload)) {
		t.Fatalf("final progress %d, want %d", last, len(payload))
	}
	if FindModelFile("org/repo", "f.gguf") != path {
		t.Fatal("FindModelFile does not see the finished download")
	}
}

func TestDownloadFileRejectsBadChecksum(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("tampered"))
	}))
	defer srv.Close()
	old := HFBase
	HFBase = srv.URL
	defer func() { HFBase = old }()
	t.Setenv("BELAI_MODELS_DIR", t.TempDir())
	_, err := DownloadFile(context.Background(), srv.Client(), "org/repo", "f.gguf", "",
		RemoteFile{Size: 8, SHA256: strings.Repeat("0", 64)}, nil)
	var de *DownloadError
	if !errors.As(err, &de) || de.Class != DownloadChecksum {
		t.Fatalf("err = %v, want checksum failure", err)
	}
	if FindModelFile("org/repo", "f.gguf") != "" {
		t.Fatal("a file that failed its checksum must not be kept")
	}
}

func TestRemoteInfoReadsLFSAndRefusesRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/models/org/moved") {
			http.Redirect(w, r, "https://elsewhere.example/api", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"siblings":[{"rfilename":"a.gguf","size":10,"lfs":{"sha256":"ABCD","size":2708804640}}]}`))
	}))
	defer srv.Close()
	old := HFBase
	HFBase = srv.URL
	defer func() { HFBase = old }()
	rf, err := RemoteInfo(context.Background(), srv.Client(), "org/repo", "a.gguf", "")
	if err != nil || rf.Size != 2708804640 || rf.SHA256 != "abcd" {
		t.Fatalf("RemoteInfo = %+v, %v", rf, err)
	}
	if _, err := RemoteInfo(context.Background(), srv.Client(), "org/repo", "missing.gguf", ""); err == nil {
		t.Fatal("expected not-found for a missing file")
	}
	if _, err := RemoteInfo(context.Background(), srv.Client(), "org/moved", "a.gguf", "tok"); err == nil {
		t.Fatal("a redirect must not be followed")
	}
}

func TestHFBinaryRejectsImpostor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "hf"), []byte("#!/bin/sh\necho 'hf 0.6.0 hidden file utility'\n"), 0o755)
	t.Setenv("PATH", dir)
	if _, ok := HFBinary(); ok {
		t.Fatal("a non-Hugging-Face hf binary was accepted")
	}
	_ = os.WriteFile(filepath.Join(dir, "hf"), []byte("#!/bin/sh\necho 'usage: hf <command> -- Hugging Face Hub CLI'\n"), 0o755)
	if p, ok := HFBinary(); !ok || p == "" {
		t.Fatal("the real CLI was rejected")
	}
}
