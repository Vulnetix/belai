package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// KnownLSPLanguages is the allowlist of language-server keys used to validate
// settings.LSP.Languages and settings.LSP.Servers. It is kept in the config
// package (which must not import internal/lsp) and is parity-checked from the
// lsp package by TestKnownLanguagesMatchRegistry.
var KnownLSPLanguages = []string{
	"bash", "c", "cpp", "csharp", "dart", "go", "java", "objc",
	"python", "ruby", "rust", "swift", "ts", "zig",
}

// LSPSettings configures language-server diagnostics.
type LSPSettings struct {
	// Enabled, default true, toggles the whole feature.
	Enabled *bool `json:"enabled,omitempty"`
	// Fallback, default true, toggles fixed-argv syntax checks when no server is
	// installed.
	Fallback *bool `json:"fallback,omitempty"`
	// ClassifyDiagnostics, default false, sends every diagnostics block through
	// the security classifier before appending it to the tool result.
	ClassifyDiagnostics *bool `json:"classify_diagnostics,omitempty"`
	// Languages maps a language ID to an explicit on/off state. Absent means
	// auto (on when a server is detected and the feature is enabled).
	Languages map[string]bool `json:"languages,omitempty"`
	// Servers supplies global-layer binary overrides per language ID.
	Servers map[string]string `json:"servers,omitempty"`
	// TimeoutMS caps each diagnose call. Default 800; range [100,30000].
	TimeoutMS int `json:"timeout_ms,omitempty"`
	// MaxDiagnostics clamps the rendered rows. Default 10; range [1,50].
	MaxDiagnostics int `json:"max_diagnostics,omitempty"`
}

// LSPEnabled reports whether language-server diagnostics are enabled.
func (s Settings) LSPEnabled() bool {
	if s.LSP == nil || s.LSP.Enabled == nil {
		return true
	}
	return *s.LSP.Enabled
}

// LSPFallbackEnabled reports whether fallback syntax checks are enabled.
func (s Settings) LSPFallbackEnabled() bool {
	if s.LSP == nil || s.LSP.Fallback == nil {
		return true
	}
	return *s.LSP.Fallback
}

// LSPClassifyDiagnostics reports whether diagnostics blocks are classified.
func (s Settings) LSPClassifyDiagnostics() bool {
	return s.LSP != nil && s.LSP.ClassifyDiagnostics != nil && *s.LSP.ClassifyDiagnostics
}

// LSPLanguageEnabled reports whether a language is explicitly enabled. The
// second return is true only when the key is present in the user's settings.
func (s Settings) LSPLanguageEnabled(id string) (on bool, explicit bool) {
	if s.LSP == nil || s.LSP.Languages == nil {
		return true, false
	}
	v, ok := s.LSP.Languages[id]
	return v, ok
}

// LSPTimeoutOr returns the configured timeout or def.
func (s Settings) LSPTimeoutOr(def int) int {
	if s.LSP == nil || s.LSP.TimeoutMS == 0 {
		return def
	}
	return s.LSP.TimeoutMS
}

// LSPMaxDiagnosticsOr returns the configured max rows or def.
func (s Settings) LSPMaxDiagnosticsOr(def int) int {
	if s.LSP == nil || s.LSP.MaxDiagnostics == 0 {
		return def
	}
	return s.LSP.MaxDiagnostics
}

// LSPServerFor returns the binary override for a language ID, or "".
func (s Settings) LSPServerFor(id string) string {
	if s.LSP == nil {
		return ""
	}
	return s.LSP.Servers[id]
}

func (l *LSPSettings) merge(from *LSPSettings) {
	if from == nil {
		return
	}
	if from.Enabled != nil {
		l.Enabled = from.Enabled
	}
	if from.Fallback != nil {
		l.Fallback = from.Fallback
	}
	if from.ClassifyDiagnostics != nil {
		l.ClassifyDiagnostics = from.ClassifyDiagnostics
	}
	if from.TimeoutMS != 0 {
		l.TimeoutMS = from.TimeoutMS
	}
	if from.MaxDiagnostics != 0 {
		l.MaxDiagnostics = from.MaxDiagnostics
	}
	if len(from.Languages) > 0 {
		if l.Languages == nil {
			l.Languages = map[string]bool{}
		}
		for k, v := range from.Languages {
			l.Languages[k] = v
		}
	}
	if len(from.Servers) > 0 {
		if l.Servers == nil {
			l.Servers = map[string]string{}
		}
		for k, v := range from.Servers {
			l.Servers[k] = v
		}
	}
}

// IsZero reports whether the settings carry no overrides.
func (l *LSPSettings) IsZero() bool {
	if l == nil {
		return true
	}
	return l.Enabled == nil && l.Fallback == nil && l.ClassifyDiagnostics == nil &&
		len(l.Languages) == 0 && len(l.Servers) == 0 && l.TimeoutMS == 0 && l.MaxDiagnostics == 0
}

// ValidateLSP validates LSP settings. An invalid value fails the whole resolve
// closed so typos are reported rather than silently ignored.
func ValidateLSP(s Settings) error {
	if s.LSP == nil {
		return nil
	}
	if s.LSP.TimeoutMS != 0 && (s.LSP.TimeoutMS < 100 || s.LSP.TimeoutMS > 30000) {
		return fmt.Errorf("lsp.timeout_ms %d out of range [100,30000]", s.LSP.TimeoutMS)
	}
	if s.LSP.MaxDiagnostics != 0 && (s.LSP.MaxDiagnostics < 1 || s.LSP.MaxDiagnostics > 50) {
		return fmt.Errorf("lsp.max_diagnostics %d out of range [1,50]", s.LSP.MaxDiagnostics)
	}
	known := map[string]bool{}
	for _, id := range KnownLSPLanguages {
		known[id] = true
	}
	for id := range s.LSP.Languages {
		if !known[id] {
			return fmt.Errorf("lsp.languages: unknown language %q", id)
		}
	}
	for id, path := range s.LSP.Servers {
		if !known[id] {
			return fmt.Errorf("lsp.servers: unknown language %q", id)
		}
		if !filepath.IsAbs(path) {
			return fmt.Errorf("lsp.servers[%q] is not an absolute path: %q", id, path)
		}
		fi, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("lsp.servers[%q]: %w", id, err)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("lsp.servers[%q] is not a regular file", id)
		}
		if fi.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("lsp.servers[%q] is not executable", id)
		}
		for _, bad := range []string{"\x00", ";", "&", "|", "$", "`", "<", ">", "\"", "'"} {
			if strings.Contains(path, bad) {
				return fmt.Errorf("lsp.servers[%q] contains shell metacharacter", id)
			}
		}
	}
	return nil
}

// parseIntPointer helpers for tests; not exported.
var _ = strconv.Atoi
