package rolemanager

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/docparity"
)

// declaredEvents lists every constant of type Event declared in activity.go,
// read from the source so the list cannot fall behind the code.
func declaredEvents(t *testing.T) []Event {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "activity.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			id, ok := vs.Type.(*ast.Ident)
			if !ok || id.Name != "Event" {
				continue
			}
			for i, name := range vs.Names {
				if i < len(vs.Values) {
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok {
						out = append(out, Event(lit.Value[1:len(lit.Value)-1]))
					}
				}
				_ = name
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// TestAllEventsListIsComplete fails when an Event constant is declared without
// being added to the canonical list the parity tests iterate, which is how a
// new decision would otherwise escape the description and record tests.
func TestAllEventsListIsComplete(t *testing.T) {
	declared := declaredEvents(t)
	if len(declared) < 20 {
		t.Fatalf("parsed only %d events", len(declared))
	}
	listed := map[Event]bool{}
	for _, e := range allEvents {
		listed[e] = true
	}
	for _, e := range declared {
		if !listed[e] {
			t.Errorf("event %q is declared but missing from allEvents", e)
		}
	}
	declaredSet := map[Event]bool{}
	for _, e := range declared {
		declaredSet[e] = true
	}
	for _, e := range allEvents {
		if !declaredSet[e] {
			t.Errorf("allEvents lists %q, which is not declared", e)
		}
	}
}

// Every event has a record, and a record says everything a resumed session
// needs: the event, and either the description that was shown or the hidden
// mark, never both.
func TestEveryEventHasARecord(t *testing.T) {
	at := time.UnixMilli(1_700_000_000_123)
	for _, e := range allEvents {
		act := Activity{Event: e, Verdict: "usable", Subject: "prompt", Detail: "SECRET-DETAIL blocks=1", Pass: 2, Model: "openrouter/typesafe/jev-1.13", At: at, Duration: 42 * time.Millisecond, Seq: 7}
		rec := act.Record()
		if rec.Content == "" || rec.Timestamp != at.UnixMilli() {
			t.Errorf("%s: content %q timestamp %d", e, rec.Content, rec.Timestamp)
		}
		if rec.Meta["activity"] != string(e) || rec.Meta["seq"] != uint64(7) || rec.Meta["duration_ms"] != int64(42) {
			t.Errorf("%s: meta = %v", e, rec.Meta)
		}
		if rec.Meta["provider"] != "openrouter" || rec.Meta["model"] != "typesafe/jev-1.13" {
			t.Errorf("%s: provider/model = %v / %v", e, rec.Meta["provider"], rec.Meta["model"])
		}
		if _, ok := rec.Meta["detail"]; ok {
			t.Errorf("%s: Detail reached the record", e)
		}
		for k, v := range rec.Meta {
			if s, ok := v.(string); ok && s == "SECRET-DETAIL blocks=1" {
				t.Errorf("%s: meta[%s] carries Detail", e, k)
			}
		}
		_, hidden := rec.Meta["hidden"]
		_, shown := rec.Meta["summary"]
		if hidden == shown {
			t.Errorf("%s: hidden=%v shown=%v, want exactly one", e, hidden, shown)
		}
		if hidden != suppressedEvents[e] {
			t.Errorf("%s: hidden=%v but suppressed=%v", e, hidden, suppressedEvents[e])
		}
	}
}

func TestRecordWithoutModelOrTimeLeavesThemOut(t *testing.T) {
	rec := Activity{Event: EventSessionName, Verdict: "valid"}.Record()
	for _, k := range []string{"model", "provider", "duration_ms", "seq"} {
		if _, ok := rec.Meta[k]; ok {
			t.Errorf("unset %s was recorded", k)
		}
	}
	if rec.Timestamp != 0 {
		t.Errorf("timestamp = %d for a zero time", rec.Timestamp)
	}
	rec = Activity{Event: EventSessionName, Model: "bare-model"}.Record()
	if rec.Meta["model"] != "bare-model" || rec.Meta["provider"] != nil {
		t.Errorf("a model with no provider = %v / %v", rec.Meta["provider"], rec.Meta["model"])
	}
}

func TestFactsCarryNoDetail(t *testing.T) {
	f := Facts(Activity{Event: EventSecuritySentinel, Verdict: string(SentinelPromptInjection), Subject: "bash", Pass: 1, Detail: "x"})
	if f["verdict"] != string(SentinelPromptInjection) || f["verdict_label"] == "" || f["subject"] != "bash" || f["pass"] != 1 {
		t.Fatalf("facts = %v", f)
	}
	if _, ok := f["detail"]; ok {
		t.Fatal("detail in facts")
	}
}

func TestVerdictLabelKnowsEverySentinelFamily(t *testing.T) {
	for _, v := range []string{
		string(SentinelSafe), string(PlanComplete), string(GoalComplete), string(DepsChanged), string(KeepBash), string(IntentPlan),
	} {
		if VerdictLabel(v) == "" {
			t.Errorf("no label for %q", v)
		}
	}
	if VerdictLabel("nonsense") != "" {
		t.Error("label for an unknown verdict")
	}
}

// A sink receives every activity, in order, with a sequence number, and a
// second sink and the observer are unaffected by the first.
func TestSinksReceiveEveryActivityInOrder(t *testing.T) {
	var mu sync.Mutex
	var a, b []Activity
	cancelA := AddSink(func(x Activity) { mu.Lock(); a = append(a, x); mu.Unlock() })
	cancelB := AddSink(func(x Activity) { mu.Lock(); b = append(b, x); mu.Unlock() })
	var seen []Activity
	cancelObs := SetObserver(func(x Activity) { mu.Lock(); seen = append(seen, x); mu.Unlock() })
	defer cancelObs()

	for i := 0; i < 50; i++ {
		record(EventSessionName, "valid", "", "", i)
	}
	cancelA()
	cancelA() // idempotent
	record(EventSessionName, "valid", "", "", 99)
	cancelB()
	record(EventSessionName, "valid", "", "", 100)

	mu.Lock()
	defer mu.Unlock()
	if len(a) != 50 || len(b) != 51 || len(seen) != 52 {
		t.Fatalf("sink A %d, sink B %d, observer %d; want 50, 51, 52", len(a), len(b), len(seen))
	}
	for i := 1; i < len(a); i++ {
		if a[i].Seq <= a[i-1].Seq || a[i].Pass != i {
			t.Fatalf("sink A out of order at %d: %+v after %+v", i, a[i], a[i-1])
		}
	}
	if a[0].Seq == 0 || a[0].At.IsZero() {
		t.Fatalf("activity not stamped: %+v", a[0])
	}
}

func TestASinkThatPanicsDoesNotStopTheOthers(t *testing.T) {
	var got int
	c1 := AddSink(func(Activity) { panic("boom") })
	c2 := AddSink(func(Activity) { got++ })
	defer c1()
	defer c2()
	record(EventSessionName, "valid", "", "", 0)
	if got != 1 {
		t.Fatalf("second sink ran %d times", got)
	}
	AddSink(nil)() // a nil sink is inert
}

// recordFields are every key a record can carry. A field added to Record
// must be added here and described in docs/role-manager.md.
var recordFields = []string{
	"activity", "verdict", "verdict_label", "subject", "pass", "provider", "model",
	"duration_ms", "seq", "summary", "outcome", "tone", "level", "hidden",
	"outcome_kind", "actor_kind", "actor", "category", "icon", "cause", "score_pct", "schema",
}

// TestSessionRecordIsDocumented keeps the "Session record" section of
// docs/role-manager.md naming every field a record can carry, and the field
// list in step with what Record produces.
func TestSessionRecordIsDocumented(t *testing.T) {
	doc := docparity.Read(t, "docs/role-manager.md")
	known := map[string]bool{}
	for _, k := range recordFields {
		known[k] = true
		if !strings.Contains(doc, k) {
			t.Errorf("docs/role-manager.md does not mention the record field %q", k)
		}
	}
	shown := Activity{Event: EventDepChange, Verdict: string(DepsChanged), Subject: "s", Pass: 1, Model: "p/m", At: time.Now(), Duration: time.Second, Seq: 1}.Record()
	hidden := Activity{Event: EventModeClassify, Verdict: "PLAN"}.Record()
	for _, rec := range []Record{shown, hidden} {
		for k := range rec.Meta {
			if !known[k] {
				t.Errorf("Record produced %q, which recordFields and the docs do not cover", k)
			}
		}
	}
	for _, phrase := range []string{"AddSink", "Activity.Record", "Session record"} {
		if !strings.Contains(doc, phrase) {
			t.Errorf("docs/role-manager.md does not mention %q", phrase)
		}
	}
}
