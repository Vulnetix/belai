package selfupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/version"
	"github.com/vulnetix/belai/internal/vulnetixcli"
)

// releaseServer serves one GitHub releases/latest payload and counts hits.
func releaseServer(t *testing.T, tag string, hits *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits++
		}
		if !strings.HasSuffix(r.URL.Path, "/repos/Vulnetix/belai/releases/latest") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"tag_name": tag,
			"html_url": "https://github.com/Vulnetix/belai/releases/tag/" + tag,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeBinary creates a real file so filepath.EvalSymlinks succeeds during
// install detection.
func fakeBinary(t *testing.T, rel string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func baseOpts(t *testing.T, srv *httptest.Server) Options {
	t.Helper()
	return Options{
		Getenv: func(k string) string {
			if k == "BELAI_GITHUB_API_BASE" {
				return srv.URL
			}
			return ""
		},
		CachePath: filepath.Join(t.TempDir(), "belai-release.json"),
		GOOS:      "linux",
		GOARCH:    "amd64",
	}
}

func TestCheckReportsNewerRelease(t *testing.T) {
	srv := releaseServer(t, "v9.9.9", nil)
	opts := baseOpts(t, srv)
	opts.Current = "v0.1.1"
	opts.ExecPath = fakeBinary(t, "usr/local/bin/belai")

	st := Check(context.Background(), opts)
	if st.Error != "" {
		t.Fatalf("Error = %q", st.Error)
	}
	if !st.Checked || !st.Available {
		t.Fatalf("Checked=%v Available=%v", st.Checked, st.Available)
	}
	if got := st.Latest.String(); got != "v9.9.9" {
		t.Fatalf("Latest = %q", got)
	}
	if note := st.BannerNote(); note != "update v9.9.9 available" {
		t.Fatalf("BannerNote = %q", note)
	}
	notice := st.Notice()
	for _, want := range []string{"v9.9.9", "v0.1.1", "install.sh", "belai-linux-amd64"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("Notice = %q, missing %q", notice, want)
		}
	}
}

func TestCheckUpToDate(t *testing.T) {
	srv := releaseServer(t, "v0.1.1", nil)
	opts := baseOpts(t, srv)
	opts.Current = "v0.1.1"
	opts.ExecPath = fakeBinary(t, "usr/local/bin/belai")

	st := Check(context.Background(), opts)
	if !st.Checked {
		t.Fatal("Checked = false")
	}
	if st.Available {
		t.Fatal("Available = true for an equal version")
	}
	if st.BannerNote() != "" || st.Notice() != "" {
		t.Fatalf("banner=%q notice=%q", st.BannerNote(), st.Notice())
	}
}

func TestCheckSkipsUnstampedBuild(t *testing.T) {
	hits := 0
	srv := releaseServer(t, "v9.9.9", &hits)
	opts := baseOpts(t, srv)
	opts.Current = "dev"

	st := Check(context.Background(), opts)
	if st.Checked || st.Available {
		t.Fatalf("Checked=%v Available=%v for a dev build", st.Checked, st.Available)
	}
	if hits != 0 {
		t.Fatalf("hit GitHub %d times for a dev build", hits)
	}
}

func TestCheckNetworkFailureIsSilent(t *testing.T) {
	opts := Options{
		Current:   "v0.1.1",
		CachePath: filepath.Join(t.TempDir(), "cache.json"),
		Getenv: func(k string) string {
			if k == "BELAI_GITHUB_API_BASE" {
				return "http://127.0.0.1:1"
			}
			return ""
		},
		Client:   &http.Client{Timeout: 500 * time.Millisecond},
		ExecPath: fakeBinary(t, "usr/local/bin/belai"),
	}
	st := Check(context.Background(), opts)
	if st.Available {
		t.Fatal("Available = true after a failed fetch")
	}
	if st.Error == "" {
		t.Fatal("Error = \"\" after a failed fetch")
	}
	if st.BannerNote() != "" || st.Notice() != "" {
		t.Fatal("a failed check must stay out of the banner and panel")
	}
}

func TestCacheAvoidsSecondRequest(t *testing.T) {
	hits := 0
	srv := releaseServer(t, "v9.9.9", &hits)
	opts := baseOpts(t, srv)
	opts.Current = "v0.1.1"
	opts.ExecPath = fakeBinary(t, "usr/local/bin/belai")

	if st := Check(context.Background(), opts); !st.Available {
		t.Fatal("first check: Available = false")
	}
	if st := Check(context.Background(), opts); !st.Available {
		t.Fatal("second check: Available = false")
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1 (the second check must read the cache)", hits)
	}

	// A stale cache is refetched.
	stale := releaseCache{Tag: "v0.0.1", FetchedAt: time.Now().Add(-releaseCacheTTL - time.Minute)}
	data, _ := json.Marshal(stale)
	if err := os.WriteFile(opts.CachePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if st := Check(context.Background(), opts); !st.Available {
		t.Fatal("third check: Available = false")
	}
	if hits != 2 {
		t.Fatalf("hits = %d, want 2 (a stale cache must refetch)", hits)
	}
}

func TestUpgradeHintPerInstallMethod(t *testing.T) {
	cases := []struct {
		method   vulnetixcli.InstallMethod
		goos     string
		wantCmd  string
		wantAsst string
	}{
		{vulnetixcli.InstallBrew, "darwin", "brew upgrade --formula vulnetix/tap/belai", ""},
		{vulnetixcli.InstallScoop, "windows", "scoop update belai", ""},
		{vulnetixcli.InstallGo, "linux", "go install github.com/vulnetix/belai/cmd/belai@latest", ""},
		{
			vulnetixcli.InstallDirect, "linux",
			"curl -fsSL https://raw.githubusercontent.com/Vulnetix/belai/main/install.sh | sh",
			"https://github.com/Vulnetix/belai/releases/download/v9.9.9/belai-linux-arm64",
		},
		{
			vulnetixcli.InstallUnknown, "windows",
			"download https://github.com/Vulnetix/belai/releases/download/v9.9.9/belai-windows-arm64.exe",
			"https://github.com/Vulnetix/belai/releases/download/v9.9.9/belai-windows-arm64.exe",
		},
	}
	for _, tc := range cases {
		cmd, asset := upgradeHint(tc.method, "v9.9.9", Options{GOOS: tc.goos, GOARCH: "arm64"})
		if cmd != tc.wantCmd {
			t.Errorf("%s: command = %q, want %q", tc.method, cmd, tc.wantCmd)
		}
		if asset != tc.wantAsst {
			t.Errorf("%s: asset = %q, want %q", tc.method, asset, tc.wantAsst)
		}
	}
}

func TestCheckDetectsBrewInstall(t *testing.T) {
	srv := releaseServer(t, "v9.9.9", nil)
	opts := baseOpts(t, srv)
	opts.Current = "v0.1.1"
	opts.GOOS = "darwin"
	opts.GOARCH = "arm64"
	opts.ExecPath = fakeBinary(t, "opt/homebrew/Cellar/belai/0.1.1/bin/belai")

	st := Check(context.Background(), opts)
	if st.Method != vulnetixcli.InstallBrew {
		t.Fatalf("Method = %q, want brew", st.Method)
	}
	if !strings.Contains(st.Notice(), "brew upgrade --formula vulnetix/tap/belai") {
		t.Fatalf("Notice = %q", st.Notice())
	}
	if strings.Contains(st.Notice(), "releases/download") {
		t.Fatalf("a brew install must not be told to download a binary: %q", st.Notice())
	}
}

func TestEnabled(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "BELAI_NO_UPDATE_CHECK" {
				return v
			}
			return ""
		}
	}
	if Enabled(env("1"), true) {
		t.Error("BELAI_NO_UPDATE_CHECK=1 must disable the check")
	}
	if Enabled(env(""), false) {
		t.Error("the settings opt-out must disable the check")
	}
	if !Enabled(env(""), true) {
		t.Error("the check must be on by default")
	}
}

func TestAssetURL(t *testing.T) {
	if got := AssetURL("v1.2.3", "linux", "amd64"); got != "https://github.com/Vulnetix/belai/releases/download/v1.2.3/belai-linux-amd64" {
		t.Fatalf("AssetURL = %q", got)
	}
	if got := AssetURL("v1.2.3", "windows", "amd64"); !strings.HasSuffix(got, "belai-windows-amd64.exe") {
		t.Fatalf("AssetURL = %q", got)
	}
}

func TestAssetURLVariant(t *testing.T) {
	orig := version.Variant
	version.Variant = "bert-guardrails"
	defer func() { version.Variant = orig }()
	if got := AssetURL("v1.2.3", "linux", "amd64"); got != "https://github.com/Vulnetix/belai/releases/download/v1.2.3/belai-bert-guardrails-linux-amd64" {
		t.Fatalf("AssetURL = %q", got)
	}
	if got := AssetURL("v1.2.3", "windows", "arm64"); !strings.HasSuffix(got, "belai-bert-guardrails-windows-arm64.exe") {
		t.Fatalf("AssetURL = %q", got)
	}
}

func TestCheckSkipsDirtyWorkingTreeBuild(t *testing.T) {
	hits := 0
	srv := releaseServer(t, "v0.42.2", &hits)
	opts := baseOpts(t, srv)
	opts.Current = "v0.42.2-dirty"
	opts.ExecPath = fakeBinary(t, "usr/local/bin/belai")

	st := Check(context.Background(), opts)
	if st.Checked || st.Available {
		t.Fatalf("Checked=%v Available=%v for a -dirty build", st.Checked, st.Available)
	}
	if hits != 0 {
		t.Fatalf("hit GitHub %d times for a -dirty build", hits)
	}
}
