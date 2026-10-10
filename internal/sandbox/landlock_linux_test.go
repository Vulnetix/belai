//go:build linux

package sandbox

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/vaultenv"
	"golang.org/x/sys/unix"
)

// The local access bits are the kernel's.
func TestLandlockBitsAreTheKernels(t *testing.T) {
	for name, pair := range map[string][2]uint64{
		"execute":     {llExecute, unix.LANDLOCK_ACCESS_FS_EXECUTE},
		"write_file":  {llWriteFile, unix.LANDLOCK_ACCESS_FS_WRITE_FILE},
		"read_file":   {llReadFile, unix.LANDLOCK_ACCESS_FS_READ_FILE},
		"read_dir":    {llReadDir, unix.LANDLOCK_ACCESS_FS_READ_DIR},
		"remove_dir":  {llRemoveDir, unix.LANDLOCK_ACCESS_FS_REMOVE_DIR},
		"remove_file": {llRemoveFile, unix.LANDLOCK_ACCESS_FS_REMOVE_FILE},
		"make_char":   {llMakeChar, unix.LANDLOCK_ACCESS_FS_MAKE_CHAR},
		"make_dir":    {llMakeDir, unix.LANDLOCK_ACCESS_FS_MAKE_DIR},
		"make_reg":    {llMakeReg, unix.LANDLOCK_ACCESS_FS_MAKE_REG},
		"make_sock":   {llMakeSock, unix.LANDLOCK_ACCESS_FS_MAKE_SOCK},
		"make_fifo":   {llMakeFifo, unix.LANDLOCK_ACCESS_FS_MAKE_FIFO},
		"make_block":  {llMakeBlock, unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK},
		"make_sym":    {llMakeSym, unix.LANDLOCK_ACCESS_FS_MAKE_SYM},
		"refer":       {llRefer, unix.LANDLOCK_ACCESS_FS_REFER},
		"truncate":    {llTruncate, unix.LANDLOCK_ACCESS_FS_TRUNCATE},
		"ioctl_dev":   {llIoctlDev, unix.LANDLOCK_ACCESS_FS_IOCTL_DEV},
		"bind_tcp":    {llNetBindTCP, unix.LANDLOCK_ACCESS_NET_BIND_TCP},
		"connect_tcp": {llNetConnectTCP, unix.LANDLOCK_ACCESS_NET_CONNECT_TCP},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s: %#x, the kernel's is %#x", name, pair[0], pair[1])
		}
	}
}

// withLandlock makes Landlock the backend for the test by putting a bubblewrap
// that cannot work first on PATH, and skips where the kernel has none.
func withLandlock(t *testing.T) int {
	t.Helper()
	if Nested() {
		t.Skip("already inside a Belai sandbox")
	}
	abi, err := landlockABI()
	if err != nil {
		t.Skipf("no Landlock here: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bwrap"), []byte("#!/bin/sh\necho 'bwrap: No permissions to create new namespace' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ResetProbeForTests()
	t.Cleanup(ResetProbeForTests)
	if name, _ := Backend(); name != "landlock" {
		t.Fatalf("backend %q (%s), want landlock", name, BackendProblem())
	}
	return abi
}

func TestLandlockProbePrintsTheABI(t *testing.T) {
	if _, err := landlockABI(); err != nil {
		t.Skipf("no Landlock here: %v", err)
	}
	var out, errOut strings.Builder
	if code := LandlockExecMain([]string{"-probe"}, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "abi=") {
		t.Fatalf("code %d out %q err %q", code, out.String(), errOut.String())
	}
	exe, _ := os.Executable()
	if abi, err := landlockProbe(exe); err != nil || abi < 1 {
		t.Fatalf("probe through the binary: %d %v", abi, err)
	}
}

// homeTemp makes a directory under the home directory, which the policy keeps
// read-only. /tmp will not do: under Landlock the host's /tmp is writable.
func homeTemp(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	dir, err := os.MkdirTemp(home, ".belai-landlock-test-")
	if err != nil {
		t.Skipf("the home directory is not writable: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestLandlockConfines(t *testing.T) {
	abi := withLandlock(t)
	root := t.TempDir()
	outside := homeTemp(t)
	hidden := homeTemp(t)
	os.WriteFile(filepath.Join(hidden, "secret"), []byte("s"), 0o600)
	p := Policy{Mode: ModeRequired, DenyNetwork: true, Writable: []string{root}, Hidden: []string{hidden}}
	run := func(script string) (string, error) {
		cmd := exec.Command("sh", "-c", script)
		cmd.Dir = root
		if ok, err := Wrap(cmd, p); !ok || err != nil {
			t.Fatalf("wrap: %v %v", ok, err)
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("echo ok > inside && cat inside"); err != nil || !strings.Contains(out, "ok") {
		t.Fatalf("write inside the root: %v %s", err, out)
	}
	if out, err := run("echo no > " + filepath.Join(outside, "x")); err == nil {
		t.Fatalf("write outside the roots succeeded: %s", out)
	}
	if out, err := run("cat " + filepath.Join(hidden, "secret")); err == nil {
		t.Fatalf("the hidden file could be read: %s", out)
	}
	if out, err := run("ls " + hidden); err != nil || !strings.Contains(out, "secret") {
		t.Fatalf("a hidden directory is listable under Landlock, by design: %v %s", err, out)
	}
	if out, err := run("echo t > /tmp/belai-landlock-test.$$ && rm /tmp/belai-landlock-test.$$"); err != nil {
		t.Fatalf("/tmp is writable: %v %s", err, out)
	}
	if out, err := run("echo x > /dev/null && /bin/true"); err != nil {
		t.Fatalf("/dev/null and exec: %v %s", err, out)
	}
	if out, err := run("echo " + EnvMarker + "=$" + EnvMarker + " && test -z \"$" + landlockPolicyEnv + "\""); err != nil || !strings.Contains(out, EnvMarker+"=1") {
		t.Fatalf("the marker is set and the policy taken out of the environment: %v %s", err, out)
	}
	if abi >= landlockNetABI {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		port := ln.Addr().(*net.TCPAddr).Port
		// A connect refused by the sandbox and one refused by nobody listening
		// read differently; the listener is there, so only the sandbox can refuse.
		if out, err := run("exec 3<>/dev/tcp/127.0.0.1/" + itoa(port)); err == nil {
			t.Fatalf("a TCP connect succeeded with the network denied: %s", out)
		}
	}
}

func TestLandlockRequiredWithNetworkDenyNeedsABI4(t *testing.T) {
	abi := withLandlock(t)
	p := Policy{Mode: ModeRequired, DenyNetwork: true, Writable: []string{t.TempDir()}}
	_, err := Wrap(exec.Command("true"), p)
	if abi >= landlockNetABI {
		if err != nil {
			t.Fatalf("ABI %d: %v", abi, err)
		}
		return
	}
	if !errors.Is(err, ErrNetworkDeny) {
		t.Fatalf("ABI %d: err = %v", abi, err)
	}
	p.Mode = ModeAuto
	if ok, err := Wrap(exec.Command("true"), p); !ok || err != nil {
		t.Fatalf("auto mode runs with the network open: %v %v", ok, err)
	}
}

func TestLandlockRefusesACommandHoldingVaultVariables(t *testing.T) {
	withLandlock(t)
	vaultenv.Default.Replace([]vaultenv.Var{{Name: "A_TOKEN", Value: "a-token-value-1"}}, time.Now().Add(time.Hour))
	defer vaultenv.Default.Replace(nil, time.Time{})
	if _, err := Wrap(exec.Command("true"), Policy{Mode: ModeAuto, Writable: []string{t.TempDir()}}); !errors.Is(err, ErrLandlockVault) {
		t.Fatalf("err = %v, want ErrLandlockVault", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
