package notify

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestNotificationsPageListsEveryEvent keeps the Triggers table equal to the
// events the code defines.
func TestNotificationsPageListsEveryEvent(t *testing.T) {
	doc := docparity.Read(t, "docs/notifications.md")
	triggers := regexp.MustCompile(`(?s)## Triggers(.*?)## Backends`).FindStringSubmatch(doc)
	if triggers == nil {
		t.Fatal("no Triggers section")
	}
	var got []string
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\|").FindAllStringSubmatch(triggers[1], -1) {
		got = append(got, m[1])
	}
	want := append([]string{}, Events...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the page lists events %v, the code defines %v", got, want)
	}
}

// TestNotificationsPageStatesTheDefaults pins the default events, the backend
// table and the terminal detection variables.
func TestNotificationsPageStatesTheDefaults(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/notifications.md")), " ")
	for _, want := range []string{
		"the default set: `permission`, `clarify`, `plan_ready`, `goal_done` and `goal_stalled`",
		"capped at 48 characters",
		"five-second timeout",
		"`min_turn_seconds` | `30`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/notifications.md does not say %q", want)
		}
	}
	if strings.Join(DefaultEvents, ",") != "permission,clarify,plan_ready,goal_done,goal_stalled" {
		t.Errorf("DefaultEvents = %v, the page lists five", DefaultEvents)
	}
	for _, b := range Backends {
		if b != BackendAuto && !strings.Contains(doc, "| `"+b+"` |") {
			t.Errorf("the backend table does not list %s", b)
		}
	}
}

// TestNotificationsPageDetectionVariablesSelectTheFlavours checks each
// variable the page names selects the sequence the page says it does.
func TestNotificationsPageDetectionVariablesSelectTheFlavours(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/notifications.md")), " ")
	for _, v := range []string{"KITTY_WINDOW_ID", "KONSOLE_VERSION", "WT_SESSION", "ConEmuPID", "TMUX"} {
		if !strings.Contains(doc, "`"+v) {
			t.Errorf("docs/notifications.md does not name %s", v)
		}
	}
	for name, want := range map[string]oscFlavour{
		"KITTY_WINDOW_ID": osc777, "KONSOLE_VERSION": osc777, "WT_SESSION": osc9, "ConEmuPID": osc9,
	} {
		if got := env(map[string]string{name: "1"}, "linux").oscSupport(); got != want {
			t.Errorf("%s selects flavour %d, the page says %d", name, got, want)
		}
	}
	for vars, want := range map[string]oscFlavour{
		"WezTerm": osc777, "iTerm.app": osc9, "ghostty": osc9,
	} {
		if got := env(map[string]string{"TERM_PROGRAM": vars}, "linux").oscSupport(); got != want {
			t.Errorf("TERM_PROGRAM=%s selects flavour %d, the page says %d", vars, got, want)
		}
	}
	for _, term := range []string{"foot", "foot-extra", "rxvt-unicode"} {
		if got := env(map[string]string{"TERM": term}, "linux").oscSupport(); got != osc777 {
			t.Errorf("TERM=%s selects flavour %d, the page says OSC 777", term, got)
		}
	}
}
