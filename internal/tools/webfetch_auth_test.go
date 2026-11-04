package tools

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/vaultenv"
)

const webFetchGHToken = "ghs_webfetchvaulttoken0000001"

// publicDNS answers every host with a public address, so validateHost passes
// without the network.
func publicDNS(t *testing.T) {
	t.Helper()
	old := lookupIP
	lookupIP = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("140.82.112.6")}, nil }
	t.Cleanup(func() { lookupIP = old })
}

type seenRequest struct {
	url, auth string
}

// recordingClient answers each request from route and records the URL and the
// Authorization header it carried.
func recordingClient(seen *[]seenRequest, route func(*http.Request) (int, string, string)) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		*seen = append(*seen, seenRequest{url: r.URL.String(), auth: r.Header.Get("Authorization")})
		status, location, body := route(r)
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", "application/json")
		if location != "" {
			rec.Header().Set("Location", location)
		}
		rec.WriteHeader(status)
		_, _ = rec.WriteString(body)
		return rec.Result(), nil
	})}
}

func okRoute(body string) func(*http.Request) (int, string, string) {
	return func(*http.Request) (int, string, string) { return http.StatusOK, "", body }
}

func TestWebFetchAuthenticatesTheGitHubAPIOnly(t *testing.T) {
	publicDNS(t)
	withVault(t, vaultenv.Var{Name: "GITHUB_TOKEN", Value: webFetchGHToken})
	for _, c := range []struct {
		url  string
		auth bool
	}{
		{"https://api.github.com/repos/vulnetix/belai", true},
		{"https://API.GitHub.com/rate_limit", true},
		{"http://api.github.com/repos/vulnetix/belai", false},
		{"https://api.github.com:8443/repos/vulnetix/belai", false},
		{"https://github.com/vulnetix/belai", false},
		{"https://uploads.github.com/x", false},
		{"https://api.github.com.example.com/x", false},
		{"https://example.com/?u=api.github.com", false},
	} {
		var seen []seenRequest
		wf := &WebFetch{Client: recordingClient(&seen, okRoute(`{"ok":true}`))}
		if _, err := wf.Execute(context.Background(), map[string]any{"url": c.url}); err != nil {
			t.Fatalf("%s: %v", c.url, err)
		}
		if len(seen) != 1 {
			t.Fatalf("%s: %d requests", c.url, len(seen))
		}
		want := ""
		if c.auth {
			want = "Bearer " + webFetchGHToken
		}
		if seen[0].auth != want {
			t.Errorf("%s: Authorization = %q, want %q", c.url, seen[0].auth, want)
		}
	}
}

func TestWebFetchSendsNoAuthorizationWithoutAVaultToken(t *testing.T) {
	publicDNS(t)
	withVault(t, vaultenv.Var{Name: "NPM_TOKEN", Value: "npm-token-value-1"})
	var seen []seenRequest
	wf := &WebFetch{Client: recordingClient(&seen, okRoute(`{}`))}
	if _, err := wf.Execute(context.Background(), map[string]any{"url": "https://api.github.com/rate_limit"}); err != nil {
		t.Fatal(err)
	}
	if seen[0].auth != "" {
		t.Fatalf("Authorization = %q without a vault GitHub token", seen[0].auth)
	}
}

// A redirect off api.github.com drops the token, and it stays dropped for the
// rest of the chain, even back on api.github.com.
func TestWebFetchDropsTheTokenWhenARedirectLeavesTheGitHubAPI(t *testing.T) {
	publicDNS(t)
	withVault(t, vaultenv.Var{Name: "GH_TOKEN", Value: webFetchGHToken})
	var seen []seenRequest
	wf := &WebFetch{Client: recordingClient(&seen, func(r *http.Request) (int, string, string) {
		switch r.URL.Host {
		case "api.github.com":
			if r.URL.Path == "/repos/o/r/tarball" {
				return http.StatusFound, "https://api.github.com.evil.example/steal", ""
			}
			if r.URL.Path == "/back" {
				return http.StatusOK, "", `{"back":true}`
			}
			return http.StatusMovedPermanently, "https://api.github.com/repos/o/r/tarball", ""
		case "api.github.com.evil.example":
			return http.StatusFound, "https://api.github.com/back", ""
		}
		return http.StatusNotFound, "", ""
	})}
	if _, err := wf.Execute(context.Background(), map[string]any{"url": "https://api.github.com/repos/o/r/zipball"}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 4 {
		t.Fatalf("hops %+v", seen)
	}
	want := []string{"Bearer " + webFetchGHToken, "Bearer " + webFetchGHToken, "", ""}
	for i, s := range seen {
		if s.auth != want[i] {
			t.Errorf("hop %d %s: Authorization = %q, want %q", i, s.url, s.auth, want[i])
		}
	}
}

// What the model reads never holds the token, even when the API echoes it.
func TestWebFetchNeverShowsTheToken(t *testing.T) {
	publicDNS(t)
	withVault(t, vaultenv.Var{Name: "GITHUB_TOKEN", Value: webFetchGHToken})
	var seen []seenRequest
	wf := &WebFetch{Client: recordingClient(&seen, func(r *http.Request) (int, string, string) {
		return http.StatusOK, "", `{"echo":"` + r.Header.Get("Authorization") + `"}`
	})}
	res, err := wf.Execute(context.Background(), map[string]any{"url": "https://api.github.com/echo"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, webFetchGHToken) {
		t.Fatalf("the token reached the result: %q", res.Content)
	}
	if !strings.Contains(res.Content, "Bearer [") {
		t.Fatalf("expected a placeholder: %q", res.Content)
	}
}

// The default client keeps its redirect guard when the token is set: a redirect
// to an internal address is still refused.
func TestWebFetchAuthKeepsTheRedirectGuard(t *testing.T) {
	publicDNS(t)
	withVault(t, vaultenv.Var{Name: "GITHUB_TOKEN", Value: webFetchGHToken})
	var seen []seenRequest
	wf := &WebFetch{Client: recordingClient(&seen, func(*http.Request) (int, string, string) {
		return http.StatusFound, "http://169.254.169.254/latest/", ""
	})}
	_, err := wf.Execute(context.Background(), map[string]any{"url": "https://api.github.com/x"})
	if err == nil || !strings.Contains(err.Error(), "redirect rejected") {
		t.Fatalf("err = %v, want a redirect rejection", err)
	}
	if len(seen) != 1 {
		t.Fatalf("hops = %d", len(seen))
	}
}
