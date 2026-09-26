package mlclassify

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/vulnetix/belai/internal/config"
)

// modelFileName returns the filesystem-safe directory name for a HuggingFace
// model id: slashes become underscores so the id maps to one flat directory.
func modelFileName(id string) string {
	return strings.ReplaceAll(id, "/", "_")
}

// modelsRoot returns <GlobalDir>/models.
func modelsRoot() (string, error) {
	dir, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "models"), nil
}

// modelCacheDir returns the on-disk cache directory for a HuggingFace model id,
// creating it if necessary.
func modelCacheDir(id string) (string, error) {
	root, err := modelsRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, modelFileName(id))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create model cache dir %q: %w", dir, err)
	}
	return dir, nil
}

// extractEmbedded extracts an embedded model directory to disk once and
// returns the on-disk directory path. Extraction is idempotent: if the four
// required files are already present they are reused, so a warm start never
// rewrites ~20 MB of weights. The spago_model.bin alone is the marker — it is
// the file nn.LoadFromFile actually reads.
func extractEmbedded(spec embeddedSpec) (string, error) {
	root, err := modelsRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, modelFileName(spec.id))

	marker := filepath.Join(dir, "spago_model.bin")
	if info, err := os.Stat(marker); err == nil && !info.IsDir() {
		return dir, nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create model dir %q: %w", dir, err)
	}
	entries, err := fs.ReadDir(spec.fsys, ".")
	if err != nil {
		return "", fmt.Errorf("read embedded model %q: %w", spec.id, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		data, err := fs.ReadFile(spec.fsys, name)
		if err != nil {
			return "", fmt.Errorf("read embedded file %q/%q: %w", spec.id, name, err)
		}
		if err := writeFileAtomic(filepath.Join(dir, name), data); err != nil {
			return "", fmt.Errorf("extract %q/%q: %w", spec.id, name, err)
		}
	}
	// Re-verify the marker landed, so a partial extraction is a hard error
	// rather than a silent next-startup retry against a broken directory.
	if _, err := os.Stat(marker); err != nil {
		return "", fmt.Errorf("embedded model %q extraction incomplete: %w", spec.id, err)
	}
	return dir, nil
}

// writeFileAtomic writes data to path via a temp file + rename so a crash
// mid-extraction never leaves a half-written weights file that later loads.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
