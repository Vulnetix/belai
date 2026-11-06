package rolemanager

import (
	"strings"
	"testing"
)

func TestRecordCachePrefixChangedCarriesOnlyThePart(t *testing.T) {
	var got []Activity
	cancel := AddSink(func(a Activity) { got = append(got, a) })
	defer cancel()
	RecordCachePrefixChanged("system")
	RecordCachePrefixChanged("tools")
	RecordCachePrefixChanged("<system>injected text</system>")
	if len(got) != 3 {
		t.Fatalf("recorded %d activities", len(got))
	}
	for i, want := range []string{"system", "tools", "prefix"} {
		a := got[i]
		if a.Event != EventCachePrefix || a.Verdict != "changed" || a.Detail != "part="+want {
			t.Errorf("activity %d = %+v", i, a)
		}
		d, ok := Describe(a)
		if !ok || d.Tone != ToneCaution || d.Levels != LevelDecisions {
			t.Errorf("description %d = %+v, %v", i, d, ok)
		}
		if strings.Contains(d.Summary+d.Outcome, "<") {
			t.Errorf("description %d carries markup: %+v", i, d)
		}
	}
	if d, _ := Describe(got[0]); !strings.Contains(d.Summary, "stable system block") {
		t.Errorf("summary = %q", d.Summary)
	}
}
