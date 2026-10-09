package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/vaultenv"
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

func TestBwrapUnsharesPIDsOnlyWhileTheVaultHoldsVariables(t *testing.T) {
	defer vaultenv.Default.Replace(nil, time.Time{})
	defer func(f func() pidMode) { pidModeFn = f }(pidModeFn)
	pidModeFn = func() pidMode { return pidNamespace }
	has := func() bool {
		for _, a := range BwrapArgs(Policy{Mode: ModeAuto}, "/work", []string{"true"}) {
			if a == "--unshare-pid" {
				return true
			}
		}
		return false
	}
	vaultenv.Default.Replace(nil, time.Time{})
	if has() {
		t.Fatal("no vault variables, no extra namespace")
	}
	vaultenv.Default.Replace([]vaultenv.Var{{Name: "A_TOKEN", Value: "a-token-value-1"}}, time.Now().Add(time.Hour))
	if !has() {
		t.Fatal("with vault variables the command gets its own pid namespace")
	}
}

// A container that masks paths under /proc refuses a fresh /proc in a new pid
// namespace; the flag is then left off, because the probe showed the user namespace
// already hides other processes' environments.
func TestBwrapLeavesOutThePIDNamespaceWhereTheUserNamespaceAlreadyHidesEnvirons(t *testing.T) {
	defer vaultenv.Default.Replace(nil, time.Time{})
	defer func(f func() pidMode) { pidModeFn = f }(pidModeFn)
	vaultenv.Default.Replace([]vaultenv.Var{{Name: "A_TOKEN", Value: "a-token-value-1"}}, time.Now().Add(time.Hour))
	has := func() bool {
		for _, a := range BwrapArgs(Policy{Mode: ModeAuto}, "/work", []string{"true"}) {
			if a == "--unshare-pid" {
				return true
			}
		}
		return false
	}
	pidModeFn = func() pidMode { return pidNamespace }
	if !has() {
		t.Fatal("a mountable pid namespace is used")
	}
	pidModeFn = func() pidMode { return pidUserNS }
	if has() {
		t.Fatal("the flag stays off where a fresh /proc cannot be mounted and environs are already hidden")
	}
}

func TestWrapRefusesAVaultCommandWhenNothingKeepsEnvironsPrivate(t *testing.T) {
	if name, _ := Backend(); name != "bwrap" {
		t.Skip("needs bubblewrap")
	}
	defer vaultenv.Default.Replace(nil, time.Time{})
	defer func(f func() pidMode) { pidModeFn = f }(pidModeFn)
	pidModeFn = func() pidMode { return pidNone }
	vaultenv.Default.Replace(nil, time.Time{})
	if _, err := Wrap(exec.Command("true"), Policy{Mode: ModeAuto}); err != nil {
		t.Fatalf("without vault variables the command runs: %v", err)
	}
	vaultenv.Default.Replace([]vaultenv.Var{{Name: "A_TOKEN", Value: "a-token-value-1"}}, time.Now().Add(time.Hour))
	if _, err := Wrap(exec.Command("true"), Policy{Mode: ModeAuto}); err != ErrPIDIsolation {
		t.Fatalf("err = %v, want ErrPIDIsolation", err)
	}
	for _, m := range []pidMode{pidNamespace, pidUserNS} {
		pidModeFn = func() pidMode { return m }
		if _, err := Wrap(exec.Command("true"), Policy{Mode: ModeAuto}); err != nil {
			t.Fatalf("mode %d refused: %v", m, err)
		}
	}
}

// On a host with a working bubblewrap the probe must find some isolation, and a
// command run the way the probe decides must not read another process's environ.
func TestPIDIsolationProbeMatchesWhatACommandCanRead(t *testing.T) {
	if name, _ := Backend(); name != "bwrap" {
		t.Skip("needs bubblewrap")
	}
	mode := pidIsolation()
	if mode == pidNone {
		t.Skip("this host offers neither a pid namespace nor hidden environs; Wrap refuses vault commands here")
	}
	holder := exec.Command("sleep", "30")
	holder.Env = append(os.Environ(), "BELAI_PIDTEST_SECRET=hunter2")
	if err := holder.Start(); err != nil {
		t.Skip("no sleep")
	}
	defer func() { _ = holder.Process.Kill(); _ = holder.Wait() }()
	args := []string{"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp"}
	if mode == pidNamespace {
		args = append(args, "--unshare-pid")
	}
	args = append(args, "--", "sh", "-c", fmt.Sprintf("( : < /proc/%d/environ ) 2>/dev/null && exit 0; exit 4", holder.Process.Pid))
	_, path := Backend()
	err := exec.Command(path, args...).Run()
	if err == nil {
		t.Fatalf("mode %d: the sandboxed command read another process's environ", mode)
	}
}

// A TMPDIR outside the private /tmp (the Pix sandbox image sets /workspace/tmp)
// is writable, or go build and its kin cannot make a work dir; one at or under
// /tmp is not bound, which would replace the private /tmp with the host's.
func TestCustomTempDirIsWritable(t *testing.T) {
	for _, tc := range []struct {
		tmp  string
		want bool
	}{
		{"/workspace/tmp", true},
		{"/workspace/tmp/", true},
		{"/tmp", false},
		{"/tmp/x", false},
		{"/private/var/folders/ab/cd/T", false},
		{"/", false},
		{"relative/tmp", false},
		{"", false},
	} {
		t.Setenv("TMPDIR", tc.tmp)
		p := FromSettings(nil, []string{"/work"}, posture.Defaults())
		got := false
		for _, w := range p.Writable {
			if w == filepath.Clean(tc.tmp) && tc.tmp != "" {
				got = true
			}
		}
		if got != tc.want {
			t.Errorf("TMPDIR=%q writable=%v, want %v (%v)", tc.tmp, got, tc.want, p.Writable)
		}
	}
	t.Setenv("TMPDIR", "/workspace/tmp")
	no := false
	if p := FromSettings(&config.SandboxSettings{Caches: &no}, []string{"/work"}, posture.Defaults()); strings.Join(p.Writable, ",") != "/work" {
		t.Errorf("the strict policy must keep only the roots: %v", p.Writable)
	}
}

// bubblewrap binds only what exists, so the Go caches must exist before a
// command runs, or they are read-only on a fresh machine.
func TestTheGoCacheDirectoriesExistBeforeTheyAreBound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	FromSettings(nil, []string{"/work"}, posture.Defaults())
	for _, d := range []string{"go", ".cache"} {
		if fi, err := os.Stat(filepath.Join(home, d)); err != nil || !fi.IsDir() {
			t.Errorf("%s was not made: %v", d, err)
		}
	}
}
