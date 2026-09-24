package vulnetixcli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeScript creates an executable shell stub at $dir/vulnetix and returns the
// CLI that points at it.
func writeScript(t *testing.T, body string) CLI {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "vulnetix")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return CLI{Path: path}
}

// probeStub handles the flags and subcommands the probe path exercises.
const probeStub = `#!/bin/sh
while [ $# -gt 0 ]; do
	case "$1" in
		--no-banner|--no-progress|--no-analytics|--disable-memory) shift ;;
		--version) echo "vulnetix version 3.1.0"; exit 0 ;;
		-*) shift ;;
		*) break ;;
	esac
done
sub="$1"; shift
case "$sub" in
	version) echo "vulnetix version 3.1.0" ;;
	env) echo "API"; echo "API URL: https://api.example.com/v1"; echo "VERSION"; echo "3.1.0" ;;
	auth)
		case "$1" in
			status)
				echo "AUTH STATE"
				echo "[OK] Authenticated"
				echo "Plan: [PRO]"
				echo "Org ID: a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"
				echo "CREDENTIAL SOURCES"
				echo "[OK] VULNETIX_API_TOKEN environment variable set"
				;;
			verify) echo "[OK] verified" ;;
		esac
		;;
esac
`

func TestProbeEmptyPath(t *testing.T) {
	cap := Probe(context.Background(), CLI{}, ProbeOptions{SkipNetwork: true, Now: time.Now})
	if cap.Present {
		t.Fatal("empty path must not be present")
	}
	if !containsString(cap.Degraded, "vulnetix binary not found") {
		t.Fatalf("Degraded = %v", cap.Degraded)
	}
}

func TestProbeStatFailure(t *testing.T) {
	cap := Probe(context.Background(), CLI{Path: filepath.Join(t.TempDir(), "missing")}, ProbeOptions{SkipNetwork: true, Now: time.Now})
	if cap.Present {
		t.Fatal("missing binary must not be present")
	}
	if !containsString(cap.Degraded, "binary not accessible") {
		t.Fatalf("Degraded = %v", cap.Degraded)
	}
}

func TestProbeFull(t *testing.T) {
	c := writeScript(t, probeStub)
	getenv := func(k string) string {
		if k == "VULNETIX_WEB_URL" {
			return "https://app.example.com"
		}
		return ""
	}
	cap := Probe(context.Background(), c, ProbeOptions{
		SkipNetwork: true,
		Now:         time.Now,
		Getenv:      getenv,
	})
	if !cap.Present {
		t.Fatal("expected present")
	}
	if cap.Version.String() != "v3.1.0" {
		t.Fatalf("Version = %q", cap.Version.String())
	}
	if !cap.Auth.Authenticated || cap.Auth.Plan != PlanPro {
		t.Fatalf("Auth = %+v", cap.Auth)
	}
	if cap.Env.APIURL != "https://api.example.com/v1" {
		t.Fatalf("Env = %+v", cap.Env)
	}
	if cap.Web.Dashboard == "" {
		t.Fatal("expected dashboard URL for authenticated user")
	}
	// SkipNetwork must leave the update status untouched.
	if cap.Update.Error != "" || cap.Update.Available {
		t.Fatalf("Update = %+v", cap.Update)
	}
}

func TestProbeVersionSourcesAllPaths(t *testing.T) {
	// --version path.
	c := writeScript(t, probeStub)
	var degraded []string
	if v := probeVersionSources(context.Background(), &c, "", &degraded); v.String() != "v3.1.0" {
		t.Fatalf("--version source = %q", v.String())
	}

	// A stub with no version anywhere marks degraded.
	c = writeScript(t, "#!/bin/sh\necho nothing\n")
	degraded = nil
	if v := probeVersionSources(context.Background(), &c, "", &degraded); !v.IsZero() {
		t.Fatalf("expected zero version, got %q", v.String())
	}
	if !containsString(degraded, "could not parse installed version") {
		t.Fatalf("Degraded = %v", degraded)
	}

	// Env section source.
	c = writeScript(t, "#!/bin/sh\nwhile [ $# -gt 0 ]; do case \"$1\" in -*) shift ;; *) break ;; esac; done\necho VERSION; echo 2.0.0\n")
	degraded = nil
	if v := probeVersionSources(context.Background(), &c, "", &degraded); v.String() != "v2.0.0" {
		t.Fatalf("env section source = %q", v.String())
	}
}

func TestCheckReachability(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	// 404 < 500 → reachable.
	if !checkReachability(context.Background(), srv.Client(), srv.URL) {
		t.Fatal("404 should be reachable")
	}

	// 500 → not reachable.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	if checkReachability(context.Background(), bad.Client(), bad.URL) {
		t.Fatal("500 should be unreachable")
	}

	// nil client.
	if checkReachability(context.Background(), nil, srv.URL) {
		t.Fatal("nil client should be unreachable")
	}

	// Invalid URL.
	if checkReachability(context.Background(), srv.Client(), "://bad") {
		t.Fatal("invalid URL should be unreachable")
	}
}

type fakeObserver struct {
	started bool
	done    bool
}

func (o *fakeObserver) Start(name string, argv []string, dir string, cancel context.CancelFunc) (func(string), func(int, bool, error)) {
	o.started = true
	return func(string) {}, func(int, bool, error) { o.done = true }
}

func TestExecObservedWithAndWithoutObserver(t *testing.T) {
	c := writeScript(t, probeStub)
	ctx := context.Background()

	// Without observer: plain ExecIn.
	if _, err := execObserved(ctx, c, nil, "vulnetix version", "", "--disable-memory", "version"); err != nil {
		t.Fatalf("execObserved(nil observer): %v", err)
	}

	// With observer: streaming path is exercised and done() is called.
	obs := &fakeObserver{}
	res, err := execObserved(ctx, c, obs, "vulnetix version", "", "--disable-memory", "version")
	if err != nil {
		t.Fatalf("execObserved(observer): %v", err)
	}
	if !obs.started || !obs.done {
		t.Fatalf("observer not invoked: %+v", obs)
	}
	if res.Stdout == "" {
		t.Fatal("expected streamed stdout")
	}
}

func TestCheckCredentialValidity(t *testing.T) {
	c := writeScript(t, probeStub) // verify → "[OK] verified"
	var errOut string
	if !checkCredentialValidity(context.Background(), c, &errOut) {
		t.Fatalf("expected valid, err=%q", errOut)
	}

	// A stub whose verify fails.
	c = writeScript(t, "#!/bin/sh\nexit 1\n")
	errOut = ""
	if checkCredentialValidity(context.Background(), c, &errOut) {
		t.Fatal("expected invalid credentials")
	}
	if errOut == "" {
		t.Fatal("expected an error message")
	}

	// Verify that reports no OK/verified marker.
	c = writeScript(t, "#!/bin/sh\nwhile [ $# -gt 0 ]; do case \"$1\" in -*) shift ;; *) break ;; esac; done\necho credentials rejected\n")
	errOut = ""
	if checkCredentialValidity(context.Background(), c, &errOut) {
		t.Fatal("expected invalid when no marker")
	}
	if errOut != "credentials not verified" {
		t.Fatalf("errOut = %q", errOut)
	}
}

func TestHardenedArgs(t *testing.T) {
	got := HardenedArgs("version")
	want := []string{"--no-banner", "--no-progress", "--no-analytics", "version"}
	if len(got) != len(want) {
		t.Fatalf("HardenedArgs = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("HardenedArgs = %v, want %v", got, want)
		}
	}
}

func TestIsReadOnlyProbe(t *testing.T) {
	for _, args := range [][]string{{"env"}, {"version"}, {"auth", "status"}, {"--disable-memory", "env"}} {
		if !isReadOnlyProbe(args) {
			t.Errorf("isReadOnlyProbe(%v) = false, want true", args)
		}
	}
	for _, args := range [][]string{{"scan"}, {"install"}, {}} {
		if isReadOnlyProbe(args) {
			t.Errorf("isReadOnlyProbe(%v) = true, want false", args)
		}
	}
}

func TestExitCode(t *testing.T) {
	if got := exitCode(errors.New("plain")); got != 1 {
		t.Fatalf("exitCode(plain) = %d, want 1", got)
	}
}

func TestCappedWriterDrop(t *testing.T) {
	w := &cappedWriter{max: 5}
	n, err := w.Write([]byte("abc"))
	if err != nil || n != 3 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	n, _ = w.Write([]byte("defgh"))
	if n != 5 {
		t.Fatalf("overflowing Write returned %d, want 5", n)
	}
	if got := w.String(); got != "abcde" {
		t.Fatalf("String = %q, want abcde", got)
	}
	// Subsequent writes are silently dropped.
	if _, err := w.Write([]byte("zz")); err != nil {
		t.Fatalf("dropped Write: %v", err)
	}
	if got := w.String(); got != "abcde" {
		t.Fatalf("String after drop = %q", got)
	}
}

func TestVersionStringEdges(t *testing.T) {
	if got := (Version{}).String(); got != "" {
		t.Fatalf("zero String = %q", got)
	}
	if got := (Version{Major: 1, Minor: 2, Patch: 3, Pre: "rc1"}).String(); got != "v1.2.3-rc1" {
		t.Fatalf("pre String = %q", got)
	}
	// Raw-only version is not zero, so it still renders.
	if got := (Version{Raw: "x"}).String(); got != "v0.0.0" {
		t.Fatalf("raw-only String = %q", got)
	}
}

func TestVersionComparePreReleaseOrdering(t *testing.T) {
	alpha := Version{Major: 1, Minor: 0, Patch: 0, Pre: "alpha"}
	beta := Version{Major: 1, Minor: 0, Patch: 0, Pre: "beta"}
	if alpha.Compare(beta) >= 0 {
		t.Fatal("alpha should sort before beta")
	}
	if beta.Compare(alpha) <= 0 {
		t.Fatal("beta should sort after alpha")
	}
}

func TestParseVersionEdges(t *testing.T) {
	if _, ok := ParseVersion(""); ok {
		t.Fatal("empty should fail")
	}
	if v, ok := ParseVersion("1.2.3-beta.1"); !ok || v.Pre != "beta.1" {
		t.Fatalf("dotted pre = %+v, ok=%v", v, ok)
	}
}

func TestResolveURLsDefaultAndRejections(t *testing.T) {
	// Default root.
	w, err := ResolveURLs(func(string) string { return "" }, false)
	if err != nil {
		t.Fatalf("ResolveURLs(default): %v", err)
	}
	if w.Root != "https://www.vulnetix.com/" {
		t.Fatalf("default Root = %q", w.Root)
	}
	if w.VDB != "https://www.vulnetix.com/vdb" {
		t.Fatalf("VDB = %q", w.VDB)
	}
	if w.Register == "" {
		t.Fatal("expected register URL when unauthenticated")
	}

	// Query and fragment are rejected.
	for _, bad := range []string{"https://x.com/?q=1", "https://x.com/#frag", "file:///etc/passwd", "://bad"} {
		if _, err := ResolveURLs(func(string) string { return bad }, false); err == nil {
			t.Errorf("ResolveURLs(%q) should fail", bad)
		}
	}
}

func TestProbeEnvStripsSecrets(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "secret")
	t.Setenv("SIGNET_PROVIDER", "secret")
	t.Setenv("VULNETIX_API_TOKEN", "keepme")
	t.Setenv("NORMAL_VAR", "keepnormal")
	env := probeEnv()
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "OPENAI_API_KEY") || strings.Contains(joined, "SIGNET_PROVIDER") {
		t.Fatal("secret variables leaked into probeEnv")
	}
	if !strings.Contains(joined, "VULNETIX_API_TOKEN=keepme") {
		t.Fatal("vulnetix credential should be preserved")
	}
	if !strings.Contains(joined, "NORMAL_VAR=keepnormal") {
		t.Fatal("normal variable should be preserved")
	}
	if !strings.Contains(joined, "NO_COLOR=1") {
		t.Fatal("hardening defaults missing")
	}
}

func containsString(xs []string, sub string) bool {
	for _, x := range xs {
		if strings.Contains(x, sub) {
			return true
		}
	}
	return false
}
