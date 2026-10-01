package repoindex

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestAnalyticsPageStatesTheRepoindexBounds pins the checkout discovery and
// probe limits docs/git-directories-analytics.md states.
func TestAnalyticsPageStatesTheRepoindexBounds(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/git-directories-analytics.md")), " ")
	for _, want := range []string{
		"`scanTimeout` 3 s, `maxDirs` 500, `maxEntries` 200, `probeTimeout` 500 ms",
		"`skipDirs`: `node_modules`, `vendor`, `target`, `dist`",
		"| checkout discovery | `repoindex.Scan` | 3 s / 500 dirs / 200 entries |",
		"| remote probe | `repoindex.RunProbe` | 500 ms / 4 KiB output |",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the page does not say %q", want)
		}
	}
	if scanTimeout != 3*time.Second || maxDirs != 500 || maxEntries != 200 || probeTimeout != 500*time.Millisecond || probeMaxBytes != 4096 {
		t.Errorf("constants %v/%d/%d/%v/%d disagree with the page", scanTimeout, maxDirs, maxEntries, probeTimeout, probeMaxBytes)
	}
	var got []string
	for d := range skipDirs {
		got = append(got, d)
	}
	sort.Strings(got)
	if strings.Join(got, " ") != "dist node_modules target vendor" {
		t.Errorf("skipDirs = %v, the page lists four", got)
	}
}
