package vulnetixcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/config"
)

// releaseCacheTTL is how long a GitHub release probe remains valid.
const releaseCacheTTL = 6 * time.Hour

// ReleaseCache stores the latest known release to avoid hitting GitHub's
// unauthenticated rate limit.
type ReleaseCache struct {
	Version   Version   `json:"version"`
	URL       string    `json:"url"`
	FetchedAt time.Time `json:"fetched_at"`
}

// cachePath returns the path to the release cache.
func cachePath() (string, error) {
	gd, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gd, "vulnetix-release.json"), nil
}

// loadCache reads the cached release, if any.
func loadCache() (*ReleaseCache, error) {
	path, err := cachePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var c ReleaseCache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// saveCache writes the cached release.
func saveCache(c ReleaseCache) error {
	path, err := cachePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteGlobalFileAtomic(path, data)
}

// LatestRelease returns the newest Vulnetix CLI version from GitHub. It
// caches the result for six hours. The optional cache argument is populated
// or reused; pass nil to use the default on-disk cache.
func LatestRelease(ctx context.Context, client *http.Client, getenv func(string) string, cache *ReleaseCache) (Version, string, error) {
	if cache == nil {
		if c, err := loadCache(); err == nil && c != nil && time.Since(c.FetchedAt) < releaseCacheTTL {
			cache = c
		}
	}
	if cache != nil && !cache.Version.IsZero() && time.Since(cache.FetchedAt) < releaseCacheTTL {
		return cache.Version, cache.URL, nil
	}

	base := "https://api.github.com"
	if getenv != nil && getenv("SIGNET_GITHUB_API_BASE") != "" {
		base = strings.TrimRight(getenv("SIGNET_GITHUB_API_BASE"), "/")
	}

	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/repos/Vulnetix/cli/releases/latest", nil)
	if err != nil {
		return Version{}, "", err
	}
	req.Header.Set("accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return Version{}, "", fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Version{}, "", fmt.Errorf("github returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Version{}, "", err
	}
	var payload struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Version{}, "", fmt.Errorf("decode release: %w", err)
	}
	v, _ := ParseVersion(payload.TagName)
	c := ReleaseCache{
		Version:   v,
		URL:       payload.HTMLURL,
		FetchedAt: time.Now(),
	}
	_ = saveCache(c)
	return v, payload.HTMLURL, nil
}
