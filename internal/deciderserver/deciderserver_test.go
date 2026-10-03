package deciderserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/localinfer"
)

// TestMain doubles as a stand-in strands-decider: when the launch test runs
// this binary through a wrapper script, it serves /health and /v1/systemone
// the way upstream's server does, reporting what it saw of its argv and
// environment so the test can check both.
func TestMain(m *testing.M) {
	if os.Getenv("DECIDER_TEST_HELPER") == "1" {
		helperServe(os.Args[1:])
		return
	}
	os.Exit(m.Run())
}

func helperServe(args []string) {
	if len(args) == 0 || args[0] != "serve" {
		fmt.Println("Usage: strands-decider [OPTIONS] COMMAND\n  serve  Serve POST /v1/systemone (strands decider)")
		return
	}
	port, name := "", ""
	for i := 1; i+1 < len(args); i++ {
		switch args[i] {
		case "--port":
			port = args[i+1]
		case "--model-name":
			name = args[i+1]
		}
	}
	device := "cpu"
	if os.Getenv("HF_HUB_OFFLINE") != "1" || os.Getenv("HF_HOME") == "" || os.Getenv("HF_TOKEN") != "" || os.Getenv("OPENAI_API_KEY") != "" {
		device = "bad-env"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "model": name, "checkpoint": args[1], "device": device})
	})
	_ = http.ListenAndServe("127.0.0.1:"+port, mux)
}

func TestArgsAndEnvAreFixed(t *testing.T) {
	m := decisions.Decider2B
	got := strings.Join(Args("/m/decider-2b/rev", 18098, m), " ")
	if got != "serve /m/decider-2b/rev --host 127.0.0.1 --port 18098 --model-name strands-decider-2b" {
		t.Errorf("Args = %q", got)
	}
	env := strings.Join(Env("/m"), " ")
	for _, want := range []string{"HF_HOME=" + filepath.Join("/m", "hf"), "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1"} {
		if !strings.Contains(env, want) {
			t.Errorf("Env = %q, missing %q", env, want)
		}
	}
	if strings.Contains(env, "TOKEN") {
		t.Errorf("Env carries a token: %q", env)
	}
}

func TestParseHealth(t *testing.T) {
	cases := []struct {
		body string
		ok   bool
	}{
		{`{"status":"ok","model":"strands-decider-2b","checkpoint":"/x/y","device":"cuda"}`, true},
		{`{"status":"ok","model":"hobson","checkpoint":"StrandsAgents/strands-decider-2B-hobson-v19","device":"cpu"}`, true},
		{`{"status":"ok","model":"x","checkpoint":"/home/u/.cache/belai/models/strands-decider/decider-2b/bb282d786bc251fd4e3068de3ada9ddbb38127cd"}`, true},
		{`{"status":"ok","model":"llama","checkpoint":"/models/llama"}`, false},
		{`{"status":"loading","model":"strands-decider-2b"}`, false},
		{`not json`, false},
	}
	for _, c := range cases {
		if _, ok := ParseHealth(strings.NewReader(c.body)); ok != c.ok {
			t.Errorf("ParseHealth(%s) = %v, want %v", c.body, ok, c.ok)
		}
	}
	h, _ := ParseHealth(strings.NewReader(`{"status":"ok","model":"strands-decider-2b\u001b[31m<x>","checkpoint":"a/strands-decider-2b","device":"cuda:0 ; rm"}`))
	if h.Model != "strands-decider-2b31mx" || h.Device != "cuda:0rm" {
		t.Errorf("health strings are not reduced to identifiers: %+v", h)
	}
}

func TestProbeIsLoopbackOnly(t *testing.T) {
	asked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { asked = true }))
	t.Cleanup(srv.Close)
	host := strings.Replace(srv.URL, "127.0.0.1", "example.com", 1)
	if _, ok := Probe(context.Background(), srv.Client(), host); ok || asked {
		t.Fatal("a non-loopback URL was probed")
	}
}

func TestParseHubProviders(t *testing.T) {
	names, err := ParseHubProviders(strings.NewReader(`{"id":"x","inferenceProviderMapping":{"hf-inference":{"status":"live"},"to<g>ether":{}}}`))
	if err != nil || strings.Join(names, ",") != "hf-inference,together" {
		t.Errorf("map form = %v, %v", names, err)
	}
	names, err = ParseHubProviders(strings.NewReader(`{"inferenceProviderMapping":[{"provider":"nebius"}]}`))
	if err != nil || strings.Join(names, ",") != "nebius" {
		t.Errorf("list form = %v, %v", names, err)
	}
	names, err = ParseHubProviders(strings.NewReader(`{"inferenceProviderMapping":{}}`))
	if err != nil || len(names) != 0 {
		t.Errorf("none = %v, %v", names, err)
	}
}

// fakeModel is a tiny checkpoint with real hashes, served by a fake Hub.
func fakeModel(t *testing.T) (decisions.DeciderModel, map[string][]byte) {
	t.Helper()
	files := map[string][]byte{
		"org/ck@r1/hobson_config.json":       []byte(`{"head_type":"pointer"}`),
		"org/ck@r1/head.safetensors":         []byte("head-bytes"),
		"org/base@r2/config.json":            []byte(`{"model_type":"qwen3_5"}`),
		"org/base@r2/model.safetensors":      []byte("weights-bytes"),
		"org/ck@r1/lora/adapter_config.json": []byte(`{"r":16}`),
	}
	// f describes one file; key is "<repo>@<revision>/<path>".
	f := func(key string, lfs bool) decisions.DeciderFile {
		b := files[key]
		sum := sha256.Sum256(b)
		_, path, _ := strings.Cut(key, "/")
		_, path, _ = strings.Cut(path, "/")
		return decisions.DeciderFile{Path: path, Size: int64(len(b)), SHA256: hex.EncodeToString(sum[:]), LFS: lfs}
	}
	m := decisions.DeciderModel{
		ID: "decider-2b", Label: "Strands Decider-2B", Repo: "org/ck", Revision: "r1", BaseRepo: "org/base", BaseRevision: "r2",
		Checkpoint: []decisions.DeciderFile{f("org/ck@r1/hobson_config.json", false), f("org/ck@r1/head.safetensors", true), f("org/ck@r1/lora/adapter_config.json", false)},
		Base:       []decisions.DeciderFile{f("org/base@r2/config.json", false), f("org/base@r2/model.safetensors", true)},
		MaxOptions: 24,
	}
	return m, files
}

func fakeHub(t *testing.T, files map[string][]byte, lie bool) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/models/") {
			// /api/models/<org>/<repo>/revision/<rev>
			parts := strings.Split(strings.TrimPrefix(p, "/api/models/"), "/")
			if len(parts) != 4 || parts[2] != "revision" {
				http.NotFound(w, r)
				return
			}
			prefix := parts[0] + "/" + parts[1] + "@" + parts[3] + "/"
			var sib []map[string]any
			for k, b := range files {
				if !strings.HasPrefix(k, prefix) {
					continue
				}
				sum := sha256.Sum256(b)
				h := hex.EncodeToString(sum[:])
				if lie {
					h = strings.Repeat("0", 64)
				}
				sib = append(sib, map[string]any{"rfilename": strings.TrimPrefix(k, prefix), "size": len(b), "lfs": map[string]any{"sha256": h, "size": len(b)}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"siblings": sib})
			return
		}
		// /<org>/<repo>/resolve/<rev>/<file>
		parts := strings.SplitN(strings.TrimPrefix(p, "/"), "/", 5)
		if len(parts) != 5 || parts[2] != "resolve" {
			http.NotFound(w, r)
			return
		}
		b, ok := files[parts[0]+"/"+parts[1]+"@"+parts[3]+"/"+parts[4]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	old := localinfer.HFBase
	localinfer.HFBase = srv.URL
	t.Cleanup(func() { localinfer.HFBase = old })
}

func TestDownloadPlacesAPinnedOfflineSnapshot(t *testing.T) {
	m, files := fakeModel(t)
	fakeHub(t, files, false)
	root := t.TempDir()
	if Present(root, m) {
		t.Fatal("present before download")
	}
	if Missing(root, m) != m.Size() {
		t.Fatalf("Missing = %d, want %d", Missing(root, m), m.Size())
	}
	if err := Download(context.Background(), nil, root, m, "", nil); err != nil {
		t.Fatal(err)
	}
	if !Present(root, m) || Missing(root, m) != 0 {
		t.Fatal("not present after download")
	}
	if b, _ := os.ReadFile(filepath.Join(CheckpointDir(root, m), "lora", "adapter_config.json")); string(b) != `{"r":16}` {
		t.Errorf("adapter config = %q", b)
	}
	snap := filepath.Join(HFHome(root), "hub", "models--org--base", "snapshots", "r2", "model.safetensors")
	if b, _ := os.ReadFile(snap); string(b) != "weights-bytes" {
		t.Errorf("base snapshot = %q", b)
	}
	if ref, _ := os.ReadFile(filepath.Join(HFHome(root), "hub", "models--org--base", "refs", "main")); string(ref) != "r2" {
		t.Errorf("refs/main = %q", ref)
	}
}

// A file whose content does not match the pinned hash is removed and never
// placed, and an LFS file the Hub reports with another hash is never fetched.
func TestDownloadRefusesChangedFiles(t *testing.T) {
	m, files := fakeModel(t)
	files["org/ck@r1/hobson_config.json"] = []byte(`{"head_type":"pointes"}`) // same size, other bytes
	fakeHub(t, files, false)
	root := t.TempDir()
	err := Download(context.Background(), nil, root, m, "", nil)
	var de *localinfer.DownloadError
	if err == nil || !asDownload(err, &de) || de.Class != localinfer.DownloadChecksum {
		t.Fatalf("err = %v, want a checksum failure", err)
	}
	if Present(root, m) {
		t.Fatal("a tampered checkpoint counts as present")
	}

	m2, files2 := fakeModel(t)
	fakeHub(t, files2, true)
	if err := Download(context.Background(), nil, t.TempDir(), m2, "", nil); err == nil || !strings.Contains(err.Error(), "different SHA-256") {
		t.Fatalf("err = %v, want the Hub's report refused", err)
	}
}

func asDownload(err error, out **localinfer.DownloadError) bool {
	de, ok := err.(*localinfer.DownloadError)
	if ok {
		*out = de
	}
	return ok
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// A server already answering on loopback is used as it is: never relaunched,
// never stopped by this process.
func TestEnsureAdoptsARunningServer(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","model":"strands-decider-2b","checkpoint":"StrandsAgents/strands-decider-2B-hobson-v19","device":"mps"}`))
	}))
	t.Cleanup(srv.Close)
	port, _ := strconv.Atoi(srv.URL[strings.LastIndexByte(srv.URL, ':')+1:])
	o := Options{Ports: []int{freePort(t), port}, Root: t.TempDir()}
	h, err := Ensure(context.Background(), decisions.Decider2B, o)
	if err != nil {
		t.Fatal(err)
	}
	if h.Owned || h.BaseURL != srv.URL {
		t.Fatalf("handle = %+v", h)
	}
	st := Detect(context.Background(), decisions.Decider2B, o)
	if st.Running != srv.URL || st.Device != "mps" || !strings.HasPrefix(st.Label(), "running at 127.0.0.1:") {
		t.Errorf("status = %+v (%s)", st, st.Label())
	}
}

func TestEnsureNeedsWeightsThenBinary(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	m, files := fakeModel(t)
	o := Options{Ports: []int{freePort(t)}, Root: t.TempDir()}
	if _, err := Ensure(context.Background(), m, o); err != ErrWeightsMissing {
		t.Fatalf("err = %v, want ErrWeightsMissing", err)
	}
	fakeHub(t, files, false)
	if err := Download(context.Background(), nil, o.Root, m, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(context.Background(), m, o); err != ErrNoBinary {
		t.Fatalf("err = %v, want ErrNoBinary", err)
	}
	if !strings.Contains(Describe(ErrNoBinary), "uv tool install strands-decider") {
		t.Errorf("Describe = %q", Describe(ErrNoBinary))
	}
}

// The launch runs the binary from PATH with the fixed argv and the scrubbed
// environment plus the offline settings: no token reaches it.
func TestEnsureLaunchesWithFixedArgvAndScrubbedEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in server is a shell script")
	}
	t.Setenv("BELAI_HOME", t.TempDir())
	bin := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexec " + strconv.Quote(self) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, BinaryName), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DECIDER_TEST_HELPER", "1")
	t.Setenv("HF_TOKEN", "hf_leak")
	t.Setenv("OPENAI_API_KEY", "sk-leak")

	m, files := fakeModel(t)
	fakeHub(t, files, false)
	o := Options{Ports: []int{freePort(t)}, Root: t.TempDir(), Deadline: 20 * time.Second}
	if err := Download(context.Background(), nil, o.Root, m, "", nil); err != nil {
		t.Fatal(err)
	}
	h, err := Ensure(context.Background(), m, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	if !h.Owned {
		t.Fatal("a launched server must be owned")
	}
	hl, ok := Probe(context.Background(), nil, h.BaseURL)
	if !ok {
		t.Fatal("the launched server does not answer /health")
	}
	if hl.Device != "cpu" {
		t.Errorf("the server saw a token or missed the offline settings (%q)", hl.Device)
	}
	if hl.Model != "strands-decider-2b" || hl.Checkpoint != "r1" {
		t.Errorf("health = %+v", hl)
	}
	h.Stop()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := Probe(context.Background(), nil, h.BaseURL); !ok {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("Stop did not stop the server")
}
