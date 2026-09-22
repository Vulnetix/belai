package rolemanager

import (
	"context"
	"strings"
	"testing"

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
