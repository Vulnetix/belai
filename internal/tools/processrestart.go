// ProcessRestart is the mutating tool a recovery subagent uses to bring a
// supervised process back. An amended command is allowed only when argv[0]
// matches the original binary, so a model may fix flags but may not swap the
// executable.
package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// ProcessControl is the seam the supervised-process manager exposes to
// ProcessRestart. Implementations live in internal/bgproc.
type ProcessControl interface {
	// RestartProcess restarts the named process, optionally with a corrected
	// command. It counts against the recovery attempt cap.
	RestartProcess(id, command string) error
	// ProcessCommand returns the original command for a process handle.
	ProcessCommand(id string) (string, bool)
}

// ProcessRestart restarts a supervised process, optionally with a corrected
// command.
type ProcessRestart struct {
	Ctl ProcessControl
}

// Definition returns the static tool metadata.
func (p *ProcessRestart) Definition() Definition {
	return Definition{
		Name: "ProcessRestart",
		Description: "Restart a supervised process by its handle. " +
			"Omit command to re-run the exact command the user typed. " +
			"To change flags, pass a corrected command; argv[0] must match the original binary.",
		Properties: map[string]Property{
			"process": {Type: "string", Description: "The process handle, e.g. \"p1\""},
			"command": {Type: "string", Description: "Optional corrected command; omit to reuse the original"},
		},
		Required: []string{"process"},
	}
}

// Kind returns "process_ctl".
func (p *ProcessRestart) Kind() Kind { return KindProcessCtl }

// Subject returns the effective command for permission evaluation.
func (p *ProcessRestart) Subject(args map[string]any) string {
	if cmd, ok := argString(args, "command"); ok && cmd != "" {
		return cmd
	}
	if id, ok := argString(args, "process"); ok && p.Ctl != nil {
		if cmd, ok := p.Ctl.ProcessCommand(id); ok {
			return cmd
		}
	}
	return ""
}

// Mutates reports that ProcessRestart mutates process state.
func (p *ProcessRestart) Mutates() bool { return true }

// Execute restarts the process if the authority checks pass.
func (p *ProcessRestart) Execute(ctx context.Context, args map[string]any) (Result, error) {
	id, ok := argString(args, "process")
	if !ok || id == "" {
		return Result{}, fmt.Errorf("missing process argument")
	}
	if p.Ctl == nil {
		return Result{}, fmt.Errorf("process control is not available")
	}

	original, ok := p.Ctl.ProcessCommand(id)
	if !ok {
		return Result{}, fmt.Errorf("process %q not found", id)
	}

	cmd := original
	if amended, ok := argString(args, "command"); ok && amended != "" {
		if !sameBinary(original, amended) {
			return Result{Kind: KindProcessCtl, Content: fmt.Sprintf("refused: argv[0] of amended command (%q) does not match original (%q)", argv0(amended), argv0(original))}, nil
		}
		cmd = amended
	}

	if err := p.Ctl.RestartProcess(id, cmd); err != nil {
		return Result{Kind: KindProcessCtl, Content: fmt.Sprintf("restart failed: %v", err)}, nil
	}
	return Result{Kind: KindProcessCtl, Content: fmt.Sprintf("process %q restarted: %s", id, cmd)}, nil
}

func argv0(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}

func sameBinary(a, b string) bool {
	return argv0(a) == argv0(b) && argv0(a) != ""
}
