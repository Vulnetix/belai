package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLSPAccessors(t *testing.T) {
	var zero Settings

	if on, explicit := zero.LSPLanguageEnabled("go"); !on || explicit {
		t.Fatalf("unset LSPLanguageEnabled = (%v,%v), want (true,false)", on, explicit)
	}
	off := Settings{LSP: &LSPSettings{Languages: map[string]bool{"go": false}}}
	if on, explicit := off.LSPLanguageEnabled("go"); on || !explicit {
		t.Fatalf("explicit off = (%v,%v), want (false,true)", on, explicit)
	}
	if on, explicit := off.LSPLanguageEnabled("ts"); on || explicit {
		t.Fatalf("absent key in a populated map = (%v,%v), want (false,false)", on, explicit)
	}

	if got := zero.LSPTimeoutOr(800); got != 800 {
		t.Fatalf("unset timeout = %d, want 800", got)
	}
	if got := (Settings{LSP: &LSPSettings{TimeoutMS: 500}}).LSPTimeoutOr(800); got != 500 {
		t.Fatalf("set timeout = %d, want 500", got)
	}
	if got := zero.LSPMaxDiagnosticsOr(10); got != 10 {
		t.Fatalf("unset max = %d, want 10", got)
	}
	if got := (Settings{LSP: &LSPSettings{MaxDiagnostics: 5}}).LSPMaxDiagnosticsOr(10); got != 5 {
		t.Fatalf("set max = %d, want 5", got)
	}

	if got := zero.LSPServerFor("go"); got != "" {
		t.Fatalf("unset server = %q, want empty", got)
	}
	withSrv := Settings{LSP: &LSPSettings{Servers: map[string]string{"go": "/usr/bin/gopls"}}}
	if got := withSrv.LSPServerFor("go"); got != "/usr/bin/gopls" {
		t.Fatalf("server = %q", got)
	}
}

func TestLSPSettingsIsZero(t *testing.T) {
	var nilL *LSPSettings
	if !nilL.IsZero() {
		t.Fatal("nil must be zero")
	}
	if !(&LSPSettings{}).IsZero() {
		t.Fatal("empty must be zero")
	}
	if (&LSPSettings{Enabled: boolPtr(true)}).IsZero() {
		t.Fatal("enabled set must not be zero")
	}
	if (&LSPSettings{Languages: map[string]bool{"go": false}}).IsZero() {
		t.Fatal("languages set must not be zero")
	}
	if (&LSPSettings{Servers: map[string]string{"go": "/x"}}).IsZero() {
		t.Fatal("servers set must not be zero")
	}
	if (&LSPSettings{TimeoutMS: 800}).IsZero() {
		t.Fatal("timeout set must not be zero")
	}
	if (&LSPSettings{MaxDiagnostics: 5}).IsZero() {
		t.Fatal("max_diagnostics set must not be zero")
	}
}

func TestLSPSettingsMerge(t *testing.T) {
	base := &LSPSettings{TimeoutMS: 800}
	base.merge(nil)
	if base.TimeoutMS != 800 {
		t.Fatalf("nil merge mutated base: %+v", base)
	}

	from := &LSPSettings{
		Enabled:             boolPtr(false),
		Fallback:            boolPtr(false),
		ClassifyDiagnostics: boolPtr(true),
		TimeoutMS:           500,
		MaxDiagnostics:      5,
		Languages:           map[string]bool{"go": false},
		Servers:             map[string]string{"go": "/usr/bin/gopls"},
	}
	base.merge(from)

	if base.Enabled == nil || *base.Enabled {
		t.Fatalf("Enabled not merged: %+v", base)
	}
	if base.Fallback == nil || *base.Fallback {
		t.Fatalf("Fallback not merged: %+v", base)
	}
	if base.ClassifyDiagnostics == nil || !*base.ClassifyDiagnostics {
		t.Fatalf("ClassifyDiagnostics not merged: %+v", base)
	}
	if base.TimeoutMS != 500 || base.MaxDiagnostics != 5 {
		t.Fatalf("numeric fields not merged: %+v", base)
	}
	if v, ok := base.Languages["go"]; !ok || v {
		t.Fatalf("Languages not merged: %+v", base)
	}
	if v, ok := base.Servers["go"]; !ok || v != "/usr/bin/gopls" {
		t.Fatalf("Servers not merged: %+v", base)
	}

	// A second merge with nil maps must not clobber existing entries.
	base.merge(&LSPSettings{Enabled: boolPtr(true)})
	if v, ok := base.Languages["go"]; !ok || v {
		t.Fatalf("nil Languages clobbered existing entry: %+v", base)
	}
}

func TestValidateLSPMaxDiagnosticsRange(t *testing.T) {
	for _, v := range []int{51, 100} {
		if err := ValidateLSP(Settings{LSP: &LSPSettings{MaxDiagnostics: v}}); err == nil {
			t.Fatalf("max_diagnostics %d should fail", v)
		}
	}
	for _, v := range []int{1, 50} {
		if err := ValidateLSP(Settings{LSP: &LSPSettings{MaxDiagnostics: v}}); err != nil {
			t.Fatalf("max_diagnostics %d should validate: %v", v, err)
		}
	}
}

func TestValidateLSPServerFileChecks(t *testing.T) {
	dir := t.TempDir()

	// Missing file.
	if err := ValidateLSP(Settings{LSP: &LSPSettings{Servers: map[string]string{"go": filepath.Join(dir, "absent")}}}); err == nil {
		t.Fatal("missing server file should fail")
	}

	// A directory is not a regular file.
	subdir := filepath.Join(dir, "srv")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLSP(Settings{LSP: &LSPSettings{Servers: map[string]string{"go": subdir}}}); err == nil {
		t.Fatal("directory server should fail")
	}

	// A non-executable regular file must fail.
	noexec := filepath.Join(dir, "noexec")
	if err := os.WriteFile(noexec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLSP(Settings{LSP: &LSPSettings{Servers: map[string]string{"go": noexec}}}); err == nil {
		t.Fatal("non-executable server should fail")
	}

	// A valid executable whose path carries a shell metacharacter must fail.
	meta := filepath.Join(dir, "gop$ls")
	if err := os.WriteFile(meta, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLSP(Settings{LSP: &LSPSettings{Servers: map[string]string{"go": meta}}}); err == nil {
		t.Fatal("metacharacter server path should fail")
	}
}
