package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runHelpCmd runs the built binary with args, in a throwaway BELAI_HOME.
func runHelpCmd(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	cmd := exec.Command(belaiBin, args...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "BELAI_HOME="+filepath.Join(t.TempDir(), "belai-home"))
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run belai: %v", err)
		}
		code = ee.ExitCode()
	}
	return out.String(), errb.String(), code
}

// TestHelpIsOrganised: -help, -h, --help and `help` print the grouped help to
// stdout and exit 0; it names the subcommands and shows examples.
func TestHelpIsOrganised(t *testing.T) {
	for _, args := range [][]string{{"-help"}, {"-h"}, {"--help"}, {"help"}} {
		out, errOut, code := runHelpCmd(t, args...)
		if code != 0 || errOut != "" {
			t.Fatalf("belai %v: exit %d, stderr %q", args, code, errOut)
		}
		for _, want := range []string{"Commands:", "  rc ", "  agent ", "  kanban ", "  plugin ", "Examples:", "belai rc --detach", "Session flags:", "Safety flags:"} {
			if !strings.Contains(out, want) {
				t.Errorf("belai %v lacks %q", args, want)
			}
		}
		for i, line := range strings.Split(out, "\n") {
			if len(line) > 80 {
				t.Errorf("belai %v line %d is %d columns: %q", args, i+1, len(line), line)
			}
		}
	}
}

// TestHelpForACommand: `belai help rc` is `belai rc -h`.
func TestHelpForACommand(t *testing.T) {
	via, _, code := runHelpCmd(t, "help", "rc")
	if code != 0 || !strings.Contains(via, "belai rc") {
		t.Fatalf("belai help rc: exit %d, out %q", code, via)
	}
	_, errOut, code := runHelpCmd(t, "help", "nope")
	if code != 2 || !strings.Contains(errOut, "not a command") {
		t.Fatalf("belai help nope: exit %d, stderr %q", code, errOut)
	}
}

// TestBadFlagPointsAtHelp: an unknown flag exits 2, names the flag and points
// at -help instead of dumping every flag.
func TestBadFlagPointsAtHelp(t *testing.T) {
	out, errOut, code := runHelpCmd(t, "-bogus")
	if code != 2 || out != "" {
		t.Fatalf("exit %d, stdout %q", code, out)
	}
	if !strings.Contains(errOut, "-bogus") || !strings.Contains(errOut, "belai -help") || len(errOut) > 200 {
		t.Fatalf("stderr = %q", errOut)
	}
}

// TestEverySubcommandAnswersEveryHelpSpelling: -h, -help, --help and help print the
// command's usage and exit 0, for each listed command.
func TestEverySubcommandAnswersEveryHelpSpelling(t *testing.T) {
	for _, cmd := range []string{"agent", "kanban", "rc", "plugin", "acp", "login"} {
		for _, h := range []string{"-h", "-help", "--help", "help"} {
			out, errOut, code := runHelpCmd(t, cmd, h)
			if code != 0 {
				t.Errorf("belai %s %s: exit %d", cmd, h, code)
			}
			if !strings.Contains(out+errOut, "belai "+cmd) && !strings.Contains(out+errOut, "Usage of "+cmd) {
				t.Errorf("belai %s %s printed no usage: %q", cmd, h, out+errOut)
			}
		}
	}
}
