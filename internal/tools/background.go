// Background processes started by the model. Bash takes run_in_background,
// BashOutput reads what such a process has printed since the last read, and
// KillShell stops it. ProcessList names the ones still known. The launcher
// only ever sees processes the model started itself: a process the user
// started with !!cmd is never readable through these tools by handle, and can
// never be stopped by them.
package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/sandbox"
)

// BackgroundInfo is the harness-side view of one model-started process:
// facts only, never the command or any output.
type BackgroundInfo struct {
	ID       string
	State    string
	ExitCode int
	Started  time.Time
	Ended    time.Time
}

// ProcessLauncher is the seam internal/bgproc.Manager exposes to the
// background tools, so the tools package does not import the manager.
type ProcessLauncher interface {
	// StartBackground runs command through `sh -c` in dir under policy, in its
	// own process group with the scrubbed environment. It fails when the
	// concurrent-process cap is reached.
	StartBackground(command, dir string, policy sandbox.Policy) (BackgroundInfo, error)
	// ReadBackground returns output written since the previous read, at most
	// max bytes, keeping only lines matching filter when it is not empty.
	ReadBackground(id, filter string, max int) (string, BackgroundInfo, error)
	// StopBackground stops a model-started process.
	StopBackground(id string) (BackgroundInfo, error)
	// ListBackground returns the model-started processes still known.
	ListBackground() []BackgroundInfo
}

// backgroundMaxRead caps one BashOutput result, as Bash caps its output.
const backgroundMaxRead = 64 * 1024

const maxFilterBytes = 1024

// BashOutputName, KillShellName and ProcessListName are the tool names.
const (
	BashOutputName   = "BashOutput"
	KillShellName    = "KillShell"
	ProcessListName  = "ProcessList"
	backgroundNoLink = "background processes are not available in this session"
)

func describe(i BackgroundInfo) string {
	s := fmt.Sprintf("%s %s", i.ID, i.State)
	if i.State != "running" {
		s += fmt.Sprintf(" (exit status %d)", i.ExitCode)
	}
	return s
}

// startBackground is Bash's run_in_background path. The permission decision
// for the command was made before Execute, exactly as for a foreground call.
func startBackground(ctx context.Context, l ProcessLauncher, command, dir string) (Result, error) {
	if l == nil {
		return Result{}, fmt.Errorf("%s", backgroundNoLink)
	}
	info, err := l.StartBackground(command, dir, sandbox.FromContext(ctx))
	if err != nil {
		return Result{}, err
	}
	return BashResult(fmt.Sprintf("started background process %s; read its output with BashOutput (bash_id %q) and stop it with KillShell (shell_id %q)", info.ID, info.ID, info.ID)), nil
}

// BashOutput reads the output a background process has produced since the
// last read.
type BashOutput struct {
	Launcher ProcessLauncher
}

// Definition returns the static tool metadata.
func (b *BashOutput) Definition() Definition {
	return Definition{
		Name: BashOutputName,
		Description: "Read the new output of a background process started with Bash run_in_background. " +
			"Returns only what the process printed since the previous BashOutput call, at most 64 KiB (call again for the rest), with a status line naming whether it is still running. " +
			"An optional filter keeps only lines matching a regular expression; lines that do not match are skipped and are not returned by a later call.",
		Properties: map[string]Property{
			"bash_id": {Type: "string", Description: "The handle Bash returned, e.g. \"p1\""},
			"filter":  {Type: "string", Description: "Optional RE2 regular expression; only matching lines are returned"},
		},
		Required: []string{"bash_id"},
	}
}

// Kind returns "process": the output is the process's own arbitrary text.
func (b *BashOutput) Kind() Kind { return KindProcess }

// Mutates reports that BashOutput only reads.
func (b *BashOutput) Mutates() bool { return false }

// Subject returns the process handle.
func (b *BashOutput) Subject(args map[string]any) string {
	id, _ := argString(args, "bash_id")
	return id
}

// Execute returns the new output.
func (b *BashOutput) Execute(ctx context.Context, args map[string]any) (Result, error) {
	id, ok := argString(args, "bash_id")
	if !ok || id == "" {
		return Result{}, fmt.Errorf("missing bash_id argument")
	}
	if b.Launcher == nil {
		return Result{}, fmt.Errorf("%s", backgroundNoLink)
	}
	filter, _ := argString(args, "filter")
	if len(filter) > maxFilterBytes {
		return Result{}, fmt.Errorf("filter exceeds %d bytes", maxFilterBytes)
	}
	if filter != "" {
		if _, err := regexp.Compile(filter); err != nil {
			return Result{}, fmt.Errorf("invalid regex: %w", err)
		}
	}
	out, info, err := b.Launcher.ReadBackground(id, filter, backgroundMaxRead)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(out) == "" {
		out = "(no new output)"
	}
	return Result{Kind: KindProcess, Content: fmt.Sprintf("[%s]\n%s", describe(info), out)}, nil
}

// KillShell stops a background process the model started.
type KillShell struct {
	Launcher ProcessLauncher
}

// Definition returns the static tool metadata.
func (k *KillShell) Definition() Definition {
	return Definition{
		Name:        KillShellName,
		Description: "Stop a background process started with Bash run_in_background. Only processes the model started can be stopped; its output stays readable with BashOutput.",
		Properties: map[string]Property{
			"shell_id": {Type: "string", Description: "The handle Bash returned, e.g. \"p1\""},
		},
		Required: []string{"shell_id"},
	}
}

// Kind returns "process_ctl": the confirmation is composed by the harness.
func (k *KillShell) Kind() Kind { return KindProcessCtl }

// Mutates reports false: it can only end a process the model itself started
// with approval, so cleaning up after one never asks again.
func (k *KillShell) Mutates() bool { return false }

// Subject returns the process handle.
func (k *KillShell) Subject(args map[string]any) string {
	id, _ := argString(args, "shell_id")
	return id
}

// Execute stops the process.
func (k *KillShell) Execute(ctx context.Context, args map[string]any) (Result, error) {
	id, ok := argString(args, "shell_id")
	if !ok || id == "" {
		return Result{}, fmt.Errorf("missing shell_id argument")
	}
	if k.Launcher == nil {
		return Result{}, fmt.Errorf("%s", backgroundNoLink)
	}
	info, err := k.Launcher.StopBackground(id)
	if err != nil {
		return Result{}, err
	}
	return Result{Kind: KindProcessCtl, Content: "stopped: " + describe(info)}, nil
}

// ProcessList names the background processes the model started.
type ProcessList struct {
	Launcher ProcessLauncher
}

// Definition returns the static tool metadata.
func (p *ProcessList) Definition() Definition {
	return Definition{
		Name:        ProcessListName,
		Description: "List the background processes started with Bash run_in_background: handle, state and exit status. Use it to recover a handle after a long conversation.",
		Properties:  map[string]Property{},
	}
}

// Kind returns "native": handles and states composed by the harness.
func (p *ProcessList) Kind() Kind { return KindNative }

// Mutates reports that ProcessList only reads.
func (p *ProcessList) Mutates() bool { return false }

// Subject has nothing to match on.
func (p *ProcessList) Subject(map[string]any) string { return "" }

// Execute lists the processes.
func (p *ProcessList) Execute(ctx context.Context, args map[string]any) (Result, error) {
	if p.Launcher == nil {
		return Result{}, fmt.Errorf("%s", backgroundNoLink)
	}
	list := p.Launcher.ListBackground()
	if len(list) == 0 {
		return NativeResult("no background processes"), nil
	}
	lines := make([]string, len(list))
	for i, info := range list {
		lines[i] = describe(info)
	}
	return NativeResult(strings.Join(lines, "\n")), nil
}
