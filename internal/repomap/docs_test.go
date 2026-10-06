package repomap

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestAnalyticsPageStatesTheRepomapBounds pins the repomap numbers and the
// skipped directories docs/git-directories-analytics.md states.
func TestAnalyticsPageStatesTheRepomapBounds(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/git-directories-analytics.md")), " ")
	for _, want := range []string{
		"`scanTimeout` 5 s caps the whole scan",
		"`maxFiles` 2000 caps each top-level directory separately",
		"| repository map scan | `repomap.Scan` | 5 s overall / 2000 files per top-level dir and for the language walk |",
		"| layout listing | `repomap.layout` | 24 top-level dirs |",
		"| language counts | `repomap.languages` | 12 extensions |",
		"`skipDirs` — `.git`, `node_modules`, `vendor`, `target`, `dist`, `.vulnetix`, `.idea`, `.vscode` — are never descended",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the page does not say %q", want)
		}
	}
	if scanTimeout != 5*time.Second || maxFiles != 2000 {
		t.Errorf("scanTimeout %v and maxFiles %d disagree with the page", scanTimeout, maxFiles)
	}
	var got []string
	for d := range skipDirs {
		got = append(got, d)
	}
	sort.Strings(got)
	if strings.Join(got, " ") != ".git .idea .vscode .vulnetix dist node_modules target vendor" {
		t.Errorf("skipDirs = %v, the page lists eight directories", got)
	}
}
