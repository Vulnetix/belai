package tools

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/repoindex"
)

const cfTestToken = "cf-test-token-0123456789abcdef"

func cfResponse(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: header}
}

// cfFake is a CF tool whose requests are answered by fn and recorded.
func cfFake(fn func(*http.Request) *http.Response) (*Cloudflare, *[]*http.Request) {
	var seen []*http.Request
	return &Cloudflare{
		Token: func() (string, bool) { return cfTestToken, true },
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			seen = append(seen, r)
			return fn(r), nil
		}),
	}, &seen
}

func TestCloudflareURLAcceptsAPIPaths(t *testing.T) {
	for raw, want := range map[string]string{
		"zones":                                  "https://api.cloudflare.com/client/v4/zones",
		"/zones?per_page=50":                     "https://api.cloudflare.com/client/v4/zones?per_page=50",
		"client/v4/zones/abc/dns_records?type=A": "https://api.cloudflare.com/client/v4/zones/abc/dns_records?type=A",
		"https://api.cloudflare.com/client/v4/zones/abc":  "https://api.cloudflare.com/client/v4/zones/abc",
		"accounts/acc/rulesets":                           "https://api.cloudflare.com/client/v4/accounts/acc/rulesets",
		"radar/entities/asns/13335":                       "https://api.cloudflare.com/client/v4/radar/entities/asns/13335",
		"zones/abc/dns_records?name=a.example.com&page=2": "https://api.cloudflare.com/client/v4/zones/abc/dns_records?name=a.example.com&page=2",
	} {
		got, err := cloudflareURL(raw)
		if err != nil {
			t.Errorf("%q: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("%q = %q, want %q", raw, got, want)
		}
	}
}

func TestCloudflareURLRefusesOtherHostsAndTraversal(t *testing.T) {
	for _, raw := range []string{
		"",
		"https://evil.example/client/v4/zones",
		"http://api.cloudflare.com/client/v4/zones",
		"https://api.cloudflare.com.evil.example/client/v4/zones",
		"https://user:pw@api.cloudflare.com/client/v4/zones",
		"https://api.cloudflare.com:8443/client/v4/zones",
		"https://api.cloudflare.com/client/v3/zones",
		"//evil.example/zones",
		"zones/../../user",
		"zones/%2e%2e/user",
		"zones/%252e%252e/user",
		"zones/..",
		"zones/abc dns",
		"zones\\abc",
		"zones#frag",
		"zones\n/x",
		"?per_page=1",
		"/",
		"//",
	} {
		if got, err := cloudflareURL(raw); err == nil {
			t.Errorf("%q was accepted as %q", raw, got)
		}
	}
}

func TestCloudflareRefusesEveryMethodButGet(t *testing.T) {
	cf, seen := cfFake(func(*http.Request) *http.Response { return cfResponse(200, `{}`, nil) })
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE", "HEAD", "post"} {
		if _, err := cf.Execute(context.Background(), map[string]any{"path": "zones", "method": m}); err == nil {
			t.Errorf("method %s was accepted", m)
		}
	}
	if _, err := cf.Execute(context.Background(), map[string]any{"path": "zones", "method": 1}); err == nil {
		t.Error("a method that is not a string was accepted")
	}
	if len(*seen) != 0 {
		t.Fatalf("%d requests were made for refused calls", len(*seen))
	}
	for _, m := range []any{"GET", "get", "", nil} {
		args := map[string]any{"path": "zones"}
		if m != nil {
			args["method"] = m
		}
		if _, err := cf.Execute(context.Background(), args); err != nil {
			t.Errorf("method %v: %v", m, err)
		}
	}
}

func TestCloudflareSendsAGetWithTheBearerAndNeverShowsIt(t *testing.T) {
	cf, seen := cfFake(func(r *http.Request) *http.Response {
		// A server that echoes the header must not put the token in front of
		// the model.
		return cfResponse(200, `{"success":true,"echo":"`+r.Header.Get("Authorization")+`"}`, nil)
	})
	res, err := cf.Execute(context.Background(), map[string]any{"path": "zones/abc/dns_records?per_page=5"})
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 {
		t.Fatalf("%d requests, want 1", len(*seen))
	}
	r := (*seen)[0]
	if r.Method != http.MethodGet || r.URL.String() != "https://api.cloudflare.com/client/v4/zones/abc/dns_records?per_page=5" {
		t.Fatalf("request = %s %s", r.Method, r.URL)
	}
	if r.Header.Get("Authorization") != "Bearer "+cfTestToken {
		t.Fatal("the bearer was not sent")
	}
	if strings.Contains(res.Content, cfTestToken) {
		t.Fatalf("the token reached the result:\n%s", res.Content)
	}
	if res.Kind != KindRemote || cf.Kind() != KindRemote || !cf.Kind().NeedsClassifier() {
		t.Fatal("CF output must be classified")
	}
	if !strings.Contains(res.Content, "HTTP 200") {
		t.Fatalf("no status line:\n%s", res.Content)
	}
}

func TestCloudflareDoesNotFollowRedirects(t *testing.T) {
	cf, seen := cfFake(func(*http.Request) *http.Response {
		return cfResponse(302, "", http.Header{"Location": []string{"https://evil.example/steal"}})
	})
	res, err := cf.Execute(context.Background(), map[string]any{"path": "zones"})
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 {
		t.Fatalf("%d requests, want the redirect never followed", len(*seen))
	}
	if !strings.Contains(res.Content, "HTTP 302") {
		t.Fatalf("result:\n%s", res.Content)
	}
}

func TestCloudflareCapsTheResponse(t *testing.T) {
	cf, _ := cfFake(func(*http.Request) *http.Response { return cfResponse(200, strings.Repeat("x", 5000), nil) })
	cf.MaxBytes = 1000
	res, err := cf.Execute(context.Background(), map[string]any{"path": "zones"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(res.Content, "x") > 1000 || !strings.Contains(res.Content, "response cut at 1000 bytes") {
		t.Fatalf("the response was not capped:\n%d bytes", len(res.Content))
	}
}

func TestCloudflareWithoutATokenFailsClosed(t *testing.T) {
	cf, seen := cfFake(func(*http.Request) *http.Response { return cfResponse(200, `{}`, nil) })
	cf.Token = func() (string, bool) { return "", false }
	if _, err := cf.Execute(context.Background(), map[string]any{"path": "zones"}); err == nil || !strings.Contains(err.Error(), CloudflareTokenEnv) {
		t.Fatalf("err = %v", err)
	}
	if len(*seen) != 0 {
		t.Fatal("a request was made without a token")
	}
}

// The probe reads one zone, never /user/tokens/verify, which answers 401 to a
// zone-scoped token.
func TestCloudflareProbe(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   bool
	}{
		{200, `{"success":true,"result":[]}`, true},
		{200, `{"success":false}`, false},
		{200, `not json`, false},
		{401, `{"success":false}`, false},
		{403, `{"success":false}`, false},
	}
	for _, c := range cases {
		cf, seen := cfFake(func(*http.Request) *http.Response { return cfResponse(c.status, c.body, nil) })
		if got := cloudflareProbe(context.Background(), cf, cfTestToken); got != c.want {
			t.Errorf("%d %s: probe = %v, want %v", c.status, c.body, got, c.want)
		}
		if len(*seen) != 1 {
			t.Fatalf("%d probe requests", len(*seen))
		}
		r := (*seen)[0]
		if r.Method != http.MethodGet || r.URL.String() != "https://api.cloudflare.com/client/v4/zones?per_page=1" {
			t.Fatalf("probe = %s %s", r.Method, r.URL)
		}
		if strings.Contains(r.URL.Path, "tokens/verify") {
			t.Fatal("the probe must not call /user/tokens/verify")
		}
	}
	if CloudflareProbePath != "zones?per_page=1" {
		t.Fatalf("probe path = %q", CloudflareProbePath)
	}
}

func TestDetectOffersCFOnlyWhenItsProbePasses(t *testing.T) {
	look := func(string) (string, bool) { return "", false }
	caps := Detect(context.Background(), CapabilityProbe{LookPath: look, Cloudflare: func(context.Context) bool { return true }}, time.Second)
	if !caps.Has(CFName) {
		t.Fatal("CF must be offered when its probe passes")
	}
	caps = Detect(context.Background(), CapabilityProbe{LookPath: look, Cloudflare: func(context.Context) bool { return false }}, time.Second)
	if caps.Has(CFName) {
		t.Fatal("CF must be absent when its probe fails")
	}
	caps = Detect(context.Background(), CapabilityProbe{LookPath: look}, time.Second)
	if caps.Has(CFName) {
		t.Fatal("CF must be absent with no probe")
	}
}

func TestWithCloudLeavesTheMemoAlone(t *testing.T) {
	base := CapabilitiesOf([]string{"Cat"}, []string{"GH"})
	on := base.withCloud(CFName, true)
	if !on.Has(CFName) || !on.Has("GH") || !on.Has("Cat") || base.Has(CFName) {
		t.Fatal("withCloud must copy, then add")
	}
	if off := on.withCloud(CFName, false); off.Has(CFName) || !on.Has(CFName) {
		t.Fatal("withCloud must copy, then remove")
	}
}

func TestNativeToolsOfferCFAndSubagentsDropIt(t *testing.T) {
	caps := CapabilitiesOf(nil, []string{CFName})
	reg := DefaultWithCaps(t.TempDir(), true, caps, repoindex.Index{})
	if _, ok := reg.Find(CFName); !ok {
		t.Fatal("CF must be offered when detected, read-only surface included")
	}
	if _, ok := reg.Plan().Find(CFName); !ok {
		t.Fatal("CF is read-only and must survive plan mode")
	}
	if _, ok := reg.Plan().Without(CFName).Find(CFName); ok {
		t.Fatal("the subagent narrowing must drop CF")
	}
	if _, ok := DefaultWithCaps(t.TempDir(), false, CapabilitiesOf(nil, nil), repoindex.Index{}).Find(CFName); ok {
		t.Fatal("CF must be absent when not detected")
	}
	if !contains(CatalogueNames(), CFName) {
		t.Fatal("CF is missing from the catalogue names")
	}
}
