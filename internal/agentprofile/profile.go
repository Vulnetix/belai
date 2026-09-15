// Package agentprofile manages user-built agent profiles stored under
// ~/.signet/profiles/agents/. Profiles are richer than the flat prompt
// profiles used by /profile: each defines a system prompt, tool allowlist,
// operating mode, and autonomy level.
package agentprofile

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// AgentProfile is a named, reusable background-agent definition.
type AgentProfile struct {
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	SystemPrompt     string   `json:"system_prompt"`
	Tools            []string `json:"tools,omitempty"`
	Mode             string   `json:"mode"`
	Schedule         string   `json:"schedule,omitempty"`
	MonitorCondition string   `json:"monitor_condition,omitempty"`
	Reflection       bool     `json:"reflection,omitempty"`
	MaxIterations    int      `json:"max_iterations,omitempty"`
	Autonomy         string   `json:"autonomy,omitempty"`
}

// Mode values.
const (
	ModeSingle    = "single"
	ModeLoop      = "loop"
	ModeScheduled = "scheduled"
	ModeMonitor   = "monitor"
)

// Autonomy values.
const (
	AutonomySupervised = "supervised"
	AutonomyAutonomous = "autonomous"
)

var validModes = map[string]bool{
	ModeSingle:    true,
	ModeLoop:      true,
	ModeScheduled: true,
	ModeMonitor:   true,
}

var validAutonomy = map[string]bool{
	AutonomySupervised: true,
	AutonomyAutonomous: true,
}

// knownToolNames is the conservative built-in set used by Validate.
var knownToolNames = map[string]bool{
	"Bash":      true,
	"Glob":      true,
	"Grep":      true,
	"Read":      true,
	"WebFetch":  true,
	"WebSearch": true,
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// Validate checks a profile's required fields and known values.
func (p AgentProfile) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(p.Description) == "" {
		return errors.New("description is required")
	}
	if strings.TrimSpace(p.SystemPrompt) == "" {
		return errors.New("system_prompt is required")
	}
	if !validModes[p.Mode] {
		return fmt.Errorf("invalid mode %q (want one of: single, loop, scheduled, monitor)", p.Mode)
	}
	if p.Mode == ModeScheduled && strings.TrimSpace(p.Schedule) == "" {
		return errors.New("schedule is required when mode is scheduled")
	}
	if p.Mode == ModeMonitor && strings.TrimSpace(p.MonitorCondition) == "" {
		return errors.New("monitor_condition is required when mode is monitor")
	}
	if p.Autonomy != "" && !validAutonomy[p.Autonomy] {
		return fmt.Errorf("invalid autonomy %q (want supervised or autonomous)", p.Autonomy)
	}
	clean := strings.Trim(unsafeName.ReplaceAllString(p.Name, "_"), "._-")
	if clean == "" {
		return fmt.Errorf("invalid profile name %q", p.Name)
	}
	for _, t := range p.Tools {
		if !knownToolNames[t] {
			return fmt.Errorf("unknown tool %q", t)
		}
	}
	return nil
}

// ValidateWithRegistry checks tool names against a live registry.
func (p AgentProfile) ValidateWithRegistry(names []string) error {
	if err := p.Validate(); err != nil {
		return err
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	for _, t := range p.Tools {
		if !set[t] {
			return fmt.Errorf("unknown tool %q", t)
		}
	}
	return nil
}

// FileName returns the on-disk filename for this profile.
func (p AgentProfile) FileName() string {
	clean := strings.Trim(unsafeName.ReplaceAllString(p.Name, "_"), "._-")
	return clean + ".json"
}
