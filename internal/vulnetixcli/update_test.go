package vulnetixcli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLatestRelease(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	wantVersion := "3.108.0"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/Vulnetix/cli/releases/latest" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"tag_name": "v%s", "html_url": "https://example.com/v%s"}`, wantVersion, wantVersion)
	}))
	defer srv.Close()

	t.Setenv("BELAI_GITHUB_API_BASE", srv.URL)
	getenv := func(k string) string {
		if k == "BELAI_GITHUB_API_BASE" {
			return srv.URL
		}
		return ""
	}
	v, url, err := LatestRelease(context.Background(), srv.Client(), getenv, nil)
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if v.String() != "v"+wantVersion {
		t.Fatalf("version = %q, want v%s", v.String(), wantVersion)
	}
	if url != "https://example.com/v"+wantVersion {
		t.Fatalf("url = %q", url)
	}
}

func TestReleaseCacheUsedWithinTTL(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be contacted while cache is fresh")
	}))
	defer srv.Close()

	getenv := func(k string) string { return "" }
	cache := &ReleaseCache{
		Version:   Version{Major: 3, Minor: 1, Patch: 0},
		URL:       "https://example.com/old",
		FetchedAt: time.Now(),
	}
	v, url, err := LatestRelease(context.Background(), srv.Client(), getenv, cache)
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if v.Compare(cache.Version) != 0 {
		t.Fatalf("cached version not returned")
	}
	if url != cache.URL {
		t.Fatalf("cached url not returned")
	}
}
