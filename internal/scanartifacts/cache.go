package scanartifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/session"
)

// SchemaVersion bumps when the parsing contract changes.
const SchemaVersion = 1

// CachePath returns the path for a project's cached summary.
func CachePath(workdir string) (string, error) {
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return "", err
	}
	gd, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gd, "scan-cache", session.WorkdirKey(abs)+".json"), nil
}

// LoadCached returns a cached summary when the fingerprint matches.
func LoadCached(workdir string) (Summary, bool, error) {
	zero := Summary{}
	path, err := CachePath(workdir)
	if err != nil {
		return zero, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return zero, false, nil
		}
		return zero, false, err
	}
	var s Summary
	if err := json.Unmarshal(data, &s); err != nil {
		return zero, false, nil // degrade on parse failure
	}
	if s.SchemaVersion != SchemaVersion {
		return zero, false, nil
	}
	want, err := fingerprint(workdir)
	if err != nil {
		return zero, false, err
	}
	if s.Fingerprint != want {
		return zero, false, nil
	}
	return s, true, nil
}

// Refresh scans workdir/.vulnetix/ and returns an up-to-date summary, writing
// it to the global cache.
func Refresh(ctx context.Context, workdir string) (Summary, error) {
	zero := Summary{}
	dir := filepath.Join(workdir, ".vulnetix")
	arts, err := Enumerate(dir)
	if err != nil {
		return zero, err
	}
	fp, err := fingerprintFromArtifacts(arts)
	if err != nil {
		return zero, err
	}
	summary := Summarize(ctx, workdir, arts)
	summary.Fingerprint = fp
	summary.SchemaVersion = SchemaVersion

	if err := saveSummary(summary); err != nil {
		// Cache failures are non-fatal.
		_ = err
	}
	return summary, nil
}

// fingerprint builds a stat-only cache key.
func fingerprint(workdir string) (string, error) {
	arts, err := Enumerate(filepath.Join(workdir, ".vulnetix"))
	if err != nil {
		return "", err
	}
	return fingerprintFromArtifacts(arts)
}

func fingerprintFromArtifacts(arts []Artifact) (string, error) {
	parts := make([]string, 0, len(arts))
	for _, a := range arts {
		parts = append(parts, fmt.Sprintf("%s|%d|%d", a.Rel, a.Size, a.ModTime.UnixNano()))
	}
	sort.Strings(parts)
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func saveSummary(s Summary) error {
	path, err := CachePath(s.Dir)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteGlobalFileAtomic(path, data)
}
