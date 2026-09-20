// Package agentprofile manages user-built agent profiles stored under
// ~/.signet/profiles/agents/. Profiles are richer than the flat prompt
// profiles used by /profile: each defines a system prompt, tool allowlist,
// operating mode, and autonomy level.
package agentprofile

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/vulnetix/signet/internal/provider"
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
	Provider         string   `json:"provider,omitempty"`
	Model            string   `json:"model,omitempty"`
	Effort           string   `json:"effort,omitempty"`
	Guardrails       *bool    `json:"guardrails,omitempty"`
	AskPermission    *bool    `json:"ask_permission,omitempty"`
	// Builtin is true for embedded profiles and never persisted to disk.
	Builtin bool `json:"-"`
	// File is the base filename this profile was loaded from (e.g.
	// "triage-deps.json"). Empty means derive it from Name. It is never
	// serialised: the file's own name is the record.
	File string `json:"-"`
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

// isLoopMode reports whether a mode repeats unattended.
func isLoopMode(mode string) bool {
	return mode == ModeLoop || mode == ModeScheduled || mode == ModeMonitor
}

var validAutonomy = map[string]bool{
	AutonomySupervised: true,
	AutonomyAutonomous: true,
}

var validEfforts = map[string]bool{
	"low":    true,
	"medium": true,
	"high":   true,
	"none":   true,
}

// knownToolNames is the conservative built-in set used by Validate.
var knownToolNames = map[string]bool{
	"Bash":           true,
	"Cd":             true,
	"Edit":           true,
	"ExitPlanMode":   true,
	"Glob":           true,
	"Grep":           true,
	"Read":           true,
	"ReadSession":    true,
	"SearchMemory":   true,
	"SearchSessions": true,
	"WebFetch":       true,
	"WebSearch":      true,
	"Write":          true,
	"update_plan":    true,
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// BuiltinPrefix identifies harness-supplied agent profiles.
const BuiltinPrefix = "signet:"

//go:embed builtin/*.json
var builtinFS embed.FS

var builtinProfiles = loadBuiltins()

// IsBuiltin reports whether name is a built-in profile name.
func IsBuiltin(name string) bool {
	return strings.HasPrefix(name, BuiltinPrefix)
}

// builtinNames returns all builtin profile names.
func builtinNames() []string {
	names := make([]string, 0, len(builtinProfiles))
	for n := range builtinProfiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func loadBuiltins() map[string]AgentProfile {
	files, err := builtinFS.ReadDir("builtin")
	if err != nil {
		return map[string]AgentProfile{}
	}
	m := make(map[string]AgentProfile, len(files))
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		data, err := builtinFS.ReadFile("builtin/" + f.Name())
		if err != nil {
			continue
		}
		var p AgentProfile
		if err := json.Unmarshal(data, &p); err != nil {
			continue
		}
		p.Builtin = true
		if err := p.Validate(); err != nil {
			continue
		}
		m[p.Name] = p
	}
	return m
}

// Validate checks a profile's required fields and known values.
func (p AgentProfile) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("name is required")
	}
	if p.File != "" {
		if err := ValidateFileName(p.File); err != nil {
			return err
		}
		if !p.Builtin && collidesWithBuiltinFileName(p) {
			return fmt.Errorf("file name %q collides with a built-in profile", p.File)
		}
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
	if p.Effort != "" && !validEfforts[p.Effort] {
		return fmt.Errorf("invalid effort %q (want low, medium, high, or none)", p.Effort)
	}
	if p.Provider != "" && !provider.Builtin(p.Provider) && !provider.ValidCustomName(p.Provider) {
		return fmt.Errorf("invalid provider %q (not a built-in or valid custom-provider name)", p.Provider)
	}
	// Fail-closed: unattended, unbounded, and unguarded is three relaxations
	// stacked, which is too many for a single profile.
	if p.Guardrails != nil && !*p.Guardrails &&
		p.Autonomy == AutonomyAutonomous && isLoopMode(p.Mode) &&
		p.MaxIterations <= 0 {
		return errors.New("guardrails: off with autonomy: autonomous and a looping mode requires max_iterations")
	}
	clean := strings.Trim(unsafeName.ReplaceAllString(p.Name, "_"), ".-_")
	if clean == "" {
		return fmt.Errorf("invalid profile name %q", p.Name)
	}
	if !p.Builtin && IsBuiltin(p.Name) {
		return fmt.Errorf("profile name %q is reserved for built-in profiles", p.Name)
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

// FileName returns the on-disk filename for this profile. When File is set it
// is the record's name; otherwise it is derived from Name.
func (p AgentProfile) FileName() string {
	if p.File != "" {
		return p.File
	}
	return deriveFileName(p.Name)
}

// deriveFileName sanitises a profile name into its default on-disk filename.
func deriveFileName(name string) string {
	clean := strings.Trim(unsafeName.ReplaceAllString(name, "_"), ".-_")
	return clean + ".json"
}

// ValidateFileName reports whether file is a safe agent-profile filename: a
// non-empty .json basename with no path separators whose stem is unchanged by
// the name sanitiser, so the name the user typed is exactly the name on disk.
func ValidateFileName(file string) error {
	if file == "" {
		return errors.New("file name is required")
	}
	if !strings.HasSuffix(file, ".json") {
		return fmt.Errorf("file name %q must end in .json", file)
	}
	if strings.ContainsAny(file, `/\`) {
		return fmt.Errorf("file name %q must not contain path separators", file)
	}
	stem := strings.TrimSuffix(file, ".json")
	if stem == "" {
		return fmt.Errorf("file name %q must have a non-empty stem", file)
	}
	if unsafeName.ReplaceAllString(stem, "_") != stem {
		return fmt.Errorf("file name %q contains unsafe characters", file)
	}
	return nil
}

// KnownTools returns the sorted tool names Validate accepts. The editor's tool
// picker must offer exactly this set, not the live registry, or saves would
// fail on a tool the profile schema does not allow.
func KnownTools() []string {
	out := make([]string, 0, len(knownToolNames))
	for name := range knownToolNames {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ValidEfforts returns the effort values a profile accepts, including the
// "none" stop the classifier's own picker omits.
func ValidEfforts() []string {
	return []string{"low", "medium", "high", "none"}
}

// Stub returns a minimal profile that passes Validate, used by the list view's
// new-agent action and the builder-failure fallback.
func Stub(name string) AgentProfile {
	return AgentProfile{
		Name:         name,
		Description:  "New agent profile",
		SystemPrompt: "You are a helpful agent.",
		Mode:         ModeSingle,
		Autonomy:     AutonomySupervised,
	}
}

// builtinFileName returns the on-disk filename a user profile would collide
// with if it sanitises to the same name as a builtin.
func builtinFileName(name string) string {
	p := AgentProfile{Name: name}
	return p.FileName()
}

var builtinFileNamesOnce sync.Once
var builtinFileNamesList []string

// builtinFileNames returns the sanitised filenames of the embedded builtins,
// read directly from the embedded FS so Validate can run while builtinProfiles
// is still being initialised (avoiding an init cycle through builtinNames).
func builtinFileNames() []string {
	builtinFileNamesOnce.Do(func() {
		files, err := builtinFS.ReadDir("builtin")
		if err != nil {
			return
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			data, err := builtinFS.ReadFile("builtin/" + f.Name())
			if err != nil {
				continue
			}
			var p AgentProfile
			if json.Unmarshal(data, &p) != nil {
				continue
			}
			builtinFileNamesList = append(builtinFileNamesList, builtinFileName(p.Name))
		}
		sort.Strings(builtinFileNamesList)
	})
	return builtinFileNamesList
}

// collidesWithBuiltinFileName reports whether p's on-disk filename matches a
// built-in's on-disk filename.
func collidesWithBuiltinFileName(p AgentProfile) bool {
	fn := p.FileName()
	for _, b := range builtinFileNames() {
		if b == fn {
			return true
		}
	}
	return false
}
