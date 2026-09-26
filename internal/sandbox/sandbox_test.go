package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
)

func TestFromSettingsDefaults(t *testing.T) {
	t.Setenv("BELAI_HOME", "/home/x/.vulnetix/belai")
	p := FromSettings(nil, []string{"/work"}, posture.Defaults())
	if p.Mode != ModeAuto || p.DenyNetwork {
		t.Fatalf("policy = %+v", p)
	}
	if p.Writable[0] != "/work" || len(p.Writable) < 5 {
		t.Fatalf("writable = %v", p.Writable)
	}
	if len(p.Hidden) != 1 || p.Hidden[0] != "/home/x/.vulnetix/belai" {
		t.Fatalf("hidden = %v", p.Hidden)
	}
}

func TestStrictPolicy(t *testing.T) {
	no := false
	p := FromSettings(&config.SandboxSettings{Network: "deny", Caches: &no, ExtraWritable: []string{"/data", "relative"}}, []string{"/work"}, posture.Defaults())
	if !p.DenyNetwork || strings.Join(p.Writable, ",") != "/work,/data" {
		t.Fatalf("policy = %+v", p)
	}
}

func TestGuardrailsOffTurnsSandboxOff(t *testing.T) {
	p := FromSettings(&config.SandboxSettings{Mode: "required"}, []string{"/w"}, posture.AllIgnore())
	if p.Mode != ModeOff {
		t.Fatalf("mode = %q", p.Mode)
	}
	cmd := exec.Command("true")
	if ok, err := Wrap(cmd, p); ok || err != nil {
		t.Fatal("off policy wrapped the command")
	}
}

func TestBwrapArgs(t *testing.T) {
	hidden := t.TempDir()
	args := strings.Join(BwrapArgs(Policy{Mode: ModeAuto, DenyNetwork: true, Writable: []string{"/w", "/w/"}, Hidden: []string{hidden}}, "/w", []string{"/bin/sh", "-c", "ls"}), " ")
	for _, want := range []string{"--ro-bind / /", "--tmpfs /tmp", "--bind-try /w /w", "--tmpfs " + hidden, "--unshare-net", "--chdir /w", "-- /bin/sh -c ls"} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q: %s", want, args)
		}
	}
	if strings.Count(args, "--bind-try /w /w") != 1 {
		t.Errorf("duplicate bind: %s", args)
	}
	if strings.Index(args, "--tmpfs "+hidden) < strings.Index(args, "--bind-try") {
		t.Error("hidden dir must be masked after the binds")
	}
}

func TestSeatbeltProfile(t *testing.T) {
	prof := SeatbeltProfile(Policy{DenyNetwork: true, Writable: []string{`/w"x`}, Hidden: []string{"/h"}})
	for _, want := range []string{"(deny file-write*)", `(subpath "/w\"x")`, `(deny file-read* file-write* (subpath "/h"))`, "(deny network-outbound"} {
		if !strings.Contains(prof, want) {
			t.Errorf("profile lacks %q:\n%s", want, prof)
		}
	}
}

// Against the real backend when there is one: writes outside the roots
// fail, writes inside succeed, the hidden dir is empty.
func TestBwrapConfines(t *testing.T) {
	if name, _ := Backend(); name != "bwrap" {
		t.Skip("bwrap not usable here")
	}
	root := t.TempDir()
	outside := t.TempDir()
	hidden := t.TempDir()
	os.WriteFile(filepath.Join(hidden, "secret"), []byte("s"), 0o600)
	p := Policy{Mode: ModeAuto, DenyNetwork: true, Writable: []string{root}, Hidden: []string{hidden}}
	run := func(script string) error {
		cmd := exec.Command("sh", "-c", script)
		cmd.Dir = root
		if ok, err := Wrap(cmd, p); !ok || err != nil {
			t.Fatalf("wrap: %v %v", ok, err)
		}
		return cmd.Run()
	}
	if err := run("echo ok > inside"); err != nil {
		t.Fatalf("write inside the root failed: %v", err)
	}
	if err := run("echo no > " + filepath.Join(outside, "x")); err == nil {
		t.Fatal("write outside the roots succeeded")
	}
	if err := run("test ! -e " + filepath.Join(hidden, "secret")); err != nil {
		t.Fatal("hidden dir visible inside the sandbox")
	}
}

func TestRequiredWithoutBackendRefuses(t *testing.T) {
	if name, _ := Backend(); name != "" {
		t.Skip("a backend exists here")
	}
	if _, err := Wrap(exec.Command("true"), Policy{Mode: ModeRequired}); err != ErrUnavailable {
		t.Fatalf("err = %v", err)
	}
}
