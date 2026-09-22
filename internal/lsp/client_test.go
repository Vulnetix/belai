package lsp

import "testing"

func TestDiagnosticsToRows(t *testing.T) {
	d := []Diagnostic{
		{
			Range:    Range{Start: Position{Line: 0, Character: 8}, End: Position{Line: 0, Character: 12}},
			Severity: intPtr(1),
			Message:  "undefined: foo",
			Source:   "gopls",
		},
		{
			Range:    Range{Start: Position{Line: 2, Character: 0}, End: Position{Line: 2, Character: 4}},
			Severity: intPtr(2),
			Message:  "declared and not used",
		},
	}
	rows := diagnosticsToRows(d, "file:///tmp/foo.go")
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0].Line != 1 || rows[0].Col != 9 {
		t.Fatalf("first row line/col = %d:%d, want 1:9", rows[0].Line, rows[0].Col)
	}
	if rows[0].Severity != SeverityError {
		t.Fatalf("severity = %v", rows[0].Severity)
	}
}

func TestSeverityFrom(t *testing.T) {
	if severityFrom(intPtr(1)) != SeverityError {
		t.Fatal("severity 1 should be Error")
	}
	if severityFrom(nil) != SeverityError {
		t.Fatal("nil severity should default to Error")
	}
}

func intPtr(i int) *int { return &i }
