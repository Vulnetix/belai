// Package localinfer manages a local inference server for the security
// classifier: detect a running server or a launchable binary, launch it with a
// health check, resolve HuggingFace model metadata, and shut the server down
// with Signet. The classifier routes to the local server through the existing
// "ollama" provider seam: set classifier.provider to ollama and OLLAMA_HOST to
// the server's base URL.
package localinfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/httpclient"
)

// DefaultPorts are the common local inference ports, probed for an
// already-running server.
var DefaultPorts = []int{11434, 18080, 8000}

// Binary is a launchable local inference server.
type Binary struct {
	Name string
	Path string
}

// Detect finds a launchable server binary. It follows the memoised LookPath
// pattern and reports the first of llama-server, ollama, or vllm found.
func Detect() (Binary, bool) {
	for _, name := range []string{"llama-server", "ollama", "vllm"} {
		if p, err := exec.LookPath(name); err == nil {
			return Binary{Name: name, Path: p}, true
		}
	}
	return Binary{}, false
}

// Args returns the launch args for llama-server serving one HuggingFace GGUF
// repo on 127.0.0.1:port. It is the default command for this device.
func Args(repo string, port int) []string {
	return []string{
		"-hf", repo + ":Q4_K_M",
		"--jinja",
		"--temp", "1.0",
		"--top-p", "0.95",
		"--top-k", "64",
		"--host", "127.0.0.1",
		"--port", fmt.Sprintf("%d", port),
		"--no-mmap",
		"-fa", "on",
		"--n-gpu-layers", "99",
		"--ctx-size", "16384",
	}
}

// ProbeRunning returns the first base URL that answers GET /v1/models. It is
// how an already-running server is found.
func ProbeRunning(ctx context.Context, bases []string) string {
	for _, base := range bases {
		url := strings.TrimRight(base, "/") + "/v1/models"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		resp, err := httpclient.Default().Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return base
		}
	}
	return ""
}

// Launch starts the server and waits for {baseURL}/v1/models to answer. It
// returns a stop function that kills the process and waits for it.
func Launch(ctx context.Context, bin Binary, args []string, baseURL string) (stop func() error, err error) {
	cmd := exec.CommandContext(ctx, bin.Path, args...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	stop = func() error {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return cmd.Wait()
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ProbeRunning(ctx, []string{baseURL}) == baseURL {
			return stop, nil
		}
		select {
		case <-ctx.Done():
			_ = stop()
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	_ = stop()
	return nil, fmt.Errorf("local server did not become healthy at %s", baseURL)
}

// ModelFile is one downloadable file of a HuggingFace model.
type ModelFile struct {
	Name   string `json:"filename"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	IsGGUF bool
}

// ResolveModel fetches the HuggingFace model metadata and returns the largest
// GGUF file plus its size and checksum. hfToken, when non-empty, is sent as a
// Bearer token (gated models). SIGNET_HF_BASE_URL overrides the API host for
// tests and proxies.
func ResolveModel(ctx context.Context, repo, hfToken string) (ModelFile, error) {
	base := strings.TrimRight(os.Getenv("SIGNET_HF_BASE_URL"), "/")
	if base == "" {
		base = "https://huggingface.co"
	}
	url := base + "/api/models/" + repo
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ModelFile{}, err
	}
	if hfToken != "" {
		req.Header.Set("authorization", "Bearer "+hfToken)
	}
	resp, err := httpclient.Default().Do(req)
	if err != nil {
		return ModelFile{}, fmt.Errorf("resolve model: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ModelFile{}, fmt.Errorf("resolve model %s: status %d", repo, resp.StatusCode)
	}
	var meta struct {
		Siblings []ModelFile `json:"siblings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return ModelFile{}, fmt.Errorf("decode model metadata: %w", err)
	}

	var best ModelFile
	for _, f := range meta.Siblings {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".gguf") {
			continue
		}
		if f.Size > best.Size {
			best = f
			best.IsGGUF = true
		}
	}
	if best.Name == "" {
		return ModelFile{}, fmt.Errorf("no GGUF file found for %s", repo)
	}
	return best, nil
}

// ModelsDir returns <GlobalDir>/models, the download destination.
func ModelsDir() (string, error) {
	dir, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "models"), nil
}

// Download fetches one model file into dir, resuming an existing partial file
// via HTTP range requests, verifying its SHA-256 when the metadata carried one,
// and renaming into place atomically. A partial file is removed on any
// failure. SIGNET_HF_BASE_URL overrides the host for tests and proxies.
func Download(ctx context.Context, repo string, mf ModelFile, token, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, mf.Name)
	part := dest + ".part"
	defer func() {
		// On any non-nil return the partial is removed; on success the rename
		// has already moved it.
		if _, err := os.Stat(part); err == nil {
			_ = os.Remove(part)
		}
	}()

	offset, err := resumeOffset(part)
	if err != nil {
		return "", err
	}

	base := strings.TrimRight(os.Getenv("SIGNET_HF_BASE_URL"), "/")
	if base == "" {
		base = "https://huggingface.co"
	}
	url := base + "/" + repo + "/resolve/main/" + mf.Name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.Header.Set("authorization", "Bearer "+token)
	}
	if offset > 0 {
		req.Header.Set("range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := httpclient.Default().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// A resumed request must answer 206; a 200 means the server ignored the
	// range and the partial must be restarted.
	restart := false
	switch resp.StatusCode {
	case http.StatusOK:
		restart = offset > 0
	case http.StatusPartialContent:
	default:
		return "", fmt.Errorf("download %s/%s: status %d", repo, mf.Name, resp.StatusCode)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if restart {
		flags |= os.O_TRUNC
		offset = 0
	} else {
		flags |= os.O_APPEND
	}
	f, err := os.OpenFile(part, flags, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}

	if mf.SHA256 != "" {
		if err := verifyChecksum(part, mf.SHA256); err != nil {
			return "", err
		}
	}
	if err := os.Rename(part, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func resumeOffset(part string) (int64, error) {
	fi, err := os.Stat(part)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return fi.Size(), nil
}

func verifyChecksum(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch: got %s want %s", got, want)
	}
	return nil
}
