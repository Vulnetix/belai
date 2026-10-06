package config

import (
	"os"
	"path/filepath"
	"strings"
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

// TestValidateLSPRangesAreInclusive pins the ranges docs/lsp.md states: a value
// on either edge is accepted, one past it fails the settings, and zero means
// unset.
func TestValidateLSPRangesAreInclusive(t *testing.T) {
	cases := []struct {
		key    string
		set    func(l *LSPSettings, v int)
		lo, hi int
		min    string
	}{
		{"timeout_ms", func(l *LSPSettings, v int) { l.TimeoutMS = v }, 100, 30000, "[100,30000]"},
		{"max_diagnostics", func(l *LSPSettings, v int) { l.MaxDiagnostics = v }, 1, 50, "[1,50]"},
		{"max_repair_attempts", func(l *LSPSettings, v int) { l.MaxRepairAttempts = v }, 2, 20, "[2,20]"},
	}
	for _, c := range cases {
		for _, v := range []int{0, c.lo, c.hi} {
			l := &LSPSettings{}
			c.set(l, v)
			if err := ValidateLSP(Settings{LSP: l}); err != nil {
				t.Errorf("%s=%d must validate: %v", c.key, v, err)
			}
		}
		for _, v := range []int{c.lo - 1, c.hi + 1, -1} {
			if v == 0 {
				continue // zero is unset, which the loop above accepts
			}
			l := &LSPSettings{}
			c.set(l, v)
			err := ValidateLSP(Settings{LSP: l})
			if err == nil {
				t.Errorf("%s=%d must be rejected", c.key, v)
				continue
			}
			if !strings.Contains(err.Error(), c.key) || !strings.Contains(err.Error(), c.min) {
				t.Errorf("%s=%d error %q should name the key and the range %s", c.key, v, err, c.min)
			}
		}
	}
}

// TestLSPRepairAttemptsAreClamped covers the accessor the agent reads: unset
// takes the caller's default, and a value is held inside [2,20].
func TestLSPRepairAttemptsAreClamped(t *testing.T) {
	for _, c := range []struct{ set, want int }{{0, 4}, {2, 2}, {7, 7}, {20, 20}, {1, 2}, {99, 20}} {
		s := Settings{LSP: &LSPSettings{MaxRepairAttempts: c.set}}
		if got := s.LSPMaxRepairAttemptsOr(4); got != c.want {
			t.Errorf("max_repair_attempts %d resolved to %d, want %d", c.set, got, c.want)
		}
	}
	if got := (Settings{}).LSPMaxRepairAttemptsOr(4); got != 4 {
		t.Errorf("no lsp block resolved to %d, want the default 4", got)
	}
}
