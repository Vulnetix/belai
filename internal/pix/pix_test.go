package pix

import (
	"bytes"
	"os"
	"testing"

	"github.com/vulnetix/belai/internal/svgguard"
)

// The drawing a model is shown must itself be admitted, or every variant of it
// would be refused.
func TestTemplateIsAdmitted(t *testing.T) {
	out, err := svgguard.Sanitize(SVG)
	if err != nil {
		t.Fatalf("the Pix template is not admitted: %v", err)
	}
	if len(out) == 0 || len(out) > svgguard.MaxBytes {
		t.Fatalf("admitted template is %d bytes", len(out))
	}
}

// The guard's own corpus holds a copy of the template; the two must not drift.
func TestTemplateMatchesTheGuardCorpus(t *testing.T) {
	want, err := os.ReadFile("../svgguard/testdata/pix.svg")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(SVG, want) {
		t.Fatal("internal/pix/pix.svg and internal/svgguard/testdata/pix.svg differ; copy one over the other")
	}
}
