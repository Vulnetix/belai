// Package localinfer manages a local inference server for the security
// classifier: detect a running server or a launchable binary, launch it with a
// health check, resolve HuggingFace model metadata, and shut the server down
// with Signet. The classifier routes to the local server through the existing
// "ollama" provider seam: set classifier.provider to ollama and OLLAMA_HOST to
// the server's base URL.
package localinfer

import (
	"context"
	"encoding/json"
	"fmt"
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
