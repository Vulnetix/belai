package vulnetixcli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// deviceServer answers /authorize, then replies to /token from a script.
func deviceServer(t *testing.T, replies []map[string]string) (*httptest.Server, *int) {
	t.Helper()
	var mu sync.Mutex
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case devicePath + "/authorize":
			json.NewEncoder(w).Encode(map[string]any{
				"device_code": "dev-secret", "user_code": "ABCD-EFGH",
				"verification_uri": "https://www.vulnetix.com/device", "interval": 0,
			})
		case devicePath + "/token":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["device_code"] != "dev-secret" {
				t.Errorf("device_code = %q", body["device_code"])
			}
			mu.Lock()
			i := min(polls, len(replies)-1)
			polls++
			mu.Unlock()
			if replies[i]["error"] != "" {
				w.WriteHeader(http.StatusBadRequest)
			}
			json.NewEncoder(w).Encode(replies[i])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &polls
}

func fastGrant(t *testing.T, d DeviceLogin) DeviceGrant {
	t.Helper()
	g, err := d.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if g.UserCode != "ABCD-EFGH" || g.BrowseURL() != "https://www.vulnetix.com/device" {
		t.Fatalf("grant = %+v", g)
	}
	g.Interval = 0
	return g
}

func TestDeviceLoginSuccessAfterPending(t *testing.T) {
	org := "3674ddf9-67cc-4a2d-9b16-a591f6d4412d"
	srv, polls := deviceServer(t, []map[string]string{
		{"error": "authorization_pending"},
		{"error": "slow_down"},
		{"orgId": org, "apiKey": org + ":deadbeef"},
	})
	d := DeviceLogin{BaseURL: srv.URL, SlowDown: time.Millisecond}
	gotOrg, key, err := d.pollWith(context.Background(), fastGrant(t, d), time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if gotOrg != org || key != "deadbeef" || *polls != 3 {
		t.Fatalf("org=%q key=%q polls=%d", gotOrg, key, *polls)
	}
}

func TestDeviceLoginDeniedAndExpired(t *testing.T) {
	for reply, want := range map[string]error{"access_denied": ErrDeviceDenied, "expired_token": ErrDeviceExpired} {
		srv, _ := deviceServer(t, []map[string]string{{"error": reply}})
		d := DeviceLogin{BaseURL: srv.URL}
		g := fastGrant(t, d)
		if _, _, err := d.pollWith(context.Background(), g, time.Millisecond); !errors.Is(err, want) {
			t.Errorf("%s: err = %v", reply, err)
		}
	}
}

func TestDeviceLoginRejectsMismatchedKey(t *testing.T) {
	srv, _ := deviceServer(t, []map[string]string{{"orgId": "org-a", "apiKey": "org-b:beef"}})
	d := DeviceLogin{BaseURL: srv.URL}
	if _, _, err := d.pollWith(context.Background(), fastGrant(t, d), time.Millisecond); err == nil {
		t.Fatal("accepted a key for another org")
	}
}

func TestDeviceLoginRefusesNonHTTPSPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"device_code": "d", "user_code": "u", "verification_uri": "javascript:alert(1)",
		})
	}))
	defer srv.Close()
	if _, err := (DeviceLogin{BaseURL: srv.URL}).Start(context.Background()); err == nil {
		t.Fatal("accepted a non-https verification page")
	}
}

func TestOpenBrowserRefusesNonHTTPS(t *testing.T) {
	for _, u := range []string{"http://x", "file:///etc/passwd", "-flag", "https://user@x"} {
		if OpenBrowser(u) == nil {
			t.Errorf("opened %q", u)
		}
	}
}

// TestSaveLoginKeepsKeyOutOfArgv runs SaveLogin against a fake vulnetix that
// records its argv and environment.
func TestSaveLoginKeepsKeyOutOfArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	script := "#!/bin/sh\necho \"ARGV $*\" > " + out + "\necho \"KEY $VULNETIX_API_KEY ORG $VULNETIX_ORG_ID\" >> " + out + "\n"
	bin := filepath.Join(dir, "vulnetix")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (CLI{Path: bin}).SaveLogin(context.Background(), "org-1", "sekret"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	lines := strings.Split(string(data), "\n")
	if strings.Contains(lines[0], "sekret") || !strings.Contains(lines[0], "auth login --noninteractive --store home") {
		t.Fatalf("argv = %q", lines[0])
	}
	if lines[1] != "KEY sekret ORG org-1" {
		t.Fatalf("env = %q", lines[1])
	}
}

func TestPlanInstall(t *testing.T) {
	has := func(names ...string) func(string) (string, error) {
		return func(n string) (string, error) {
			for _, x := range names {
				if x == n {
					return "/usr/bin/" + n, nil
				}
			}
			return "", errors.New("missing")
		}
	}
	p, ok := PlanInstall("linux", has("brew"))
	if !ok || p.Manager != InstallBrew || p.Commands() != "brew install vulnetix/tap/vulnetix" {
		t.Fatalf("linux brew: %+v %v", p, ok)
	}
	p, ok = PlanInstall("windows", has("scoop"))
	if !ok || p.Manager != InstallScoop || len(p.Steps) != 2 || p.Steps[1].String() != "scoop install vulnetix" {
		t.Fatalf("windows scoop: %+v %v", p, ok)
	}
	if _, ok := PlanInstall("windows", has("brew")); ok {
		t.Fatal("brew planned on windows")
	}
	if _, ok := PlanInstall("darwin", has()); ok {
		t.Fatal("planned with no package manager")
	}
}
