package rolemanager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/lsp"
	"github.com/vulnetix/signet/internal/nonce"
)

type fakeDiagnoser struct {
	report lsp.Report
	panic  bool
}

func (f *fakeDiagnoser) Diagnose(ctx context.Context, abs string, content []byte) lsp.Report {
	if f.panic {
		panic("intentional")
	}
	return f.report
}

func TestDiagnosticsGateRenderReturnsEmpty(t *testing.T) {
	pool := nonce.New()
	_ = pool.Seed(1)

	cases := []struct {
		name   string
		gate   DiagnosticsGate
		report lsp.Report
	}{
		{"nil diagnoser", DiagnosticsGate{Diagnoser: nil}, lsp.Report{}},
		{"unknown extension", DiagnosticsGate{Diagnoser: &fakeDiagnoser{}}, lsp.Report{}},
		{"empty report", DiagnosticsGate{Diagnoser: &fakeDiagnoser{report: lsp.Report{Language: "Go"}}}, lsp.Report{Language: "Go"}},
		{"unsupported", DiagnosticsGate{Diagnoser: &fakeDiagnoser{report: lsp.Report{Status: lsp.StatusUnsupported}}}, lsp.Report{Status: lsp.StatusUnsupported}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.gate.Render(context.Background(), "foo.txt", []byte("x"), pool)
			if got != "" {
				t.Fatalf("expected empty, got %q", got)
			}
		})
	}
}

func TestDiagnosticsGateRenderSealed(t *testing.T) {
	pool := nonce.New()
	_ = pool.Seed(1)
	gate := DiagnosticsGate{
		Diagnoser: &fakeDiagnoser{report: lsp.Report{
			Language: "Go",
			Status:   lsp.StatusReady,
			Rows:     []lsp.Row{{Severity: lsp.SeverityError, Line: 1, Col: 1, Message: "oops"}},
		}},
	}
	got := gate.Render(context.Background(), "foo.go", []byte("package main"), pool)
	if got == "" {
		t.Fatal("expected sealed block")
	}
	if !strings.HasPrefix(got, "<diagnostics") {
		t.Fatalf("expected diagnostics delimiter, got %q", got)
	}
	if !strings.Contains(got, "oops") {
		t.Fatalf("body missing message: %q", got)
	}
}

func TestDiagnosticsGateRenderPanicsSafe(t *testing.T) {
	pool := nonce.New()
	_ = pool.Seed(1)
	gate := DiagnosticsGate{Diagnoser: &fakeDiagnoser{panic: true}}
	got := gate.Render(context.Background(), "foo.go", []byte("x"), pool)
	if got != "" {
		t.Fatalf("expected empty after panic, got %q", got)
	}
}

func TestDiagnosticsGateObserverDetailIsStructured(t *testing.T) {
	pool := nonce.New()
	_ = pool.Seed(1)
	var captured Activity
	cancel := SetObserver(func(a Activity) {
		if a.Event == EventLSPDiagnose {
			captured = a
		}
	})
	defer cancel()

	gate := DiagnosticsGate{
		Diagnoser: &fakeDiagnoser{report: lsp.Report{
			Language: "Go",
			Status:   lsp.StatusReady,
			Rows:     []lsp.Row{{Severity: lsp.SeverityError, Line: 1, Col: 1, Message: "oops"}},
		}},
	}
	_ = gate.Render(context.Background(), "foo.go", []byte("x"), pool)
	if captured.Event != EventLSPDiagnose {
		t.Fatal("expected lsp_diagnose event")
	}
	if captured.Verdict != "problems" {
		t.Fatalf("verdict = %q, want problems", captured.Verdict)
	}
	if strings.Contains(captured.Detail, "oops") {
		t.Fatalf("raw message leaked into Detail: %q", captured.Detail)
	}
}

// TestRecordLSPDiagnoseVerdicts pins the verdict word for each report status
// and that clean/problems depend only on row presence, never on row content.
func TestRecordLSPDiagnoseVerdicts(t *testing.T) {
	rows := []lsp.Row{{Message: "super secret leak"}}
	cases := []struct {
		name   string
		report lsp.Report
		want   string
	}{
		{"ready clean", lsp.Report{Language: "Go", Status: lsp.StatusReady}, "clean"},
		{"ready problems", lsp.Report{Language: "Go", Status: lsp.StatusReady, Rows: rows}, "problems"},
		{"fallback clean", lsp.Report{Language: "Go", Status: lsp.StatusFallback}, "clean"},
		{"fallback problems", lsp.Report{Language: "Go", Status: lsp.StatusFallback, Rows: rows}, "problems"},
		{"warming", lsp.Report{Language: "Go", Status: lsp.StatusWarming}, "warming"},
		{"timeout", lsp.Report{Language: "Go", Status: lsp.StatusTimeout}, "timeout"},
		{"unavailable", lsp.Report{Language: "Go", Status: lsp.StatusUnavailable}, "unavailable"},
		{"unknown status", lsp.Report{Language: "Go", Status: lsp.Status("bogus")}, "unavailable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var captured Activity
			cancel := SetObserver(func(a Activity) {
				if a.Event == EventLSPDiagnose {
					captured = a
				}
			})
			defer cancel()
			recordLSPDiagnose(c.report)
			if captured.Verdict != c.want {
				t.Fatalf("verdict = %q, want %q", captured.Verdict, c.want)
			}
			if strings.Contains(captured.Detail, "secret") {
				t.Fatalf("row message leaked into Detail: %q", captured.Detail)
			}
		})
	}
}

func TestLangPhrase(t *testing.T) {
	if got := langPhrase(""); got != "unknown" {
		t.Fatalf("langPhrase(\"\") = %q, want unknown", got)
	}
	if got := langPhrase("Go"); got != "Go" {
		t.Fatalf("langPhrase(Go) = %q", got)
	}
}

func TestLSPLanguageEnabled(t *testing.T) {
	// Auto: no settings means every language is enabled.
	enabled := lspLanguageEnabled(config.Settings{})
	if !enabled("go") {
		t.Fatal("auto should enable go")
	}
	// Explicit off wins.
	enabled = lspLanguageEnabled(config.Settings{LSP: &config.LSPSettings{Languages: map[string]bool{"go": false}}})
	if enabled("go") {
		t.Fatal("explicit off should disable go")
	}
	// Explicit on wins.
	enabled = lspLanguageEnabled(config.Settings{LSP: &config.LSPSettings{Languages: map[string]bool{"go": true}}})
	if !enabled("go") {
		t.Fatal("explicit on should enable go")
	}
	// A language not named in the map stays auto-enabled.
	enabled = lspLanguageEnabled(config.Settings{LSP: &config.LSPSettings{Languages: map[string]bool{"go": false}}})
	if !enabled("rust") {
		t.Fatal("unnamed language should stay auto-enabled")
	}
}

func TestDiagnoseFile(t *testing.T) {
	ctx := context.Background()
	if got := DiagnoseFile(ctx, nil, "x.go"); got.Status != lsp.StatusUnsupported {
		t.Fatalf("nil diagnoser = %q, want unsupported", got.Status)
	}

	missing := filepath.Join(t.TempDir(), "nope.go")
	d := &fakeDiagnoser{report: lsp.Report{Language: "Go", Status: lsp.StatusReady}}
	if got := DiagnoseFile(ctx, d, missing); got.Status != lsp.StatusUnavailable {
		t.Fatalf("missing file = %q, want unavailable", got.Status)
	}

	path := filepath.Join(t.TempDir(), "ok.go")
	if err := os.WriteFile(path, []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DiagnoseFile(ctx, d, path); got.Status != lsp.StatusReady {
		t.Fatalf("existing file = %q, want ready (delegated)", got.Status)
	}
}

func TestDiagnosticsGateFromSettingsDisabled(t *testing.T) {
	off := false
	gate := DiagnosticsGateFromSettings(config.Settings{LSP: &config.LSPSettings{Enabled: &off}}, nil, true)
	if gate.Diagnoser != nil || gate.MaxRows != 0 {
		t.Fatalf("disabled gate = %+v, want zero gate", gate)
	}
}

func TestDiagnosticsGateFromSettingsEnabled(t *testing.T) {
	gate := DiagnosticsGateFromSettings(config.Settings{LSP: &config.LSPSettings{
		TimeoutMS:      900,
		MaxDiagnostics: 7,
		Servers:        map[string]string{"go": "/usr/bin/gopls"},
	}}, []string{"/tmp"}, true)
	if gate.Diagnoser == nil {
		t.Fatal("enabled gate must carry a diagnoser")
	}
	if gate.MaxRows != 7 || gate.Budget != 900*1000*1000 { // 900ms in ns
		t.Fatalf("gate = MaxRows %d Budget %v, want 7 / 900ms", gate.MaxRows, gate.Budget)
	}
	// With servers disallowed, the override must not be wired but fallback
	// syntax checks still attach a diagnoser.
	headless := DiagnosticsGateFromSettings(config.Settings{LSP: &config.LSPSettings{}}, []string{"/tmp"}, false)
	if headless.Diagnoser == nil {
		t.Fatal("headless gate (fallback-only) must still carry a diagnoser")
	}
}
