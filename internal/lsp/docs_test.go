package lsp

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/docparity"
)

// languageRows parses the Supported languages table of docs/lsp.md into its
// cells, keyed by the language ID in the first column.
func languageRows(t *testing.T) map[string][]string {
	t.Helper()
	rows := map[string][]string{}
	in := false
	for _, line := range strings.Split(docparity.Read(t, "docs/lsp.md"), "\n") {
		if strings.HasPrefix(line, "## Supported languages") {
			in = true
			continue
		}
		if in && strings.HasPrefix(line, "### ") {
			break
		}
		if !in || !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		rows[strings.Trim(cells[0], "`")] = cells
	}
	return rows
}

// TestLanguageTableMatchesTheRegistry keeps every row of the Supported
// languages table equal to the registry: the display name, the extensions and
// names it recognises, the server and its alternatives and the fallback.
func TestLanguageTableMatchesTheRegistry(t *testing.T) {
	rows := languageRows(t)
	if len(rows) != len(languages) {
		t.Errorf("the table has %d languages, the registry %d", len(rows), len(languages))
	}
	for _, l := range languages {
		cells, ok := rows[l.ID]
		if !ok {
			t.Errorf("docs/lsp.md has no row for %s", l.ID)
			continue
		}
		if cells[1] != l.Display {
			t.Errorf("%s: display %q, registry %q", l.ID, cells[1], l.Display)
		}
		want := strings.Join(l.Exts, " ")
		if len(l.Basenames) > 0 {
			want += "`, `" + strings.Join(l.Basenames, "`, `")
		}
		if got := strings.Trim(cells[2], "`"); got != want {
			t.Errorf("%s: extensions %q, registry %q", l.ID, got, want)
		}
		// The page leaves out the housekeeping arguments of clangd and jdtls.
		server := l.Server
		if len(l.Args) > 0 && l.Server != "clangd" && l.Server != "jdtls" {
			server += " " + strings.Join(l.Args, " ")
		}
		primary := "`" + server + "`"
		for _, a := range l.Alts {
			primary += " → `" + a + "`"
		}
		if cells[3] != primary {
			t.Errorf("%s: server %s, registry %s", l.ID, cells[3], primary)
		}
		switch {
		case l.Fallback == nil:
			if cells[4] != "**none**" {
				t.Errorf("%s has no fallback in the registry but the table says %s", l.ID, cells[4])
			}
		default:
			if strings.Contains(cells[4], "none") || !strings.Contains(cells[4], l.Fallback[0]) {
				t.Errorf("%s: fallback %s, registry %v", l.ID, cells[4], l.Fallback)
			}
		}
	}
}

// TestPageStatesTheManagerConstants pins the numbers in the Fallback behaviour
// and Limitations sections to the constants the manager uses.
func TestPageStatesTheManagerConstants(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/lsp.md")), " ")
	for _, want := range []string{
		"Three strikes in a row halve the budget, never below 100 ms",
		fmt.Sprintf("%s strikes in a row", map[int]string{5: "Five"}[strikeDisable]),
		fmt.Sprintf("only after a %d second cooldown", int(restartCooldown/time.Second)),
		fmt.Sprintf("gets %d seconds to initialise", int(initTimeout/time.Second)),
		fmt.Sprintf("cut off after %d seconds", int(fallbackTimeout/time.Second)),
		fmt.Sprintf("reused for %d ms", int(defaultCacheTTL/time.Millisecond)),
		fmt.Sprintf("unused for %d minutes is evicted", int(defaultIdleEviction/time.Minute)),
		fmt.Sprintf("at most %d live connections", defaultMaxLive),
		fmt.Sprintf("(%d live connections)", defaultMaxLive),
		fmt.Sprintf("capped at %d runes", maxSourceRunes),
		fmt.Sprintf("capped at %d runes", defaultMaxRunes),
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/lsp.md does not say %q", want)
		}
	}
	if strikeBudgetHalve != 3 {
		t.Errorf("strikeBudgetHalve = %d, but the page says three strikes halve the budget", strikeBudgetHalve)
	}
}
