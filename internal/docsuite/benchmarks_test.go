package docsuite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/docparity"
)

// TestBenchmarkArmsAreWhatThePageSays loads every arm in docs/benchmarks.md as
// settings and checks its offload limits: the baseline is off, the shipped
// default equals the real default, and the aggressive arm offloads past about
// 6 KB.
func TestBenchmarkArmsAreWhatThePageSays(t *testing.T) {
	root := docparity.Root(t)
	doc := docparity.Read(t, "docs/benchmarks.md")
	files, err := filepath.Glob(filepath.Join(root, "bench", "arms", "*.json"))
	if err != nil || len(files) != 3 {
		t.Fatalf("bench/arms has %d files (%v), the page lists three", len(files), err)
	}
	for _, f := range files {
		rel := "bench/arms/" + filepath.Base(f)
		if !strings.Contains(doc, "| `"+rel+"` |") {
			t.Errorf("docs/benchmarks.md has no row for %s", rel)
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var s config.Settings
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Errorf("%s is not valid settings: %v", rel, err)
			continue
		}
		threshold, preview := s.OffloadLimits()
		switch filepath.Base(f) {
		case "offload-off.json":
			if s.OffloadEnabled() {
				t.Errorf("%s must turn offload off", rel)
			}
		case "offload-4000-1500.json":
			if !s.OffloadEnabled() || threshold != config.DefaultOffloadThresholdTokens || preview != config.DefaultOffloadPreviewTokens {
				t.Errorf("%s is %d/%d, but the page calls it the shipped default %d/%d", rel, threshold, preview, config.DefaultOffloadThresholdTokens, config.DefaultOffloadPreviewTokens)
			}
		case "offload-1500-750.json":
			// Four characters per estimated token: 1500 tokens is about 6 KB.
			if !s.OffloadEnabled() || threshold != 1500 || preview != 750 || threshold*4 != 6000 {
				t.Errorf("%s is %d/%d, the page says it offloads anything over ~6 KB", rel, threshold, preview)
			}
		}
	}
}

// TestBenchmarkToolingExists keeps the files and recipes the page names real.
func TestBenchmarkToolingExists(t *testing.T) {
	root := docparity.Root(t)
	for _, p := range []string{"bench/harbor/belai_agent.py", "internal/run/usage.go", "internal/run/usagesummary.go"} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("docs/benchmarks.md names %s: %v", p, err)
		}
	}
	just := docparity.Read(t, "justfile")
	for _, recipe := range []string{"build-bench", "bench"} {
		if !strings.Contains(just, "\n"+recipe+" ") && !strings.Contains(just, "\n"+recipe+":") {
			t.Errorf("the justfile has no %s recipe", recipe)
		}
	}
	adapter := docparity.Read(t, "bench/harbor/belai_agent.py")
	for _, opt := range []string{"settings:", "mode:", "effort:", "-trust-dir", "-ask-permission=false", "-usage-json"} {
		if !strings.Contains(adapter, opt) {
			t.Errorf("the Harbor adapter has no %s, which docs/benchmarks.md describes", opt)
		}
	}
}
