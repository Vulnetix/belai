package localinfer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/vulnetix/belai/internal/proc"
)

// HFBinary returns the path to the first Hugging Face CLI it finds: the hf
// binary, then huggingface-cli. The boolean reports whether either was found.
func HFBinary() (string, bool) {
	for _, name := range []string{"hf", "huggingface-cli"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, true
		}
	}
	return "", false
}

// CachedModel is one GGUF discovered in the Hugging Face hub cache.
type CachedModel struct {
	Repo string
	File string
	Path string
}

// HFDownload asks an HF CLI to download the requested quantisation of a repo,
// streaming progress lines through sink. The optional token is passed through
// the child environment as HF_TOKEN; it never appears in argv. The returned
// path is the resolved local GGUF.
//
// The implementation prefers the official CLI because it owns resumable,
// checksummed downloads; Belai no longer does this by hand. It captures stdout
// and parses the last line that looks like a .gguf path. If that heuristic
// fails, it falls back to scanning the HF hub cache for a matching file.
func HFDownload(ctx context.Context, binary, repo, quant, token string, sink func(string)) (string, error) {
	if binary == "" {
		return "", fmt.Errorf("hf binary is required")
	}

	var args []string
	if strings.HasSuffix(filepath.Base(binary), "huggingface-cli") {
		args = []string{"download", repo, "--include", "*" + quant + "*.gguf"}
	} else {
		args = []string{"download", repo, "--include", "*" + quant + "*.gguf"}
	}

	cmd := exec.CommandContext(ctx, binary, args...)
	proc.SetProcessGroup(cmd)
	cmd.Env = append(os.Environ(), "HF_TOKEN="+token)

	tee := proc.NewLineTee(0, func(line string) {
		if sink != nil {
			sink(line)
		}
	})
	cmd.Stdout = tee
	cmd.Stderr = tee

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("hf download start: %w", err)
	}
	err := cmd.Wait()
	tee.Flush()
	if err != nil {
		return "", fmt.Errorf("hf download: %w", err)
	}

	path := parseGGUFPath(tee.Content())
	if path != "" {
		return path, nil
	}

	// Fallback: scan the hub cache ourselves.
	return findHubGGUF(repo, quant)
}

// parseGGUFPath looks for the last absolute-path-looking line that ends in
// .gguf in the combined CLI output.
func parseGGUFPath(output string) string {
	var best string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasSuffix(strings.ToLower(line), ".gguf") {
			continue
		}
		if filepath.IsAbs(line) || strings.HasPrefix(line, "~") {
			best = line
		}
	}
	return best
}

// findHubGGUF walks the Hugging Face hub cache looking for a file matching
// repo and quant.
func findHubGGUF(repo, quant string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("hf cache scan: %w", err)
	}
	dir := filepath.Join(home, ".cache", "huggingface", "hub")
	if v := os.Getenv("HF_HOME"); v != "" {
		dir = filepath.Join(v, "hub")
	}

	var matches []string
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		name := strings.ToLower(info.Name())
		if !strings.HasSuffix(name, ".gguf") {
			return nil
		}
		if quant != "" && !strings.Contains(name, strings.ToLower(quant)) {
			return nil
		}
		// A very light repo match: the cache directory encodes the repo as
		// "models--<org>--<repo>".
		safe := strings.ReplaceAll(repo, "/", "--")
		if !strings.Contains(strings.ToLower(path), strings.ToLower(safe)) {
			return nil
		}
		matches = append(matches, path)
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("hf cache scan: no %s GGUF found for %s", quant, repo)
	}
	return matches[0], nil
}

// HFCacheScan returns GGUF files currently present in the HF hub cache.
func HFCacheScan(ctx context.Context, binary string) ([]CachedModel, error) {
	if binary == "" {
		return scanHubCache(nil)
	}

	// Try the modern "cache scan" form first, then the legacy "scan-cache".
	var args []string
	if strings.HasSuffix(filepath.Base(binary), "huggingface-cli") {
		args = []string{"scan-cache"}
	} else {
		args = []string{"cache", "scan"}
	}

	cmd := exec.CommandContext(ctx, binary, args...)
	proc.SetProcessGroup(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil || len(out) == 0 {
		return scanHubCache(nil)
	}
	return scanHubCache(out)
}

// scanHubCache parses CLI cache-scan output or, when out is nil, walks the
// hub cache directly.
func scanHubCache(out []byte) ([]CachedModel, error) {
	var models []CachedModel
	if len(out) == 0 {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir := filepath.Join(home, ".cache", "huggingface", "hub")
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(strings.ToLower(info.Name()), ".gguf") {
				return nil
			}
			models = append(models, CachedModel{
				Repo: guessRepoFromPath(path),
				File: info.Name(),
				Path: path,
			})
			return nil
		})
		return models, nil
	}

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(strings.ToLower(line), ".gguf") {
			continue
		}
		fields := strings.Fields(line)
		for _, f := range fields {
			if strings.HasSuffix(strings.ToLower(f), ".gguf") {
				models = append(models, CachedModel{
					Repo: guessRepoFromPath(f),
					File: filepath.Base(f),
					Path: f,
				})
			}
		}
	}
	return models, nil
}

// guessRepoFromPath extracts a rough repo id from an HF hub cache path. It is
// a human-readable hint, not a canonical parse. The cache nests files under
// snapshots/<hash>/ or blobs/, so the models--<org>--<repo> directory may be
// several levels above the file rather than its immediate parent.
func guessRepoFromPath(path string) string {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if strings.HasPrefix(part, "models--") {
			parts := strings.SplitN(strings.TrimPrefix(part, "models--"), "--", 2)
			if len(parts) == 2 {
				return parts[0] + "/" + parts[1]
			}
		}
	}
	return ""
}

// HFWhoami reports the currently authenticated HF user, if any.
func HFWhoami(ctx context.Context, binary string) (string, bool) {
	if binary == "" {
		return "", false
	}
	var args []string
	if strings.HasSuffix(filepath.Base(binary), "huggingface-cli") {
		args = []string{"whoami"}
	} else {
		args = []string{"auth", "whoami"}
	}
	out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line, true
		}
	}
	return "", false
}
