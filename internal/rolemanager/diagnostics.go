package rolemanager

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/delimiters"
	"github.com/vulnetix/signet/internal/lsp"
	"github.com/vulnetix/signet/internal/nonce"
	"github.com/vulnetix/signet/internal/sanitize"
)

// Diagnoser is the injection seam. internal/lsp.Manager implements it.
type Diagnoser interface {
	Diagnose(ctx context.Context, abs string, content []byte) lsp.Report
}

// DiagnosticsGate orchestrates the post-Edit/Write syntax check. The zero value
// is disabled, so a caller that never sets it pays nothing and risks nothing.
type DiagnosticsGate struct {
	Diagnoser Diagnoser
	Budget    time.Duration // 0 => 800ms
	MaxRows   int           // 0 => 10
	MaxRunes  int           // 0 => 200
}

// Render returns the sealed block to append to an Edit/Write result, or ""
// when there is nothing to say. It never returns an error and never panics.
func (g DiagnosticsGate) Render(ctx context.Context, abs string, content []byte, pool *nonce.Pool) string {
	if g.Diagnoser == nil {
		return ""
	}
	defer func() {
		_ = recover()
	}()

	lang := lsp.LanguageFor(abs)
	if lang == nil {
		return ""
	}

	budget := g.Budget
	if budget <= 0 {
		budget = 800 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	report := g.Diagnoser.Diagnose(ctx, abs, content)
	recordLSPDiagnose(report)

	if report.Status == lsp.StatusUnsupported {
		return ""
	}

	body := lsp.Render(report, g.MaxRows, g.MaxRunes)
	if body == "" {
		return ""
	}
	body = sanitize.Sanitize(body)

	non, err := pool.Reserve()
	if err != nil {
		return ""
	}
	return delimiters.Wrap(delimiters.KindDiagnostics, non, body)
}

func recordLSPDiagnose(r lsp.Report) {
	var verdict string
	switch r.Status {
	case lsp.StatusReady:
		if len(r.Rows) == 0 {
			verdict = "clean"
		} else {
			verdict = "problems"
		}
	case lsp.StatusFallback:
		if len(r.Rows) == 0 {
			verdict = "clean"
		} else {
			verdict = "problems"
		}
	case lsp.StatusWarming:
		verdict = "warming"
	case lsp.StatusTimeout:
		verdict = "timeout"
	default:
		verdict = "unavailable"
	}
	lang := langPhrase(string(r.Language))
	record(EventLSPDiagnose, verdict, lang, fmt.Sprintf("count=%d source=lsp lang=%s", len(r.Rows), lang), 0)
}

func langPhrase(lang string) string {
	if lang == "" {
		return "unknown"
	}
	return lang
}

// DiagnosticsGateFromSettings builds a gate from effective settings. When
// serversAllowed is false, live language servers are disabled but fallback
// syntax checks still run if enabled — this is the headless / non-TTY path.
func DiagnosticsGateFromSettings(s config.Settings, roots []string, serversAllowed bool) DiagnosticsGate {
	if !s.LSPEnabled() {
		return DiagnosticsGate{}
	}
	servers := map[string]string{}
	if serversAllowed {
		for _, id := range config.KnownLSPLanguages {
			if p := s.LSPServerFor(id); p != "" {
				servers[id] = p
			}
		}
	}
	m := lsp.NewManager(lsp.Options{
		Roots:    roots,
		Enabled:  lspLanguageEnabled(s),
		Fallback: s.LSPFallbackEnabled(),
		Budget:   time.Duration(s.LSPTimeoutOr(800)) * time.Millisecond,
		MaxLive:  6,
		Servers:  servers,
	})
	return DiagnosticsGate{
		Diagnoser: m,
		Budget:    time.Duration(s.LSPTimeoutOr(800)) * time.Millisecond,
		MaxRows:   s.LSPMaxDiagnosticsOr(10),
		MaxRunes:  200,
	}
}

func lspLanguageEnabled(s config.Settings) func(id string) bool {
	return func(id string) bool {
		on, explicit := s.LSPLanguageEnabled(id)
		if explicit {
			return on
		}
		return true
	}
}

// DiagnoseFile is a small helper for callers that already have the file bytes.
// It is not the primary API; DiagnosticsGate.Render is.
func DiagnoseFile(ctx context.Context, d Diagnoser, abs string) lsp.Report {
	if d == nil {
		return lsp.Report{Status: lsp.StatusUnsupported}
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return lsp.Report{Status: lsp.StatusUnavailable}
	}
	return d.Diagnose(ctx, abs, body)
}
