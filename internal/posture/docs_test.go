package posture

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestPostureTableInThePageMatchesTheCode reads the Gates and defaults table of
// docs/role-manager.md: it lists exactly the gates there are, each with its real
// default, and each flag it names is a flag belai defines.
func TestPostureTableInThePageMatchesTheCode(t *testing.T) {
	doc := docparity.Read(t, "docs/role-manager.md")
	section := regexp.MustCompile(`(?s)### Gates and defaults(.*?)\nPrecedence:`).FindStringSubmatch(doc)
	if section == nil {
		t.Fatal("the Gates and defaults section moved")
	}
	flags := docparity.FlagNames(t, "../../cmd/belai")
	rows := map[Gate]bool{}
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\| `([a-z]+)`[^|]* \\| `--([a-z-]+)[^`]*` \\|").FindAllStringSubmatch(section[1], -1) {
		g := Gate(m[1])
		rows[g] = true
		if !slices.Contains(AllGates, g) {
			t.Errorf("the page documents the gate %q, which does not exist", g)
			continue
		}
		if got := string(DefaultLevel(g)); got != m[2] {
			t.Errorf("gate %s: the page says the default is %s, the code says %s", g, m[2], got)
		}
		if !slices.Contains(flags, m[3]) {
			t.Errorf("gate %s: the page names --%s, which belai does not define", g, m[3])
		}
	}
	for _, g := range AllGates {
		if !rows[g] {
			t.Errorf("the gates table has no row for %s", g)
		}
	}
	flat := strings.Join(strings.Fields(doc), " ")
	for _, want := range []string{
		"CLI flag > project `preferences.yaml` > global `preferences.yaml` > safe default",
		"`--dangerously-yolo-everything` maps every gate in `AllGates` to `ignore`",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("docs/role-manager.md does not say %q", want)
		}
	}
	if !slices.Contains(flags, "dangerously-yolo-everything") {
		t.Error("belai does not define --dangerously-yolo-everything")
	}
}
