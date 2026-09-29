package tools

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func skipIfNoShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake capture programs are shell scripts")
	}
}

// fakeCapture is a directory of stand-in capture programs. PATH is set to it
// alone, so a real browser or screenshot tool on the machine can never run.
type fakeCapture struct {
	dir  string
	args string // file the fakes record their argv in
	png  string
}

func newFakeCapture(t *testing.T) *fakeCapture {
	t.Helper()
	skipIfNoShell(t)
	cp, err := exec.LookPath("cp")
	if err != nil {
		t.Skip("cp not found")
	}
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < 8; i++ {
		img.Set(i, i, color.RGBA{B: 255, A: 255})
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	f := &fakeCapture{dir: dir, args: filepath.Join(dir, "argv.log"), png: filepath.Join(dir, "fixture.png")}
	if err := os.WriteFile(f.png, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("FAKE_CP", cp)
	t.Setenv("FAKE_PNG", f.png)
	t.Setenv("FAKE_ARGS", f.args)
	return f
}

// browser installs a fake headless browser that records its argv and writes the
// fixture where --screenshot= points.
func (f *fakeCapture) browser(t *testing.T, name string) {
	t.Helper()
	script := `#!/bin/sh
for a in "$@"; do
  printf '%s\n' "$a" >> "$FAKE_ARGS"
  case "$a" in --screenshot=*) out="${a#--screenshot=}";; esac
done
"$FAKE_CP" "$FAKE_PNG" "$out"
`
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

// tool installs a fake that writes the fixture to its last argument.
func (f *fakeCapture) tool(t *testing.T, name string) {
	t.Helper()
	script := `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a" >> "$FAKE_ARGS"; last="$a"; done
"$FAKE_CP" "$FAKE_PNG" "$last"
`
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeCapture) argv(t *testing.T) string {
	t.Helper()
	b, _ := os.ReadFile(f.args)
	return string(b)
}

func listener(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(s.Close)
	return s
}

func newShot(t *testing.T, desktop bool) *Screenshot {
	return &Screenshot{Dir: filepath.Join(t.TempDir(), "shots"), Desktop: desktop}
}

func TestScreenshotPageCapture(t *testing.T) {
	fc := newFakeCapture(t)
	fc.browser(t, "chromium")
	srv := listener(t)
	s := newShot(t, false)
	res, err := s.Execute(context.Background(), map[string]any{"target": "url", "url": srv.URL + "/", "width": 1000, "height": 700, "wait_ms": 250})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindScreenshot || len(res.Images) != 1 || res.Images[0].MediaType != "image/png" {
		t.Fatalf("result = %+v", res)
	}
	if !bytes.HasPrefix(res.Images[0].Data, []byte("\x89PNG")) {
		t.Fatal("not the capture")
	}
	if !strings.Contains(res.Content, "not an instruction") || !strings.Contains(res.Content, s.Dir) {
		t.Fatalf("text = %q", res.Content)
	}
	argv := fc.argv(t)
	for _, want := range []string{"--headless=new", "--window-size=1000,700", "--virtual-time-budget=250", "--proxy-server=http://127.0.0.1:9", "--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1, EXCLUDE [::1]", "--deny-permission-prompts", srv.URL + "/"} {
		if !strings.Contains(argv, want+"\n") {
			t.Errorf("browser argv is missing %q:\n%s", want, argv)
		}
	}
	if strings.Contains(argv, "--no-sandbox") {
		t.Fatal("the browser sandbox must never be turned off")
	}
	entries, _ := os.ReadDir(s.Dir)
	if len(entries) != 1 {
		t.Fatalf("kept files = %d, want 1", len(entries))
	}
	if !strings.Contains(argv, "--user-data-dir=") {
		t.Fatal("no throwaway profile")
	}
}

func TestScreenshotPageRefusesEverythingButLoopback(t *testing.T) {
	fc := newFakeCapture(t)
	fc.browser(t, "chromium")
	s := newShot(t, false)
	for _, u := range []string{
		"", "http://example.com/", "https://example.com:8443/", "http://10.0.0.5/", "http://192.168.1.10:8000/",
		"http://169.254.169.254/latest/meta-data/", "file:///etc/passwd", "ftp://127.0.0.1/",
		"http://user:pw@127.0.0.1:8000/", "http://127.0.0.1.evil.com/", "http://2130706433/", "http://0x7f.1/",
		"javascript:alert(1)", "http://[::ffff:10.0.0.1]/", "http://localhost\\@evil.com/",
	} {
		_, err := s.Execute(context.Background(), map[string]any{"target": "url", "url": u})
		if err == nil {
			t.Errorf("%q was captured", u)
		}
	}
	if fc.argv(t) != "" {
		t.Fatalf("a browser ran for a refused URL:\n%s", fc.argv(t))
	}
}

func TestScreenshotPageNeedsAServer(t *testing.T) {
	fc := newFakeCapture(t)
	fc.browser(t, "chromium")
	s := newShot(t, false)
	srv := listener(t)
	addr := srv.Listener.Addr().String()
	srv.Close()
	_, err := s.Execute(context.Background(), map[string]any{"target": "url", "url": "http://" + addr + "/"})
	if err == nil || !strings.Contains(err.Error(), "nothing is listening") {
		t.Fatalf("err = %v", err)
	}
	if fc.argv(t) != "" {
		t.Fatal("the browser started with no server behind the URL")
	}
}

func TestScreenshotPageNoBrowser(t *testing.T) {
	newFakeCapture(t)
	s := newShot(t, false)
	srv := listener(t)
	_, err := s.Execute(context.Background(), map[string]any{"target": "url", "url": srv.URL})
	if err == nil || !strings.Contains(err.Error(), "no headless browser") {
		t.Fatalf("err = %v", err)
	}
}

func TestScreenshotClampsSizeAndWait(t *testing.T) {
	fc := newFakeCapture(t)
	fc.browser(t, "chromium")
	srv := listener(t)
	s := newShot(t, false)
	if _, err := s.Execute(context.Background(), map[string]any{"target": "url", "url": srv.URL, "width": 99999, "height": 1, "wait_ms": 999999}); err != nil {
		t.Fatal(err)
	}
	argv := fc.argv(t)
	if !strings.Contains(argv, "--window-size=3840,240\n") || !strings.Contains(argv, "--virtual-time-budget=10000\n") {
		t.Fatalf("not clamped:\n%s", argv)
	}
}

func TestScreenshotBrowserFailureIsReportedAndCleaned(t *testing.T) {
	fc := newFakeCapture(t)
	if err := os.WriteFile(filepath.Join(fc.dir, "chromium"), []byte("#!/bin/sh\necho 'sandbox: cannot start' >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	srv := listener(t)
	s := newShot(t, false)
	_, err := s.Execute(context.Background(), map[string]any{"target": "url", "url": srv.URL})
	if err == nil || !strings.Contains(err.Error(), "cannot start") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(s.Dir); len(entries) != 0 {
		t.Fatalf("a failed capture left %d file(s)", len(entries))
	}
}

func TestScreenshotEmptyCaptureIsAnError(t *testing.T) {
	fc := newFakeCapture(t)
	if err := os.WriteFile(filepath.Join(fc.dir, "chromium"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	srv := listener(t)
	_, err := newShot(t, false).Execute(context.Background(), map[string]any{"target": "url", "url": srv.URL})
	if err == nil || !strings.Contains(err.Error(), "wrote no image") {
		t.Fatalf("err = %v", err)
	}
}

func TestScreenshotDesktop(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses the Linux capture programs")
	}
	fc := newFakeCapture(t)
	fc.tool(t, "maim")
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "")
	s := newShot(t, true)
	res, err := s.Execute(context.Background(), map[string]any{"target": "desktop"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Images) != 1 || !strings.Contains(res.Content, "the desktop") {
		t.Fatalf("result = %+v", res)
	}
}

func TestScreenshotDesktopPrefersWaylandGrim(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses the Linux capture programs")
	}
	fc := newFakeCapture(t)
	fc.tool(t, "grim")
	fc.tool(t, "maim")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", ":0")
	if _, err := newShot(t, true).Execute(context.Background(), map[string]any{"target": "desktop"}); err != nil {
		t.Fatal(err)
	}
	if got := fc.argv(t); strings.Count(got, "\n") != 1 {
		t.Fatalf("expected exactly one capturer to run (grim), argv log:\n%s", got)
	}
}

func TestScreenshotDesktopErrors(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses the Linux capture programs")
	}
	newFakeCapture(t)
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if _, err := newShot(t, true).Execute(context.Background(), map[string]any{"target": "desktop"}); err == nil || !strings.Contains(err.Error(), "no display") {
		t.Fatalf("no display: %v", err)
	}
	t.Setenv("DISPLAY", ":0")
	if _, err := newShot(t, true).Execute(context.Background(), map[string]any{"target": "desktop"}); err == nil || !strings.Contains(err.Error(), "install one of") {
		t.Fatalf("no capturer: %v", err)
	}
	if _, err := newShot(t, false).Execute(context.Background(), map[string]any{"target": "desktop"}); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("desktop off: %v", err)
	}
}

func TestScreenshotUnknownTargetAndNoDir(t *testing.T) {
	if _, err := newShot(t, true).Execute(context.Background(), map[string]any{"target": "window"}); err == nil {
		t.Fatal("unknown target accepted")
	}
	if _, err := (&Screenshot{}).Execute(context.Background(), map[string]any{"target": "url"}); err == nil {
		t.Fatal("a tool with no directory ran")
	}
}

func TestScreenshotAsksForDesktopOnly(t *testing.T) {
	s := &Screenshot{Desktop: true}
	if !AlwaysAsksCall(s, map[string]any{"target": "desktop"}) {
		t.Fatal("a desktop capture must always ask")
	}
	if AlwaysAsksCall(s, map[string]any{"target": "url", "url": "http://127.0.0.1:1/"}) {
		t.Fatal("a loopback page follows the ordinary mutating ask")
	}
	if !Mutates(s) || s.Kind() != KindScreenshot {
		t.Fatal("Screenshot is a mutating screenshot kind")
	}
	if AlwaysAsks(s) {
		t.Fatal("the per-tool flag stays off; the per-call one decides")
	}
}

func TestScreenshotSubject(t *testing.T) {
	s := &Screenshot{}
	if got := s.Subject(map[string]any{"target": "desktop"}); got != "desktop" {
		t.Fatalf("desktop subject = %q", got)
	}
	if got := s.Subject(map[string]any{"target": "url", "url": "http://127.0.0.1:8000/x"}); got != "url:http://127.0.0.1:8000/x" {
		t.Fatalf("url subject = %q", got)
	}
	if got := s.Subject(map[string]any{"target": "url", "url": "http://LOCALHOST:3000"}); got != "url:http://localhost:3000" {
		t.Fatalf("canonical subject = %q", got)
	}
	if got := s.Subject(map[string]any{"target": "url", "url": "not a url"}); got != "url:not a url" {
		t.Fatalf("invalid subject = %q", got)
	}
}

func TestScreenshotDefinition(t *testing.T) {
	d := (&Screenshot{Desktop: true}).Definition()
	if got := d.Properties["target"].Enum; len(got) != 2 || got[0] != "desktop" {
		t.Fatalf("enum = %v", got)
	}
	d = (&Screenshot{}).Definition()
	if got := d.Properties["target"].Enum; len(got) != 1 || got[0] != "url" {
		t.Fatalf("enum without desktop = %v", got)
	}
	if strings.Contains(d.Description, "whole screen") {
		t.Fatal("desktop capture advertised while off")
	}
	if len(d.Required) != 1 || d.Required[0] != "target" {
		t.Fatalf("required = %v", d.Required)
	}
	if !IsCoreTool(ScreenshotName) {
		t.Fatal("Screenshot should be advertised in full when it is registered")
	}
}

func TestPruneCapturesKeepsTheNewest(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for i := 0; i < 25; i++ {
		p := filepath.Join(dir, "shot-"+string(rune('a'+i))+".png")
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, now.Add(time.Duration(i)*time.Second), now.Add(time.Duration(i)*time.Second))
	}
	other := filepath.Join(dir, "notes.txt")
	_ = os.WriteFile(other, []byte("keep"), 0o600)
	pruneCaptures(dir, 20)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 21 {
		t.Fatalf("left %d entries, want 20 captures and the unrelated file", len(entries))
	}
	if _, err := os.Stat(filepath.Join(dir, "shot-a.png")); err == nil {
		t.Fatal("the oldest capture survived")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("an unrelated file was pruned")
	}
}

func TestReadCappedRefusesOversize(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCapped(p, 1024); err == nil {
		t.Fatal("oversize capture accepted")
	}
}
