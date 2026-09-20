// Package commands holds harness commands backed by the Vulnetix CLI.
package commands

import (
	"fmt"
	"strings"
)

// Action is the parsed intent of a /vulnetix argument.
type Action int

const (
	ActionRun Action = iota
	ActionConfigure
	ActionList
	ActionStatus
	ActionHelp
	ActionFirewall
)

// Invocation is the result of parsing a /vulnetix argument.
type Invocation struct {
	Action Action
	Args   []string
	Raw    string
}

var actionNames = map[string]Action{
	"run":       ActionRun,
	"review":    ActionRun,
	"configure": ActionConfigure,
	"list":      ActionList,
	"status":    ActionStatus,
	"help":      ActionHelp,
	"firewall":  ActionFirewall,
}

// ParseInvocation parses a /vulnetix argument. Bare "" maps to ActionRun.
func ParseInvocation(arg string) (Invocation, error) {
	raw := strings.TrimSpace(arg)
	if raw == "" {
		return Invocation{Action: ActionRun, Raw: raw}, nil
	}
	fields := strings.Fields(raw)
	name := strings.ToLower(fields[0])
	act, ok := actionNames[name]
	if !ok {
		return Invocation{}, fmt.Errorf("unknown /vulnetix subcommand %q (try review, configure, list, status, firewall, help)", name)
	}
	return Invocation{Action: act, Args: fields[1:], Raw: raw}, nil
}
