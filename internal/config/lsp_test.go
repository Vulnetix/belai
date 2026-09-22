package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateLSPEmpty(t *testing.T) {
	if err := ValidateLSP(Settings{}); err != nil {
		t.Fatalf("empty settings should validate: %v", err)
	}
}

func TestValidateLSPTimeoutRange(t *testing.T) {
	low := Settings{LSP: &LSPSettings{TimeoutMS: 50}}
	if err := ValidateLSP(low); err == nil {
		t.Fatal("expected error for timeout below range")
	}
	high := Settings{LSP: &LSPSettings{TimeoutMS: 300000}}
	if err := ValidateLSP(high); err == nil {
		t.Fatal("expected error for timeout above range")
	}
}

func TestValidateLSPUnknownLanguage(t *testing.T) {
	s := Settings{LSP: &LSPSettings{Languages: map[string]bool{"fortran": false}}}
	if err := ValidateLSP(s); err == nil {
		t.Fatal("expected error for unknown language")
	}
}

func TestValidateLSPServerPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "gopls")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := Settings{LSP: &LSPSettings{Servers: map[string]string{"go": bin}}}
	if err := ValidateLSP(s); err != nil {
		t.Fatalf("valid server path should validate: %v", err)
	}
}

func TestValidateLSPServerNotAbsolute(t *testing.T) {
	s := Settings{LSP: &LSPSettings{Servers: map[string]string{"go": "gopls"}}}
	if err := ValidateLSP(s); err == nil {
		t.Fatal("expected error for relative server path")
	}
}

func TestLSPProjectServersDropped(t *testing.T) {
	on := true
	global := Settings{LSP: &LSPSettings{
		Servers: map[string]string{"go": "/usr/bin/gopls"},
	}}
	proj := Settings{LSP: &LSPSettings{
		Servers: map[string]string{"go": "/evil/gopls"},
		Enabled: &on,
	}}
	merged := global.Override(proj)
	if got := merged.LSP.Servers["go"]; got != "/usr/bin/gopls" {
		t.Fatalf("project server not dropped, got %q", got)
	}
}

func TestLSPProjectEnabledCanOnlyTighten(t *testing.T) {
	on, off := true, false
	global := Settings{LSP: &LSPSettings{Enabled: &on}}
	proj := Settings{LSP: &LSPSettings{Enabled: &off}}
	merged := global.Override(proj)
	if merged.LSPEnabled() {
		t.Fatal("project layer should be able to disable lsp")
	}

	// Project true may not loosen a global false.
	global2 := Settings{LSP: &LSPSettings{Enabled: &off}}
	proj2 := Settings{LSP: &LSPSettings{Enabled: &on}}
	merged2 := global2.Override(proj2)
	if merged2.LSPEnabled() {
		t.Fatal("project layer should not be able to enable lsp when global disabled")
	}
}

func TestLSPProjectLanguagesFalseOnly(t *testing.T) {
	on, off := true, false
	global := Settings{LSP: &LSPSettings{Languages: map[string]bool{"go": false}}}
	proj := Settings{LSP: &LSPSettings{Languages: map[string]bool{"go": on, "ts": off}}}
	merged := global.Override(proj)
	if merged.LSP.Languages["go"] {
		t.Fatal("project true should not override global go=false")
	}
	if v, ok := merged.LSP.Languages["ts"]; !ok || v {
		t.Fatalf("project false entry ts should survive: got ok=%v v=%v", ok, v)
	}
}

func TestLSPTimeoutTakesMinimum(t *testing.T) {
	global := Settings{LSP: &LSPSettings{TimeoutMS: 800}}
	proj := Settings{LSP: &LSPSettings{TimeoutMS: 500}}
	merged := global.Override(proj)
	if merged.LSP.TimeoutMS != 500 {
		t.Fatalf("timeout = %d, want 500", merged.LSP.TimeoutMS)
	}
}
