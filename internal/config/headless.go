package config

import "fmt"

// Headless output defaults and bounds (docs/headless.md).
const (
	// DefaultHeadlessStdinBytes is the most a prompt read from standard input
	// may hold.
	DefaultHeadlessStdinBytes = 1 << 20
	// DefaultHeadlessEventTextCap is the most bytes of text one -output-format
	// event carries; longer text is split (text) or cut (tool_result).
	DefaultHeadlessEventTextCap = 4 << 10

	MinHeadlessStdinBytes   = 1 << 10
	MaxHeadlessStdinBytes   = 16 << 20
	MinHeadlessEventTextCap = 256
	MaxHeadlessEventTextCap = 1 << 20
)

// HeadlessOutputFormats are the values of headless.output_format and of the
// -output-format flag.
var HeadlessOutputFormats = []string{"text", "json", "stream-json"}

// HeadlessSettings is the contract of a noninteractive -prompt run with its
// caller (docs/headless.md): the default output format and two caps. A
// per-user block: the project layer is dropped, so a repository cannot change
// what a script that runs Belai receives or raise the caps.
type HeadlessSettings struct {
	// OutputFormat is the default of -output-format: text, json or
	// stream-json. Empty is text.
	OutputFormat string `json:"output_format,omitempty"`
	// MaxStdinBytes caps a prompt read from standard input. Default 1 MiB.
	MaxStdinBytes *int `json:"max_stdin_bytes,omitempty"`
	// EventTextCap caps the text one output event carries, in bytes.
	// Default 4 KiB.
	EventTextCap *int `json:"event_text_cap,omitempty"`
}

// HeadlessOutputFormat returns the effective default output format.
func (s Settings) HeadlessOutputFormat() string {
	if h := s.Headless; h != nil && h.OutputFormat != "" {
		return h.OutputFormat
	}
	return "text"
}

// HeadlessStdinLimit returns the effective cap on a prompt read from stdin.
func (s Settings) HeadlessStdinLimit() int {
	if h := s.Headless; h != nil && h.MaxStdinBytes != nil {
		return *h.MaxStdinBytes
	}
	return DefaultHeadlessStdinBytes
}

// HeadlessEventCap returns the effective cap on the text of one output event.
func (s Settings) HeadlessEventCap() int {
	if h := s.Headless; h != nil && h.EventTextCap != nil {
		return *h.EventTextCap
	}
	return DefaultHeadlessEventTextCap
}

func (h *HeadlessSettings) merge(from *HeadlessSettings) {
	if from.OutputFormat != "" {
		h.OutputFormat = from.OutputFormat
	}
	if from.MaxStdinBytes != nil {
		h.MaxStdinBytes = from.MaxStdinBytes
	}
	if from.EventTextCap != nil {
		h.EventTextCap = from.EventTextCap
	}
}

// ValidHeadlessOutputFormat reports whether s names an output format.
func ValidHeadlessOutputFormat(s string) bool {
	for _, f := range HeadlessOutputFormats {
		if s == f {
			return true
		}
	}
	return false
}

// ValidateHeadless rejects a headless block with an unknown format or a cap
// outside its bounds, naming the key.
func ValidateHeadless(s Settings) error {
	h := s.Headless
	if h == nil {
		return nil
	}
	if h.OutputFormat != "" && !ValidHeadlessOutputFormat(h.OutputFormat) {
		return fmt.Errorf("headless.output_format %q must be text, json or stream-json", h.OutputFormat)
	}
	for _, c := range []struct {
		key      string
		v        *int
		min, max int
	}{
		{"headless.max_stdin_bytes", h.MaxStdinBytes, MinHeadlessStdinBytes, MaxHeadlessStdinBytes},
		{"headless.event_text_cap", h.EventTextCap, MinHeadlessEventTextCap, MaxHeadlessEventTextCap},
	} {
		if c.v != nil && (*c.v < c.min || *c.v > c.max) {
			return fmt.Errorf("%s %d must be between %d and %d", c.key, *c.v, c.min, c.max)
		}
	}
	return nil
}
