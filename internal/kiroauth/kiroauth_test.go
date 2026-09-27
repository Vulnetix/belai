package kiroauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeOIDC is an httptest SSO-OIDC endpoint. tokenReplies are served in order
// to /token; the last one repeats.
type fakeOIDC struct {
	t            *testing.T
	mu           sync.Mutex
	tokenReplies []func(w http.ResponseWriter, body map[string]string)
	tokenCalls   int
	registered   map[string]any
	authorized   map[string]string
}

func (f *fakeOIDC) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/client/register":
			_ = json.NewDecoder(r.Body).Decode(&f.registered)
			fmt.Fprint(w, `{"clientId":"cid","clientSecret":"csecret","clientSecretExpiresAt":4102444800}`)
		case "/device_authorization":
			_ = json.NewDecoder(r.Body).Decode(&f.authorized)
			fmt.Fprint(w, `{"deviceCode":"dcode","userCode":"ABCD-EFGH","verificationUri":"https://device.sso.us-east-1.amazonaws.com/","verificationUriComplete":"https://device.sso.us-east-1.amazonaws.com/?user_code=ABCD-EFGH","expiresIn":600,"interval":1}`)
		case "/token":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			i := f.tokenCalls
			if i >= len(f.tokenReplies) {
				i = len(f.tokenReplies) - 1
			}
			f.tokenCalls++
			f.tokenReplies[i](w, body)
		default:
			http.NotFound(w, r)
		}
	}))
}

func oidcErr(status int, code string) func(http.ResponseWriter, map[string]string) {
	return func(w http.ResponseWriter, _ map[string]string) {
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"error":%q,"error_description":"x"}`, code)
	}
}

func TestDeviceLoginPendingSlowDownThenSuccess(t *testing.T) {
	f := &fakeOIDC{t: t}
	var gotDevice atomic.Value
	f.tokenReplies = []func(http.ResponseWriter, map[string]string){
		oidcErr(400, "authorization_pending"),
		oidcErr(400, "slow_down"),
		func(w http.ResponseWriter, body map[string]string) {
			gotDevice.Store(body["deviceCode"] + "|" + body["grantType"])
			fmt.Fprint(w, `{"accessToken":"at","refreshToken":"rt-secret-xyz","expiresIn":3600}`)
		},
	}
	srv := f.server()
	defer srv.Close()

	d := DeviceLogin{BaseURL: srv.URL, Client: srv.Client(), SlowDown: time.Millisecond, StartURL: "https://acme.awsapps.com/start", Region: "eu-west-1", ProfileARN: "arn:aws:codewhisperer:us-east-1:1:profile/X"}
	g, err := d.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if g.UserCode != "ABCD-EFGH" || !strings.Contains(g.BrowseURL(), "user_code=") {
		t.Fatalf("grant = %v", g)
	}
	if strings.Contains(fmt.Sprintf("%v %#v", g, g), "dcode") || strings.Contains(fmt.Sprintf("%v", g), "csecret") {
		t.Fatal("grant formatting leaked a secret")
	}
	if f.registered["issuerUrl"] != "https://acme.awsapps.com/start" || f.authorized["startUrl"] != "https://acme.awsapps.com/start" {
		t.Fatalf("start url not sent: %v %v", f.registered, f.authorized)
	}
	g.Interval = 0 // keep the test fast; slow_down adds only 1ms
	l, err := d.pollWith(context.Background(), g, time.Millisecond)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if gotDevice.Load() != "dcode|urn:ietf:params:oauth:grant-type:device_code" {
		t.Fatalf("token request = %v", gotDevice.Load())
	}
	want := Login{RefreshToken: "rt-secret-xyz", ClientID: "cid", ClientSecret: "csecret", ClientSecretExpiresAt: 4102444800, Region: "eu-west-1", StartURL: "https://acme.awsapps.com/start", ProfileARN: "arn:aws:codewhisperer:us-east-1:1:profile/X"}
	if l != want {
		t.Fatalf("login = %#v", l)
	}
	back, err := ParseLogin(l.Encode())
	if err != nil || back != want {
		t.Fatalf("ParseLogin(Encode) = %v, %v", back, err)
	}
	if strings.Contains(fmt.Sprintf("%v %#v", l, l), "rt-secret-xyz") || strings.Contains(fmt.Sprintf("%v", l), "csecret") {
		t.Fatal("login formatting leaked a secret")
	}
}

func TestDeviceLoginDeniedAndExpired(t *testing.T) {
	for code, want := range map[string]error{"access_denied": ErrDeviceDenied, "expired_token": ErrDeviceExpired} {
		f := &fakeOIDC{t: t, tokenReplies: []func(http.ResponseWriter, map[string]string){oidcErr(400, code)}}
		srv := f.server()
		d := DeviceLogin{BaseURL: srv.URL, Client: srv.Client()}
		g, err := d.Start(context.Background())
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		g.Interval = 0
		if _, err := d.pollWith(context.Background(), g, time.Millisecond); !errors.Is(err, want) {
			t.Fatalf("%s: got %v want %v", code, err, want)
		}
		srv.Close()
	}
}

func TestDeviceLoginValidatesSettings(t *testing.T) {
	cases := []DeviceLogin{
		{StartURL: "http://insecure.example/start"},
		{Region: "us-east-1.evil.com/"},
		{APIRegion: "not a region"},
		{BaseURL: "https://oidc.us-east-1.amazonaws.com.evil.com"},
	}
	for _, d := range cases {
		if _, err := d.Start(context.Background()); err == nil {
			t.Fatalf("Start(%+v) = nil error", d)
		}
	}
}

func TestHostPinning(t *testing.T) {
	good := []string{"https://q.us-east-1.amazonaws.com", "https://codewhisperer.us-east-1.amazonaws.com/", "http://127.0.0.1:4000", "http://localhost:1"}
	bad := []string{"http://q.us-east-1.amazonaws.com", "https://q.us-east-1.amazonaws.com.evil.com", "https://evil.com", "https://q.us-east-1.amazonaws.com:8443", "https://user@q.us-east-1.amazonaws.com", "ftp://127.0.0.1"}
	for _, u := range good {
		if !AllowedAPIURL(u) {
			t.Errorf("AllowedAPIURL(%q) = false", u)
		}
	}
	for _, u := range bad {
		if AllowedAPIURL(u) {
			t.Errorf("AllowedAPIURL(%q) = true", u)
		}
	}
	if !AllowedOIDCURL("https://oidc.eu-west-1.amazonaws.com") || AllowedOIDCURL("https://q.us-east-1.amazonaws.com") {
		t.Error("AllowedOIDCURL mismatch")
	}
	if APIBaseURL("../evil") != "https://q.us-east-1.amazonaws.com" {
		t.Error("APIBaseURL accepted an invalid region")
	}
}

func testLogin() Login {
	return Login{RefreshToken: "rt1", ClientID: "cid", ClientSecret: "cs", Region: "us-east-1", ProfileARN: "arn:p"}
}

func TestRefresherCachesAndRotates(t *testing.T) {
	f := &fakeOIDC{t: t}
	var seen []string
	f.tokenReplies = []func(http.ResponseWriter, map[string]string){
		func(w http.ResponseWriter, body map[string]string) {
			seen = append(seen, body["grantType"]+":"+body["refreshToken"])
			fmt.Fprint(w, `{"accessToken":"at1","refreshToken":"rt2","expiresIn":3600}`)
		},
		func(w http.ResponseWriter, body map[string]string) {
			seen = append(seen, body["grantType"]+":"+body["refreshToken"])
			fmt.Fprint(w, `{"accessToken":"at2","expiresIn":3600}`)
		},
	}
	srv := f.server()
	defer srv.Close()

	now := time.Unix(1_700_000_000, 0)
	r := NewRefresher(srv.Client()).WithBaseURL(srv.URL)
	r.now = func() time.Time { return now }
	var rotOld, rotNew string
	r.SetOnRotate(func(o, n string) { rotOld, rotNew = o, n })

	stored := testLogin().Encode()
	tok, err := r.Token(context.Background(), stored)
	if err != nil || tok.Value != "at1" || tok.ProfileARN != "arn:p" {
		t.Fatalf("Token = %+v, %v", tok, err)
	}
	if rotOld != stored {
		t.Fatalf("OnRotate old = %q", rotOld)
	}
	nl, err := ParseLogin(rotNew)
	if err != nil || nl.RefreshToken != "rt2" || nl.ClientID != "cid" {
		t.Fatalf("OnRotate new = %v, %v", nl, err)
	}
	// Cached: no second call.
	if tok, _ := r.Token(context.Background(), stored); tok.Value != "at1" || f.tokenCalls != 1 {
		t.Fatalf("cache miss: %+v calls=%d", tok, f.tokenCalls)
	}
	// Near expiry the refresh uses the rotated token even when asked with
	// the old stored login.
	now = now.Add(59 * time.Minute)
	if tok, err := r.Token(context.Background(), stored); err != nil || tok.Value != "at2" {
		t.Fatalf("refresh = %+v, %v", tok, err)
	}
	if len(seen) != 2 || seen[0] != "refresh_token:rt1" || seen[1] != "refresh_token:rt2" {
		t.Fatalf("refresh requests = %v", seen)
	}
}

func TestRefresherReloginRequired(t *testing.T) {
	f := &fakeOIDC{t: t, tokenReplies: []func(http.ResponseWriter, map[string]string){oidcErr(400, "invalid_grant")}}
	srv := f.server()
	defer srv.Close()
	r := NewRefresher(srv.Client()).WithBaseURL(srv.URL)
	if _, err := r.Token(context.Background(), testLogin().Encode()); !errors.Is(err, ErrReloginRequired) {
		t.Fatalf("got %v", err)
	}

	expired := testLogin()
	expired.ClientSecretExpiresAt = 1
	if _, err := r.Token(context.Background(), expired.Encode()); !errors.Is(err, ErrReloginRequired) {
		t.Fatalf("expired registration: got %v", err)
	}
	if _, err := r.Token(context.Background(), "not json"); err == nil {
		t.Fatal("malformed login accepted")
	}
}

func TestRefresherRefusesUnpinnedHost(t *testing.T) {
	r := NewRefresher(nil).WithBaseURL("https://evil.example")
	if _, err := r.Token(context.Background(), testLogin().Encode()); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("got %v", err)
	}
	bad := testLogin()
	bad.Region = "x/../evil"
	if _, err := ParseLogin(bad.Encode()); err == nil {
		t.Fatal("ParseLogin accepted an invalid region")
	}
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestImportKiroCache(t *testing.T) {
	home := t.TempDir()
	if _, err := ImportKiroCache(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no cache: got %v", err)
	}
	dir := filepath.Join(home, ".aws", "sso", "cache")
	writeFile(t, filepath.Join(dir, "kiro-auth-token.json"), `{"accessToken":"a","refreshToken":"r","expiresAt":"2030-01-01T00:00:00Z","clientIdHash":"abc123","authMethod":"IdC","provider":"BuilderId","region":"us-east-1"}`)
	if _, err := ImportKiroCache(home); err == nil {
		t.Fatal("missing registration accepted")
	}
	writeFile(t, filepath.Join(dir, "abc123.json"), `{"clientId":"cid","clientSecret":"cs","expiresAt":"2030-01-01T00:00:00Z"}`)
	l, err := ImportKiroCache(home)
	if err != nil {
		t.Fatalf("ImportKiroCache: %v", err)
	}
	if l.RefreshToken != "r" || l.ClientID != "cid" || l.ClientSecret != "cs" || l.Region != "us-east-1" || l.ClientSecretExpiresAt == 0 {
		t.Fatalf("login = %#v", l)
	}

	writeFile(t, filepath.Join(dir, "kiro-auth-token.json"), `{"refreshToken":"r","authMethod":"social","provider":"Github","profileArn":"arn"}`)
	if _, err := ImportKiroCache(home); !errors.Is(err, ErrSocialLogin) {
		t.Fatalf("social: got %v", err)
	}

	writeFile(t, filepath.Join(dir, "kiro-auth-token.json"), `{"refreshToken":"r","authMethod":"IdC","clientIdHash":"../../etc/passwd"}`)
	if _, err := ImportKiroCache(home); err == nil {
		t.Fatal("path-traversing client hash accepted")
	}
}

func TestDiscoverProfiles(t *testing.T) {
	f := &fakeOIDC{t: t, tokenReplies: []func(http.ResponseWriter, map[string]string){
		func(w http.ResponseWriter, _ map[string]string) {
			fmt.Fprint(w, `{"accessToken":"at","expiresIn":3600}`)
		},
	}}
	oidc := f.server()
	defer oidc.Close()
	r := NewRefresher(oidc.Client()).WithBaseURL(oidc.URL)

	profiles := func(body string, check func(*http.Request)) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			check(req)
			fmt.Fprint(w, body)
		}))
	}
	check := func(req *http.Request) {
		if req.Header.Get("X-Amz-Target") != "AmazonCodeWhispererService.ListAvailableProfiles" ||
			req.Header.Get("Authorization") != "Bearer at" || req.Header.Get("Content-Type") != "application/x-amz-json-1.0" {
			t.Errorf("headers = %v", req.Header)
		}
	}
	us := profiles(`{"profiles":[{"arn":"arn:aws:codewhisperer:us-east-1:1:profile/A","profileName":"Team‮A"},{"arn":"not-an-arn"}]}`, check)
	defer us.Close()
	eu := profiles(`{"profiles":[{"arn":"arn:aws:codewhisperer:eu-central-1:1:profile/B","profileName":"B"},{"arn":"arn:aws:codewhisperer:us-east-1:1:profile/A"}]}`, check)
	defer eu.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer down.Close()

	got, err := DiscoverProfiles(context.Background(), nil, r, testLogin(), []string{us.URL, down.URL, eu.URL})
	if err != nil || len(got) != 2 {
		t.Fatalf("profiles = %+v, %v", got, err)
	}
	if got[0].Name != "TeamA" || got[1].Region() != "eu-central-1" {
		t.Fatalf("profiles = %+v", got)
	}
	l := testLogin().WithProfile(got[1])
	if l.ProfileARN != got[1].ARN || l.APIRegion != "eu-central-1" {
		t.Fatalf("WithProfile = %#v", l)
	}
	if _, err := DiscoverProfiles(context.Background(), nil, r, testLogin(), []string{down.URL}); err == nil {
		t.Fatal("all regions failing should be an error")
	}
	if _, err := ListProfiles(context.Background(), nil, "https://evil.example", "at"); err == nil {
		t.Fatal("unpinned host accepted")
	}
	if ARNRegion("arn:aws:codewhisperer:../x:1:profile/A") != "" || ARNRegion("arn:aws:s3:us-east-1:1:x/y") != "" {
		t.Fatal("ARNRegion accepted a bad ARN")
	}
}

func TestResolveProfile(t *testing.T) {
	// The lookup's own refresh rotates the token: the returned login must
	// carry the new one, or storing it would save a dead refresh token.
	f := &fakeOIDC{t: t, tokenReplies: []func(http.ResponseWriter, map[string]string){
		func(w http.ResponseWriter, _ map[string]string) {
			fmt.Fprint(w, `{"accessToken":"at","refreshToken":"rotated","expiresIn":3600}`)
		},
	}}
	oidc := f.server()
	defer oidc.Close()
	one := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"profiles":[{"arn":"arn:aws:codewhisperer:eu-central-1:1:profile/P","profileName":"P"}]}`)
	}))
	defer one.Close()

	r := NewRefresher(oidc.Client()).WithBaseURL(oidc.URL)
	l, ps, err := ResolveProfile(context.Background(), nil, r, testLogin(), "", []string{one.URL})
	if err != nil || ps != nil {
		t.Fatalf("ResolveProfile = %v, %v", ps, err)
	}
	if l.RefreshToken != "rotated" || l.ProfileARN != "arn:aws:codewhisperer:eu-central-1:1:profile/P" || l.APIRegion != "eu-central-1" {
		t.Fatalf("login = %#v (refresh %q)", l, l.RefreshToken)
	}

	l, _, err = ResolveProfile(context.Background(), nil, r, testLogin(), "arn:aws:codewhisperer:us-east-1:9:profile/X", nil)
	if err != nil || l.ProfileARN != "arn:aws:codewhisperer:us-east-1:9:profile/X" {
		t.Fatalf("explicit = %#v, %v", l, err)
	}
	if _, _, err := ResolveProfile(context.Background(), nil, r, testLogin(), "not-an-arn", nil); err == nil {
		t.Fatal("bad explicit ARN accepted")
	}
}

func TestProfileRegionsIncludeTheLoginsOwn(t *testing.T) {
	l := Login{Region: "ap-southeast-2"}
	got := profileRegionsFor(l)
	want := []string{"ap-southeast-2", "us-east-1", "eu-central-1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("regions = %v, want %v", got, want)
	}
	l = Login{Region: "us-east-1", APIRegion: "ap-northeast-1"}
	if got := profileRegionsFor(l); strings.Join(got, ",") != "ap-northeast-1,us-east-1,eu-central-1" {
		t.Fatalf("regions = %v", got)
	}
	if got := profileRegionsFor(Login{Region: "bad/../region"}); strings.Join(got, ",") != "us-east-1,eu-central-1" {
		t.Fatalf("invalid region kept: %v", got)
	}
	// A profile found in ap-southeast-2 moves the API there.
	p := Profile{ARN: "arn:aws:codewhisperer:ap-southeast-2:123:profile/X"}
	if lp := (Login{Region: "ap-southeast-2"}).WithProfile(p); lp.APIRegion != "ap-southeast-2" || APIBaseURL(lp.APIRegionOrDefault()) != "https://q.ap-southeast-2.amazonaws.com" {
		t.Fatalf("login = %#v", lp)
	}
}

func TestAPIRegionFallsBackToTheSSORegion(t *testing.T) {
	for _, c := range []struct {
		l    Login
		want string
	}{
		{Login{}, "us-east-1"},
		{Login{Region: "ap-southeast-2"}, "ap-southeast-2"},
		{Login{Region: "ap-southeast-2", APIRegion: "us-east-1"}, "us-east-1"},
	} {
		if got := c.l.APIRegionOrDefault(); got != c.want {
			t.Errorf("%#v: got %q want %q", c.l, got, c.want)
		}
	}
}
