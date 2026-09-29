package rolemanager

import (
	"strings"
	"testing"
)

func TestEveryEventIsPresented(t *testing.T) {
	icons := map[string]Event{}
	for _, e := range allEvents {
		p, ok := presentations[e]
		if !ok {
			t.Errorf("event %q has no category and icon", e)
			continue
		}
		if p.Icon == "" || p.Category == "" {
			t.Errorf("event %q: empty presentation", e)
		}
		if other, dup := icons[p.Icon]; dup {
			t.Errorf("icon %q is used by both %q and %q", p.Icon, other, e)
		}
		icons[p.Icon] = e
	}
	if len(presentations) != len(allEvents) {
		t.Errorf("presentations has %d entries for %d events", len(presentations), len(allEvents))
	}
}

func TestActorOfSeparatesJevModelAndHarness(t *testing.T) {
	cases := []struct {
		name string
		a    Activity
		kind ActorKind
		want string
	}{
		{"hosted jev", Activity{Event: EventSecuritySentinel, Model: "openrouter/typesafe/jev-1.13"}, ActorJev, "jev-1.13"},
		{"bare jev", Activity{Event: EventModeDetect, Model: "typesafe/jev-1.13"}, ActorJev, "jev-1.13"},
		{"local decider", Activity{Event: EventBashSwap, Model: "decision-local/decider-4b"}, ActorJev, "decider-4b"},
		{"chat model", Activity{Event: EventSecuritySentinel, Model: "openrouter/anthropic/claude-haiku-4.5"}, ActorModel, "claude-haiku-4.5"},
		{"deterministic", Activity{Event: EventVerdictCacheHit}, ActorHarness, "belai"},
		{"jev job that named no backend", Activity{Event: EventLSPTriage}, ActorHarness, "belai"},
		{"chat role that named none", Activity{Event: EventGoalDraft}, ActorModel, ""},
		{"fallback ruling by the agent model", Activity{Event: EventSecurityFallback}, ActorModel, ""},
	}
	for _, c := range cases {
		got := ActorOf(c.a)
		if got.Kind != c.kind || got.Name() != c.want {
			t.Errorf("%s: got %s %q, want %s %q", c.name, got.Kind, got.Name(), c.kind, c.want)
		}
	}
}

// A Jev or chat decision never presents as the harness.
func TestNoModelDecisionIsNamedBelai(t *testing.T) {
	for _, m := range []string{"openrouter/typesafe/jev-1.13", "anthropic/claude-x", "decision-local/plumb-4b"} {
		for _, e := range allEvents {
			if ActorOf(Activity{Event: e, Model: m}).Name() == HarnessName {
				t.Errorf("event %q with model %q presents as %s", e, m, HarnessName)
			}
		}
	}
}

func TestRecordCarriesActorCategoryIconAndCause(t *testing.T) {
	a := Activity{Event: EventBashSwap, Verdict: "swapped", Subject: "Grep", Detail: "score=97", Model: "openrouter/typesafe/jev-1.13"}
	m := a.Record().Meta
	want := map[string]any{
		"schema": RecordSchema, "actor_kind": "jev", "actor": "jev-1.13", "category": "tools",
		"icon": "swap", "cause": "none", "score_pct": 97, "model": "typesafe/jev-1.13", "provider": "openrouter",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("meta[%q] = %v, want %v", k, m[k], v)
		}
	}
	if _, has := m["detail"]; has {
		t.Error("Detail must never be persisted")
	}
	for _, v := range m {
		if s, ok := v.(string); ok && strings.Contains(s, "score=") {
			t.Errorf("raw detail leaked into meta: %q", s)
		}
	}
	h := Activity{Event: EventVerdictCacheHit, Subject: "Bash"}.Record().Meta
	if h["actor_kind"] != "harness" || h["actor"] != "belai" || h["cause"] != "cache_hit" {
		t.Errorf("cache hit meta = %v", h)
	}
}

func TestScorePctAcceptsOnlyNumbers(t *testing.T) {
	for in, want := range map[string]int{"score=97": 97, "a=1 score=0": 0, "score=-1": -1, "score=abc": -1, "score=101": -1, "": -1, "xscore=9": -1} {
		if got := ScorePct(in); got != want {
			t.Errorf("ScorePct(%q) = %d, want %d", in, got, want)
		}
	}
}
