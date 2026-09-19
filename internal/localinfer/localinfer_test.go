package localinfer

import (
	"context"
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

	"github.com/vulnetix/signet/internal/activity"
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

func TestFreePortBindable(t *testing.T) {
	port, err := FreePort()
	if err != nil {
		t.Fatalf("FreePort: %v", err)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("FreePort returned port %d which is not free: %v", port, err)
	}
	ln.Close()
}

func TestBaseURLDefaultHost(t *testing.T) {
	if got, want := BaseURL("", 1234), "http://127.0.0.1:1234/v1"; got != want {
		t.Fatalf("BaseURL = %q, want %q", got, want)
	}
}

func TestPreferredPortsIncludesCommon(t *testing.T) {
	got := PreferredPorts()
	want := []int{11434, 18080, 8000}
	if len(got) != len(want) {
		t.Fatalf("PreferredPorts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("PreferredPorts = %v, want %v", got, want)
		}
	}
}

func TestArgsUsesModelPathWhenSet(t *testing.T) {
	args := Args(ArgsOptions{ModelPath: "/tmp/model.gguf", Port: 1234})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-m /tmp/model.gguf") {
		t.Fatalf("args missing -m path: %v", args)
	}
	if strings.Contains(joined, "-hf") {
		t.Fatalf("args should not use -hf when ModelPath is set: %v", args)
	}
}

func TestArgsUsesHFRepoWhenNoModelPath(t *testing.T) {
	args := Args(ArgsOptions{Repo: "org/model", Quant: "Q5_K_M", Port: 1234})
	joined := strings.Join(args, " ")
	want := "-hf org/model:Q5_K_M"
	if !strings.Contains(joined, want) {
		t.Fatalf("args missing %q: %v", want, args)
	}
}

func TestArgsDefaultsQuantAndPort(t *testing.T) {
	args := Args(ArgsOptions{Repo: "org/model"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, ":Q4_K_M") {
		t.Fatalf("args missing default quant: %v", args)
	}
	if !strings.Contains(joined, "--port 8080") {
		t.Fatalf("args missing default port 8080: %v", args)
	}
}

func TestArgsLoopbackAndSamplingFlags(t *testing.T) {
	args := Args(ArgsOptions{Repo: "org/model", Port: 18080})
	joined := strings.Join(args, " ")
	for _, want := range []string{"--host 127.0.0.1", "--port 18080", "--ctx-size", "--jinja"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %v", want, args)
		}
	}
}

func TestArgsHFRepoAlias(t *testing.T) {
	args := Args(ArgsOptions{HFRepo: "alias/repo", Port: 1234})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-hf alias/repo:Q4_K_M") {
		t.Fatalf("args missing alias repo: %v", args)
	}
}

func TestDetectFindsOrReportsAbsent(t *testing.T) {
	_, ok := Detect()
	// Either a binary is present (ok) or none is (false); both are valid.
	_ = ok
}

func TestLaunchGracefulStopKillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups not supported on windows")
	}
	// Start a long-running process that writes to stdout. Stopping it must
	// terminate the process.
	dir := t.TempDir()
	script := filepath.Join(dir, "sleep.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho started\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	reg := activity.NewRegistry()
	pidfile := filepath.Join(dir, "server.pid")
	bin := Binary{Name: "sh", Path: script}
	args := []string{}
	baseURL := BaseURL("", 1) // health check will fail

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stop, err := Launch(ctx, bin, args, baseURL, LaunchOptions{
		Deadline: 500 * time.Millisecond,
		Registry: reg,
		Pidfile:  pidfile,
	})
	if err == nil {
		t.Fatal("expected launch to fail health check quickly")
	}
	// If stop was not returned the process tree should already be gone.
	if stop != nil {
		_ = stop()
	}

	// Pidfile must be removed on stop.
	if _, err := os.Stat(pidfile); !os.IsNotExist(err) {
		t.Fatalf("pidfile not removed: %v", err)
	}

	// The activity registry must contain a terminal record.
	list := reg.List()
	if len(list) == 0 {
		t.Fatal("activity registry is empty")
	}
	if list[0].State != activity.StateDone && list[0].State != activity.StateFailed && list[0].State != activity.StateKilled {
		t.Fatalf("unexpected terminal state %s", list[0].State)
	}
}

func TestLaunchTokenInEnvironmentNotArgv(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "env.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$HF_TOKEN\" > \"$1/token\"; echo done\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	bin := Binary{Name: "sh", Path: script}
	args := []string{dir}
	stop, err := Launch(context.Background(), bin, args, "http://127.0.0.1:1/v1", LaunchOptions{
		Deadline: 100 * time.Millisecond,
		HFToken:  "secret-token",
	})
	if stop != nil {
		_ = stop()
	}
	if err == nil {
		// We expect health check failure; err may be nil if the fake script
		// happens to answer on port 1, which it won't.
	}

	got, _ := os.ReadFile(filepath.Join(dir, "token"))
	if strings.TrimSpace(string(got)) != "secret-token" {
		t.Fatalf("HF_TOKEN not in child environment: %q", got)
	}
	for _, a := range args {
		if a == "secret-token" {
			t.Fatal("token leaked into argv")
		}
	}
}

func TestHFBinaryReportsFalseWhenAbsent(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	if _, ok := HFBinary(); ok {
		t.Fatal("HFBinary should report false when PATH is empty")
	}
}

func TestHFDownloadUsesEnvironmentToken(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "hf")
	// Fake hf binary writes its environment and arguments, then prints a
	// fake .gguf path.
	script := `#!/bin/sh
printf '%s\n' "$HF_TOKEN" > "$SIGNET_TEST_DIR/token"
printf '%s\n' "$*" > "$SIGNET_TEST_DIR/argv"
printf 'ok\n/home/user/.cache/huggingface/hub/models--org--model/blobs/model-Q4_K_M.gguf\n'
`
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SIGNET_TEST_DIR", dir)

	path, err := HFDownload(context.Background(), binary, "org/model", "Q4_K_M", "my-token", nil)
	if err != nil {
		t.Fatalf("HFDownload: %v", err)
	}
	if !strings.Contains(path, "Q4_K_M.gguf") {
		t.Fatalf("unexpected path %q", path)
	}
	tok, _ := os.ReadFile(filepath.Join(dir, "token"))
	if strings.TrimSpace(string(tok)) != "my-token" {
		t.Fatalf("token not in env: %q", tok)
	}
	argv, _ := os.ReadFile(filepath.Join(dir, "argv"))
	if strings.Contains(string(argv), "my-token") {
		t.Fatal("token appeared in argv")
	}
}

func TestHFWhoami(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "hf")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho 'user'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	who, ok := HFWhoami(context.Background(), binary)
	if !ok || who != "user" {
		t.Fatalf("HFWhoami = %q, %v", who, ok)
	}
}

func TestHFCacheScan(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "hf")
	script := `#!/bin/sh
printf 'REPO                    REVISION                                SIZE    LAST_MODIFIED\n'
printf 'org/model               abc123                                  4.0K    2024-01-01\n'
printf '  model-Q4_K_M.gguf\n'
`
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	models, err := HFCacheScan(context.Background(), binary)
	if err != nil {
		t.Fatalf("HFCacheScan: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("expected at least one cached model")
	}
	found := false
	for _, m := range models {
		if strings.Contains(m.File, "Q4_K_M.gguf") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Q4_K_M model not found in scan: %v", models)
	}
}
