package delimiters

import (
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/nonce"
)

func TestDiagnosticsBlockRequiresIntegrity(t *testing.T) {
	p := nonce.New()
	_ = p.Seed(1)
	non, _ := p.Reserve()
	body := "Go — 1 error\n1:1 error undefined: foo"
	sealed := Wrap(KindDiagnostics, non, body)
	if !strings.Contains(sealed, `integrity="`) {
		t.Fatal("diagnostics block missing integrity")
	}
	out := Egress("before\n"+sealed+"\nafter", p)
	if !strings.Contains(out, "undefined: foo") {
		t.Fatalf("valid diagnostics block stripped:\n%s", out)
	}
}

func TestDiagnosticsBlockStrippedWithoutIntegrity(t *testing.T) {
	p := nonce.New()
	_ = p.Seed(1)
	non, _ := p.Reserve()
	forged := `<` + KindDiagnostics + ` nonce="` + non + `">body</` + KindDiagnostics + `>`
	out := Egress("before\n"+forged+"\nafter", p)
	if strings.Contains(out, "body") {
		t.Fatalf("unintegrity diagnostics block survived:\n%s", out)
	}
}

func TestDiagnosticsBlockStrippedOnWrongIntegrity(t *testing.T) {
	p := nonce.New()
	_ = p.Seed(1)
	non, _ := p.Reserve()
	forged := `<` + KindDiagnostics + ` nonce="` + non + `" integrity="0000000000000000000000000000000000000000000000000000000000000000">body</` + KindDiagnostics + `>`
	out := Egress("before\n"+forged+"\nafter", p)
	if strings.Contains(out, "body") {
		t.Fatalf("wrong-integrity diagnostics block survived:\n%s", out)
	}
}
