package session

import (
	"encoding/json"
	"math"
	"testing"
)

func TestCompactionStateRoundTrip(t *testing.T) {
	in := CompactionState{Fails: 2, CooldownLeft: 3, Tokens: 90_000, Window: 128_000}
	e := in.ToEntry("p")
	if e.Type != EntryTypeCompactionState || e.ParentID != "p" || e.ID == "" {
		t.Fatalf("entry = %+v", e)
	}
	// Through JSON, as the session file stores it.
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var back Entry
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if got := CompactionStateFromEntry(back); got != in {
		t.Fatalf("round trip = %+v, want %+v", got, in)
	}
}

func TestCompactionStateMalformedValuesAreClamped(t *testing.T) {
	cases := []struct {
		name string
		meta map[string]any
		want CompactionState
	}{
		{"strings", map[string]any{"fails": "9", "cooldown_left": []any{1}, "tokens": nil, "window": true}, CompactionState{}},
		{"negative", map[string]any{"fails": float64(-5), "cooldown_left": float64(-1), "tokens": float64(-9), "window": float64(-1)}, CompactionState{}},
		{"huge", map[string]any{"fails": float64(1e12), "cooldown_left": float64(1e12), "tokens": float64(1e30), "window": 1e30}, CompactionState{Fails: 1000, CooldownLeft: 20, Tokens: math.MaxInt32, Window: math.MaxInt32}},
		{"nan", map[string]any{"fails": math.NaN(), "cooldown_left": math.Inf(1)}, CompactionState{CooldownLeft: 0}},
		{"missing", nil, CompactionState{}},
	}
	for _, tc := range cases {
		got := CompactionStateFromEntry(Entry{Type: EntryTypeCompactionState, Meta: tc.meta})
		if got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestLatestCompactionStateTakesTheLast(t *testing.T) {
	if _, ok := LatestCompactionState([]Entry{{Type: "user"}}); ok {
		t.Fatal("found a state in a session without one")
	}
	entries := []Entry{
		CompactionState{Fails: 1}.ToEntry(""),
		{Type: "user", Content: "x"},
		CompactionState{Fails: 3, CooldownLeft: 4}.ToEntry(""),
	}
	got, ok := LatestCompactionState(entries)
	if !ok || got.Fails != 3 || got.CooldownLeft != 4 {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}
