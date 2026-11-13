package localinfer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// hfRepoServer serves the model-listing JSON RepoGGUFs and RemoteInfo read
// from /api/models/<repo>?blobs=true.
func hfRepoServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func setHFBase(t *testing.T, srv *httptest.Server) {
	t.Helper()
	old := HFBase
	HFBase = srv.URL
	t.Cleanup(func() { HFBase = old })
}

func TestRepoGGUFsListsAndFilters(t *testing.T) {
	srv := hfRepoServer(t, `{"siblings":[
		{"rfilename":"model-Q4_K_M.gguf","size":10,"lfs":{"sha256":"ABCDEF","size":2708804640}},
		{"rfilename":"model-Q8_0.gguf","size":5},
		{"rfilename":"UPPER.GGUF","size":3},
		{"rfilename":"mmproj-f16.gguf","size":100},
		{"rfilename":"README.md","size":2},
		{"rfilename":"config.json","size":1}
	]}`)
	setHFBase(t, srv)

	files, err := RepoGGUFs(context.Background(), srv.Client(), "org/repo", "")
	if err != nil {
		t.Fatalf("RepoGGUFs: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("files = %+v, want the three GGUFs only (mmproj, README, config dropped)", files)
	}
	for _, f := range files {
		switch f.Name {
		case "model-Q4_K_M.gguf":
			if f.Size != 2708804640 || f.SHA256 != "abcdef" {
				t.Fatalf("Q4_K_M = %+v, want LFS size and lower-case sha", f)
			}
		case "model-Q8_0.gguf":
			if f.Size != 5 || f.SHA256 != "" {
				t.Fatalf("Q8_0 = %+v, want plain size 5", f)
			}
		case "UPPER.GGUF":
			// Case-insensitive suffix: included.
		default:
			t.Fatalf("unexpected file %q", f.Name)
		}
	}
}

func TestRepoGGUFsSendsTokenAndMapsErrors(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if strings.HasPrefix(r.URL.Path, "/api/models/org/missing") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"siblings":[]}`))
	}))
	defer srv.Close()
	setHFBase(t, srv)

	files, err := RepoGGUFs(context.Background(), srv.Client(), "org/empty", "tok")
	if err != nil || len(files) != 0 {
		t.Fatalf("RepoGGUFs = %v, %v", files, err)
	}
	if auth != "Bearer tok" {
		t.Fatalf("Authorization = %q, want the token on the listing request", auth)
	}
	_, err = RepoGGUFs(context.Background(), srv.Client(), "org/missing", "")
	var de *DownloadError
	if !errors.As(err, &de) || de.Class != DownloadNotFound {
		t.Fatalf("err = %v, want not-found class", err)
	}
}

func TestPickGGUF(t *testing.T) {
	files := []RepoFile{
		{Name: "dir/model-Q8_0.gguf", RemoteFile: RemoteFile{Size: 20}},
		{Name: "dir/model-Q4_K_M.gguf", RemoteFile: RemoteFile{Size: 10}},
		{Name: "dir/model-Q4_K_M-imat.gguf", RemoteFile: RemoteFile{Size: 12}},
	}
	got, ok := PickGGUF(files, "q4_k_m") // case-insensitive match
	if !ok || got.Name != "dir/model-Q4_K_M.gguf" {
		t.Fatalf("PickGGUF = %+v, %v; want the smallest Q4_K_M", got, ok)
	}
	got, ok = PickGGUF(files, "Q8_0")
	if !ok || got.Name != "dir/model-Q8_0.gguf" {
		t.Fatalf("PickGGUF = %+v, %v; want Q8_0", got, ok)
	}
	if _, ok := PickGGUF(files, "Q5_K_M"); ok {
		t.Fatal("PickGGUF must report false for an absent quant")
	}
	if _, ok := PickGGUF(nil, "Q4_K_M"); ok {
		t.Fatal("PickGGUF must report false for an empty list")
	}
}

func TestHubPartialSizePicksNewestIncomplete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HF_HOME", "")
	t.Setenv("HF_HUB_CACHE", "")
	blobs := filepath.Join(home, ".cache", "huggingface", "hub", "models--org--model", "blobs")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, size int64, mod time.Time) {
		t.Helper()
		p := filepath.Join(blobs, name)
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	write("a.incomplete", 4, time.Now().Add(-2*time.Hour))
	write("b.incomplete", 7, time.Now().Add(-time.Hour))
	write("c", 99, time.Now()) // not a partial: ignored

	if got := HubPartialSize("org/model"); got != 7 {
		t.Fatalf("HubPartialSize = %d, want 7 (newest incomplete)", got)
	}
	if got := HubPartialSize("other/model"); got != 0 {
		t.Fatalf("HubPartialSize = %d, want 0 for a repo with no partials", got)
	}
}

func TestRemoveModelFileDeletesOwnCopy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BELAI_MODELS_DIR", dir)
	dest := filepath.Join(dir, "org--repo", "f.gguf")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest+".part", []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveModelFile("org/repo", "f.gguf"); err != nil {
		t.Fatalf("RemoveModelFile: %v", err)
	}
	for _, p := range []string{dest, dest + ".part"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after removal", p)
		}
	}
	// A missing file is not an error: the second removal succeeds.
	if err := RemoveModelFile("org/repo", "f.gguf"); err != nil {
		t.Fatalf("RemoveModelFile on a missing file: %v", err)
	}
}

func TestDownloadErrorFormattingAndUnwrap(t *testing.T) {
	inner := errors.New("boom")
	e := &DownloadError{Class: DownloadNetwork, Msg: "download f.gguf", Err: inner}
	if got := e.Error(); got != "download f.gguf: boom" {
		t.Fatalf("Error = %q", got)
	}
	if !errors.Is(e, inner) {
		t.Fatal("errors.Is must see the wrapped error")
	}
	var target *DownloadError
	if !errors.As(fmt.Errorf("outer: %w", e), &target) || target != e {
		t.Fatal("errors.As must find the DownloadError through a wrap")
	}
	plain := &DownloadError{Class: DownloadNotFound, Msg: "no such file"}
	if got := plain.Error(); got != "no such file" {
		t.Fatalf("Error = %q", got)
	}
	if plain.Unwrap() != nil {
		t.Fatalf("Unwrap = %v, want nil", plain.Unwrap())
	}
}

func TestLaunchErrorFormatting(t *testing.T) {
	plain := &LaunchError{ExitCode: 1, Msg: "server died"}
	if got := plain.Error(); got != "server died" {
		t.Fatalf("Error = %q", got)
	}
	withOut := &LaunchError{ExitCode: 1, Msg: "server died", Output: "llama_model_load: error loading model"}
	if got := withOut.Error(); got != "server died\nllama_model_load: error loading model" {
		t.Fatalf("Error = %q", got)
	}
}

func TestVersionReadsBuildNumber(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "llama-server")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'llama-server version 1.2.3 (build 1234)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	build, first := Version(context.Background(), Binary{Path: bin})
	if build != 1234 || !strings.Contains(first, "version") {
		t.Fatalf("Version = (%d, %q), want build 1234", build, first)
	}
	if b, f := Version(context.Background(), Binary{}); b != 0 || f != "" {
		t.Fatalf("Version(empty) = (%d, %q), want (0, empty)", b, f)
	}
	fail := filepath.Join(dir, "fail")
	if err := os.WriteFile(fail, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if b, _ := Version(context.Background(), Binary{Path: fail}); b != 0 {
		t.Fatalf("Version(failing) build = %d, want 0", b)
	}
}

func TestLlamaServerFindsOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH lookup semantics differ")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "llama-server"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	bin, ok := LlamaServer()
	if !ok || bin.Path != filepath.Join(dir, "llama-server") {
		t.Fatalf("LlamaServer = %+v, %v", bin, ok)
	}
	t.Setenv("PATH", "/nonexistent")
	if _, ok := LlamaServer(); ok {
		t.Fatal("LlamaServer must report false when absent")
	}
}

func TestHFCLIFindsVerifiedBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	dir := t.TempDir()
	hf := filepath.Join(dir, "hf")
	if err := os.WriteFile(hf, []byte("#!/bin/sh\necho 'usage: hf <command> -- Hugging Face Hub CLI'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	got, ok := HFCLI()
	if !ok || got != hf {
		t.Fatalf("HFCLI = (%q, %v), want the verified fake", got, ok)
	}
	// Memoised: a second call returns the same path without re-checking.
	if again, _ := HFCLI(); again != got {
		t.Fatalf("HFCLI memoisation broken: %q then %q", got, again)
	}
}

func TestHFDownloadFileParsesGGUFPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "hf")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho downloading\nprintf '/cache/models--org--model/blobs/f.gguf\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := HFDownloadFile(context.Background(), binary, "org/model", "f.gguf", "", nil)
	if err != nil {
		t.Fatalf("HFDownloadFile: %v", err)
	}
	if got != "/cache/models--org--model/blobs/f.gguf" {
		t.Fatalf("HFDownloadFile = %q", got)
	}
}

func TestHFDownloadFileRequiresBinary(t *testing.T) {
	if _, err := HFDownloadFile(context.Background(), "", "org/model", "f.gguf", "", nil); err == nil {
		t.Fatal("empty binary must fail")
	}
}

func TestHFDownloadFileFallsBackToFindModelFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	home := t.TempDir()
	t.Setenv("HF_HOME", home)
	snap := filepath.Join(home, "hub", "models--org--model", "snapshots", "abc")
	if err := os.MkdirAll(snap, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(snap, "f.gguf")
	if err := os.WriteFile(want, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "hf")
	// The CLI succeeds but prints no .gguf path, forcing the cache fallback.
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := HFDownloadFile(context.Background(), binary, "org/model", "f.gguf", "", nil)
	if err != nil {
		t.Fatalf("HFDownloadFile: %v", err)
	}
	if got != want {
		t.Fatalf("HFDownloadFile = %q, want cache fallback %q", got, want)
	}
}

func TestHFDownloadFileMapsCLIFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "hf")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho 'Failed to connect to huggingface.co' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := HFDownloadFile(context.Background(), binary, "org/model", "f.gguf", "", nil)
	var de *DownloadError
	if !errors.As(err, &de) || de.Class != DownloadNetwork {
		t.Fatalf("err = %v, want network-class DownloadError", err)
	}
	if !strings.Contains(err.Error(), "Failed to connect") {
		t.Fatalf("err = %q, want the CLI's last output line", err)
	}
}
