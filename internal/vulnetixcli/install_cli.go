package vulnetixcli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/proc"
)

// InstallTimeout bounds a package-manager install of the CLI.
const InstallTimeout = 10 * time.Minute

// InstallStep is one fixed command of an install plan.
type InstallStep struct {
	Argv []string
}

// String renders the step as the user would type it.
func (s InstallStep) String() string { return strings.Join(s.Argv, " ") }

// InstallPlan is how the CLI can be installed on this machine: a package
// manager and its fixed commands. No argument comes from the user or the
// repository, and nothing runs until the user confirms the plan.
type InstallPlan struct {
	Manager InstallMethod
	Steps   []InstallStep
}

// Commands renders the plan's steps joined for display.
func (p InstallPlan) Commands() string {
	out := make([]string, len(p.Steps))
	for i, s := range p.Steps {
		out[i] = s.String()
	}
	return strings.Join(out, " && ")
}

// ManualInstallHint is shown when no supported package manager is found.
const ManualInstallHint = "curl -fsSL https://cli.vulnetix.com/install.sh | sh   (or: go install github.com/vulnetix/cli/v3@latest)"

// PlanInstall picks Homebrew on macOS and Linux, Scoop on Windows. ok is
// false when neither is on PATH.
func PlanInstall(goos string, lookPath func(string) (string, error)) (InstallPlan, bool) {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if goos == "" {
		goos = runtime.GOOS
	}
	switch goos {
	case "windows":
		if _, err := lookPath("scoop"); err == nil {
			return InstallPlan{Manager: InstallScoop, Steps: []InstallStep{
				{Argv: []string{"scoop", "bucket", "add", "vulnetix", "https://github.com/Vulnetix/scoop-bucket"}},
				{Argv: []string{"scoop", "install", "vulnetix"}},
			}}, true
		}
	default:
		if _, err := lookPath("brew"); err == nil {
			return InstallPlan{Manager: InstallBrew, Steps: []InstallStep{
				{Argv: []string{"brew", "install", "vulnetix/tap/vulnetix"}},
			}}, true
		}
	}
	return InstallPlan{}, false
}

// RunInstall runs every step in order, streaming output lines to sink. The
// child gets the scrubbed environment and its own process group. A scoop
// bucket that is already added is not an error.
func RunInstall(ctx context.Context, p InstallPlan, sink func(string)) error {
	if len(p.Steps) == 0 {
		return errors.New("no install plan")
	}
	ctx, cancel := context.WithTimeout(ctx, InstallTimeout)
	defer cancel()
	for _, s := range p.Steps {
		tee := proc.NewLineTee(MaxOutputBytes, sink)
		cmd := exec.CommandContext(ctx, s.Argv[0], s.Argv[1:]...)
		cmd.Env = proc.ScrubbedEnv()
		cmd.Stdout, cmd.Stderr = tee, tee
		cmd.WaitDelay = 2 * time.Second
		proc.SetProcessGroup(cmd)
		err := cmd.Run()
		tee.Flush()
		if err != nil {
			if p.Manager == InstallScoop && len(s.Argv) > 1 && s.Argv[1] == "bucket" &&
				strings.Contains(strings.ToLower(tee.Content()), "already exists") {
				continue
			}
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	return nil
}
