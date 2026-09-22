// Package selfupdate checks the Signet GitHub releases for a newer version of
// this binary and renders the upgrade command that matches how the binary was
// installed (Homebrew tap, Scoop bucket, `go install`, or a direct download).
//
// It is a read-only check: nothing is downloaded or executed. The result is
// surfaced in the TUI banner and as one signet-panel notice, and the user
// runs the upgrade themselves.
//
// Version parsing and install-method detection are reused from
// internal/vulnetixcli: both are generic path/semver helpers that happen to
// live there first, and duplicating them would let the two checks drift.
package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/version"
	"github.com/vulnetix/signet/internal/vulnetixcli"
)

// releaseCacheTTL is how long a release probe stays valid. GitHub's
// unauthenticated API allows 60 requests an hour per IP and a session may
// start many times an hour, so the answer is cached on disk.
const releaseCacheTTL = 6 * time.Hour

// repo is the releases repository this binary is built from.
const (
	repoOwner = "Vulnetix"
	repoName  = "signet"
)

// Status is the outcome of an update check.
type Status struct {
	// Checked reports whether a comparison actually happened. It is false for
	// an unstamped build, an opted-out check, or a failed fetch.
	Checked bool
	// Available reports whether Latest is newer than Current.
	Available bool
	Current   vulnetixcli.Version
	Latest    vulnetixcli.Version
	// Tag is the release tag as GitHub reported it, used to build asset URLs.
	Tag string
	// URL is the release page.
	URL string
	// Method is how this binary was installed.
	Method vulnetixcli.InstallMethod
	// Command is the upgrade command for Method, ready to paste.
	Command string
	// AssetURL is the direct download for this GOOS/GOARCH. It is set only
	// when no package manager owns the binary.
	AssetURL string
	// Error is a human-readable reason the check did not complete.
	Error string
}

// Options configures a check. The zero value is the production configuration.
type Options struct {
	// Current overrides the running binary's version. Empty means
	// version.Version.
	Current string
	// Client is the HTTP client. Nil means a 10s-timeout client.
	Client *http.Client
	// Getenv overrides environment lookup. Nil means os.Getenv.
	Getenv func(string) string
	// ExecPath overrides the path used for install detection. Empty means
	// os.Executable().
	ExecPath string
	// CachePath overrides the on-disk release cache. Empty means the file in
	// the global config directory.
	CachePath string
	// SkipCache forces a network fetch and still refreshes the cache.
	SkipCache bool
	// GOOS and GOARCH override the platform used to name the release asset.
	GOOS, GOARCH string
}

// releaseCache is the on-disk memo of the last GitHub answer.
type releaseCache struct {
	Tag       string    `json:"tag"`
	URL       string    `json:"url"`
	FetchedAt time.Time `json:"fetched_at"`
}

// Enabled reports whether the update check may run. The env var is the
// escape hatch for air-gapped and CI use; settings carry the durable opt-out.
func Enabled(getenv func(string) string, settingsEnabled bool) bool {
	if getenv == nil {
		getenv = os.Getenv
	}
	switch strings.TrimSpace(getenv("SIGNET_NO_UPDATE_CHECK")) {
	case "1", "true", "yes":
		return false
	}
	return settingsEnabled
}

// Check compares the running version against the newest GitHub release. It
// never returns an error: a failed check is a Status carrying Error, because
// no startup path should fail because GitHub was unreachable.
func Check(ctx context.Context, opts Options) Status {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}

	cur := opts.Current
	if cur == "" {
		cur = version.Version
	}
	current, ok := vulnetixcli.ParseVersion(cur)
	if !ok || isLocalBuild(cur) {
		// A `go build` with no -ldflags stamp ("dev"), or a working-tree
		// build (`just build` stamps "-dirty"). There is nothing meaningful
		// to compare, and telling a source build to run `brew upgrade`
		// would be wrong.
		return Status{}
	}

	st := Status{Checked: true, Current: current}
	st.Method, _ = detectMethod(opts, getenv)

	tag, url, err := latestRelease(ctx, opts, getenv)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	latest, ok := vulnetixcli.ParseVersion(tag)
	if !ok {
		st.Error = "unrecognised release tag " + tag
		return st
	}
	st.Latest = latest
	st.Tag = tag
	st.URL = url
	st.Available = latest.Compare(current) > 0
	if st.Available {
		st.Command, st.AssetURL = upgradeHint(st.Method, tag, opts)
	}
	return st
}

// isLocalBuild reports whether the version stamp came from a working tree
// rather than a release. `just build` stamps "<tag>-dirty" off an unclean
// tree; that binary is ahead of, not behind, the release it names.
func isLocalBuild(v string) bool {
	return strings.Contains(strings.ToLower(v), "-dirty")
}

// detectMethod resolves the running binary and classifies its install path.
func detectMethod(opts Options, getenv func(string) string) (vulnetixcli.InstallMethod, string) {
	path := opts.ExecPath
	if path == "" {
		p, err := os.Executable()
		if err != nil {
			return vulnetixcli.InstallUnknown, ""
		}
		path = p
	}
	m, prefix := vulnetixcli.DetectInstall(path, getenv)
	if m == "" {
		return vulnetixcli.InstallUnknown, ""
	}
	return m, prefix
}

// upgradeHint returns the paste-ready upgrade command for an install method,
// plus the direct release asset for this platform when no package manager
// owns the binary.
func upgradeHint(m vulnetixcli.InstallMethod, tag string, opts Options) (string, string) {
	goos := opts.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := opts.GOARCH
	if goarch == "" {
		goarch = runtime.GOARCH
	}

	switch m {
	case vulnetixcli.InstallBrew:
		// The tapped name, not bare "signet": Homebrew also knows an
		// unrelated cask by that name.
		return "brew upgrade --formula vulnetix/tap/signet", ""
	case vulnetixcli.InstallScoop:
		return "scoop update signet", ""
	case vulnetixcli.InstallGo:
		return "go install github.com/vulnetix/signet/cmd/signet@latest", ""
	}

	asset := AssetURL(tag, goos, goarch)
	if goos == "windows" {
		return "download " + asset, asset
	}
	return "curl -fsSL https://raw.githubusercontent.com/Vulnetix/signet/main/install.sh | sh", asset
}

// AssetURL returns the release download URL for one platform. Asset names
// match .github/workflows/release.yml: signet[-variant]-<goos>-<goarch>[.exe].
// The variant suffix comes from version.Variant so a guardrails binary updates
// to a guardrails binary and a vanilla binary keeps the plain signet asset.
func AssetURL(tag, goos, goarch string) string {
	name := "signet"
	if version.Variant != "" {
		name += "-" + version.Variant
	}
	name += fmt.Sprintf("-%s-%s", goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/%s", repoOwner, repoName, tag, name)
}

// latestRelease returns the newest release tag and page URL, from the disk
// cache when it is fresh.
func latestRelease(ctx context.Context, opts Options, getenv func(string) string) (string, string, error) {
	path := opts.CachePath
	if path == "" {
		p, err := cachePath()
		if err == nil {
			path = p
		}
	}
	if !opts.SkipCache && path != "" {
		if c, err := loadCache(path); err == nil && c != nil && c.Tag != "" && time.Since(c.FetchedAt) < releaseCacheTTL {
			return c.Tag, c.URL, nil
		}
	}

	base := "https://api.github.com"
	if v := strings.TrimSpace(getenv("SIGNET_GITHUB_API_BASE")); v != "" {
		base = strings.TrimRight(v, "/")
	}

	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/releases/latest", base, repoOwner, repoName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("accept", "application/vnd.github+json")
	req.Header.Set("user-agent", version.UserAgent())

	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("github returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	var payload struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", "", fmt.Errorf("decode release: %w", err)
	}
	if strings.TrimSpace(payload.TagName) == "" {
		return "", "", errors.New("release has no tag")
	}
	if path != "" {
		_ = saveCache(path, releaseCache{Tag: payload.TagName, URL: payload.HTMLURL, FetchedAt: time.Now()})
	}
	return payload.TagName, payload.HTMLURL, nil
}

// cachePath returns the default release cache file.
func cachePath() (string, error) {
	gd, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gd, "signet-release.json"), nil
}

func loadCache(path string) (*releaseCache, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var c releaseCache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func saveCache(path string, c releaseCache) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteGlobalFileAtomic(path, data)
}

// BannerNote is the short suffix for the banner's version line, or "" when
// nothing is available.
func (s Status) BannerNote() string {
	if !s.Available || s.Latest.IsZero() {
		return ""
	}
	return "update " + s.Latest.String() + " available"
}

// Notice is the multi-line signet-panel message, or "" when there is nothing
// to say. It states both versions, how to update on this machine, and where
// the release lives.
func (s Status) Notice() string {
	if !s.Available || s.Latest.IsZero() {
		return ""
	}
	lines := []string{
		fmt.Sprintf("signet %s is available (running %s)", s.Latest.String(), s.Current.String()),
	}
	if s.Command != "" {
		lines = append(lines, "update: "+s.Command)
	}
	if s.AssetURL != "" && !strings.Contains(s.Command, s.AssetURL) {
		lines = append(lines, "binary: "+s.AssetURL)
	}
	if s.URL != "" {
		lines = append(lines, "release notes: "+s.URL)
	}
	return strings.Join(lines, "\n")
}
