package config

import "fmt"

// InstructionsSettings governs how the repository's agent instruction files
// (AGENTS.md, CLAUDE.md) reach agent-mode turns (docs/instruction-files.md).
// The root files are attached to every turn; a file in a subdirectory is
// attached the first time a tool touches a path beneath it. Every field is a
// plain value the harness reads; no model is asked anything. A nil field
// means the built-in default.
type InstructionsSettings struct {
	// Enabled switches the harness attachment of instruction files off. The
	// model can still Read one itself. nil means on.
	Enabled *bool `json:"enabled,omitempty"`
	// Nested switches the just-in-time delivery of per-directory files off,
	// leaving the root files. nil means on.
	Nested *bool `json:"nested,omitempty"`
	// MaxFiles is the most instruction files attached in one turn. Default 12,
	// 1 to 40.
	MaxFiles *int `json:"max_files,omitempty"`
	// FileBytes is the largest single instruction file attached; a bigger one
	// is left for the model to page through. Default 49152, 1024 to 262144.
	FileBytes *int `json:"file_bytes,omitempty"`
	// TotalBytes is the on-disk size of every instruction file attached in
	// one turn together. Default 131072, 1024 to 1048576.
	TotalBytes *int `json:"total_bytes,omitempty"`
}

// Instruction-file defaults.
const (
	DefaultInstructionsMaxFiles   = 12
	DefaultInstructionsFileBytes  = 48 * 1024
	DefaultInstructionsTotalBytes = 128 * 1024
)

// instructionsField describes one numeric key: where it lives, its default and
// its hard bounds. A repository may only lower a value, because a raised cap
// lets a cloned project put more of its own text in front of the model.
type instructionsField struct {
	key      string
	get      func(*InstructionsSettings) **int
	def      int
	min, max int
}

var instructionsFields = []instructionsField{
	{"instructions.max_files", func(c *InstructionsSettings) **int { return &c.MaxFiles }, DefaultInstructionsMaxFiles, 1, 40},
	{"instructions.file_bytes", func(c *InstructionsSettings) **int { return &c.FileBytes }, DefaultInstructionsFileBytes, 1024, 256 * 1024},
	{"instructions.total_bytes", func(c *InstructionsSettings) **int { return &c.TotalBytes }, DefaultInstructionsTotalBytes, 1024, 1024 * 1024},
}

// value is the field's effective value: the set one when it is in range,
// otherwise the default, so a bad value can never reach the delivery code.
func (f instructionsField) value(c *InstructionsSettings) int {
	if c == nil {
		return f.def
	}
	p := *f.get(c)
	if p == nil || *p < f.min || *p > f.max {
		return f.def
	}
	return *p
}

// InstructionFilesEnabled reports whether the harness attaches instruction
// files to agent turns. Default on.
func (s Settings) InstructionFilesEnabled() bool {
	return s.Instructions == nil || s.Instructions.Enabled == nil || *s.Instructions.Enabled
}

// InstructionFilesNested reports whether per-directory instruction files are
// delivered when a tool touches a path beneath them. Default on, and off
// whenever InstructionFilesEnabled is.
func (s Settings) InstructionFilesNested() bool {
	return s.InstructionFilesEnabled() && (s.Instructions == nil || s.Instructions.Nested == nil || *s.Instructions.Nested)
}

// InstructionMaxFiles is the most instruction files attached in one turn.
func (s Settings) InstructionMaxFiles() int { return instructionsFields[0].value(s.Instructions) }

// InstructionFileBytes is the largest single instruction file attached.
func (s Settings) InstructionFileBytes() int { return instructionsFields[1].value(s.Instructions) }

// InstructionTotalBytes is the on-disk size cap across one turn's files.
func (s Settings) InstructionTotalBytes() int { return instructionsFields[2].value(s.Instructions) }

// ValidateInstructions rejects a numeric value outside its bounds, naming the
// key. A repository's out-of-range value never gets here: mergeInstructions
// drops it.
func ValidateInstructions(s Settings) error {
	if s.Instructions == nil {
		return nil
	}
	for _, f := range instructionsFields {
		if p := *f.get(s.Instructions); p != nil && (*p < f.min || *p > f.max) {
			return fmt.Errorf("%s %d must be between %d and %d", f.key, *p, f.min, f.max)
		}
	}
	return nil
}

// mergeInstructions folds in over cur. A user layer sets any key in range. The
// project layer, which a cloned repository controls, may turn the two
// switches off but never on, and may set a number only when it is in range and
// below the effective value.
func mergeInstructions(cur, in *InstructionsSettings, project bool) *InstructionsSettings {
	if in == nil {
		return cur
	}
	out := &InstructionsSettings{}
	if cur != nil {
		*out = *cur
	}
	for _, p := range []struct{ in, out **bool }{{&in.Enabled, &out.Enabled}, {&in.Nested, &out.Nested}} {
		if *p.in == nil || (project && **p.in) {
			continue
		}
		v := **p.in
		*p.out = &v
	}
	for _, f := range instructionsFields {
		v := *f.get(in)
		if v == nil {
			continue
		}
		if project && (*v < f.min || *v > f.max || *v >= f.value(out)) {
			continue
		}
		n := *v
		*f.get(out) = &n
	}
	return out
}

// instructionsIgnored names the project keys mergeInstructions would not apply
// on top of user, for the note Resolve prints.
func instructionsIgnored(user, proj *InstructionsSettings) []string {
	if proj == nil {
		return nil
	}
	merged := mergeInstructions(user, proj, true)
	var out []string
	// A project true is dropped: a repository cannot turn a switch on. It is
	// worth a note only when it would have changed something.
	if proj.Enabled != nil && *proj.Enabled && !(Settings{Instructions: user}).InstructionFilesEnabled() {
		out = append(out, "instructions.enabled")
	}
	if proj.Nested != nil && *proj.Nested && user != nil && user.Nested != nil && !*user.Nested {
		out = append(out, "instructions.nested")
	}
	for _, f := range instructionsFields {
		if v := *f.get(proj); v != nil && f.value(merged) != *v {
			out = append(out, f.key)
		}
	}
	return out
}
