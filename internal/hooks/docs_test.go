package hooks

import (
	"context"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestHooksPageEventTableMatchesTheCode keeps the Events table of
// docs/hooks.md equal to the valid events and to which of them can block.
func TestHooksPageEventTableMatchesTheCode(t *testing.T) {
	doc := docparity.Read(t, "docs/hooks.md")
	rows := map[string]string{}
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\|.*\\| (yes[^|]*|no) \\|$").FindAllStringSubmatch(doc, -1) {
		rows[m[1]] = strings.TrimSpace(m[2])
	}
	want := Events()
	sort.Strings(want)
	var got []string
	for e := range rows {
		got = append(got, e)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the page lists events %v, the code accepts %v", got, want)
	}
	for _, e := range want {
		if blocks := strings.HasPrefix(rows[e], "yes"); blocks != Blocking(e) {
			t.Errorf("%s: the page says can block = %v, the code says %v", e, blocks, Blocking(e))
		}
	}
}

// fieldsOf returns the json keys of a struct type.
func fieldsOf(v any) []string {
	var out []string
	typ := reflect.TypeOf(v)
	for i := 0; i < typ.NumField(); i++ {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			out = append(out, tag)
		}
	}
	return out
}

// TestHooksPageFieldsMatchTheCode keeps the hook file fields, the stdin
// fields and the stdout fields the page documents equal to the structs.
func TestHooksPageFieldsMatchTheCode(t *testing.T) {
	doc := docparity.Read(t, "docs/hooks.md")
	for _, f := range fieldsOf(Hook{}) {
		if !strings.Contains(doc, "| `"+f+"` |") {
			t.Errorf("the hook file table does not list %q", f)
		}
	}
	for _, f := range fieldsOf(Input{}) {
		if !strings.Contains(doc, "`"+f+"`") {
			t.Errorf("the stdin table does not list %q", f)
		}
	}
	for _, f := range fieldsOf(Output{}) {
		if !strings.Contains(doc, "`"+f+"`") {
			t.Errorf("the page does not describe the stdout field %q", f)
		}
	}
}

// TestHooksPageStatesTheLimits pins the timeout maximum and the context cap.
func TestHooksPageStatesTheLimits(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/hooks.md")), " ")
	for _, want := range []string{
		"default 5000, at most 60000",
		"`reason` and `additional_context` are each capped at 2 KiB",
		"Belai reads at most 64 KiB of a hook's stdout",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/hooks.md does not say %q", want)
		}
	}
	if MaxTimeoutMS != 60000 || MaxContextBytes != 2048 {
		t.Errorf("MaxTimeoutMS %d and MaxContextBytes %d disagree with the page", MaxTimeoutMS, MaxContextBytes)
	}
}

// A decision cut off by the output cap cannot be parsed, so on a blocking
// event it denies; on a non-blocking one it is only a warning.
func TestAHookDecisionCutOffByTheCapDenies(t *testing.T) {
	dir := t.TempDir()
	script(t, dir, "long.sh", `echo '{"decision":"allow","additional_context":"`+strings.Repeat("x", 200)+`"}'`)
	build := func(event string) *Set {
		s := set(dir, &Hook{Name: "long", Event: event, Command: "long.sh"})
		s.Runner.MaxBytes = 40
		return s
	}
	if o := build(EventPreTool).Dispatch(context.Background(), Input{Event: EventPreTool, ToolName: "Bash"}); o.Decision != DecisionDeny {
		t.Errorf("a cut-off decision on pre_tool gave %q, want deny", o.Decision)
	}
	if o := build(EventPostTool).Dispatch(context.Background(), Input{Event: EventPostTool, ToolName: "Bash"}); o.Decision == DecisionDeny {
		t.Errorf("a cut-off decision on post_tool denied; it can only warn")
	}
	// The same decision under the real cap is read whole.
	s := build(EventPreTool)
	s.Runner.MaxBytes = 64 * 1024
	if o := s.Dispatch(context.Background(), Input{Event: EventPreTool, ToolName: "Bash"}); o.Decision == DecisionDeny {
		t.Errorf("a whole decision under the cap denied")
	}
}
