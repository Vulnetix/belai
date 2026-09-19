// Package commands holds harness commands backed by the Vulnetix CLI.
package commands

import (
	"fmt"
	"strings"
)

// Action is the parsed intent of a /code-review argument.
type Action int

const (
	ActionRun Action = iota
	ActionConfigure
	ActionList
	ActionStatus
	ActionHelp
)

// Invocation is the result of parsing a /code-review argument.
type Invocation struct {
	Action Action
	Args   []string
	Raw    string
}

var actionNames = map[string]Action{
	"run":       ActionRun,
	"configure": ActionConfigure,
	"list":      ActionList,
	"status":    ActionStatus,
	"help":      ActionHelp,
}

// ParseInvocation parses a /code-review argument. Bare "" maps to ActionRun.
func ParseInvocation(arg string) (Invocation, error) {
	raw := strings.TrimSpace(arg)
	if raw == "" {
		return Invocation{Action: ActionRun, Raw: raw}, nil
	}
	fields := strings.Fields(raw)
	name := strings.ToLower(fields[0])
	act, ok := actionNames[name]
	if !ok {
		return Invocation{}, fmt.Errorf("unknown /code-review subcommand %q (try run, configure, list, status, help)", name)
	}
	return Invocation{Action: act, Args: fields[1:], Raw: raw}, nil
}
