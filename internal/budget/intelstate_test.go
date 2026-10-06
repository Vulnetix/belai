package budget

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// R23: the snapshot carries what the website cannot read from the local ledger:
// limits, pace, trend, runway and roles, as numbers and the harness's words.
func TestBudgetRule23_SnapshotCarriesTheIntel(t *testing.T) {
	now := at(14, 10, 0)
	in := Intel{
		Now:      now,
		Provider: "anthropic",
		Limits: []PlanLimit{
			{Provider: "anthropic", Window: WindowFiveHour, Used: 0.23, Reset: now.Add(3 * time.Hour), ObservedAt: now},
		},
		Pace:   Pace{Label: PaceComfortable, TokensPerHour: 410000, PctPerHour: 0.015},
		Trend:  Trend{Label: TrendEasing, Ratio: 0.5},
		Runway: Runway{Kind: RunwayLimit, Lasts: true, Until: 90 * time.Minute, Reset: 3 * time.Hour},
		Roles:  []RoleTokens{{Role: "agent", Tokens: 7600000}, {Role: "security", Tokens: 1000000}},
	}
	s := NewIntelState(in)
	if s.Version != 1 || s.Provider != "anthropic" || s.At != now.UnixMilli() {
		t.Fatalf("header = %+v", s)
	}
	l := s.Limits[0]
	if len(s.Limits) != 1 || l.Window != WindowFiveHour || l.Used != 0.23 || l.ResetsAt != now.Add(3*time.Hour).UnixMilli() || l.ObservedAt != now.UnixMilli() {
		t.Fatalf("limits = %+v", s.Limits)
	}
	if s.Pace.Label != PaceComfortable || s.Pace.TokensPerHour != 410000 || s.Trend.Label != TrendEasing || s.Trend.Ratio != 0.5 {
		t.Fatalf("pace/trend = %+v %+v", s.Pace, s.Trend)
	}
	if s.Runway.Kind != RunwayLimit || !s.Runway.Lasts || s.Runway.UntilSeconds != 5400 || s.Runway.ResetSeconds != 10800 {
		t.Fatalf("runway = %+v", s.Runway)
	}

	e := s.ToEntry("parent")
	if e.Type != "intel_state" || e.ParentID != "parent" || e.ID == "" {
		t.Fatalf("entry = %+v", e)
	}
	var back IntelState
	if err := json.Unmarshal([]byte(e.Content), &back); err != nil {
		t.Fatalf("content is not JSON: %v", err)
	}
	if back.Pace.Label != PaceComfortable || len(back.Roles) != 2 || back.Roles[0].Role != "agent" {
		t.Fatalf("round trip = %+v", back)
	}
	// The JSON keys are the contract the website reads.
	for _, key := range []string{`"version"`, `"provider"`, `"at"`, `"limits"`, `"resetsAt"`, `"observedAt"`, `"pace"`, `"tokensPerHour"`, `"trend"`, `"runway"`, `"untilSeconds"`, `"resetSeconds"`, `"roles"`} {
		if !strings.Contains(e.Content, key) {
			t.Errorf("content has no %s: %s", key, e.Content)
		}
	}
}

// R23: with nothing to report the lists are empty arrays, never null, so the
// website never has to guard a missing list.
func TestBudgetRule23_EmptyListsEncodeAsArrays(t *testing.T) {
	e := NewIntelState(Intel{Now: at(14, 10, 0), Provider: "p"}).ToEntry("")
	if !strings.Contains(e.Content, `"limits":[]`) || !strings.Contains(e.Content, `"roles":[]`) {
		t.Fatalf("content = %s", e.Content)
	}
}

// R24: the signature is what makes two snapshots the same news; token counts and
// the clock are not part of it.
func TestBudgetRule24_SignatureIgnoresNoise(t *testing.T) {
	now := at(14, 10, 0)
	mk := func(used float64, tph float64, at time.Time) IntelState {
		return NewIntelState(Intel{
			Now: at, Provider: "p",
			Limits: []PlanLimit{{Provider: "p", Window: WindowFiveHour, Used: used, Reset: now.Add(time.Hour), ObservedAt: at}},
			Pace:   Pace{Label: PaceActive, TokensPerHour: tph},
			Roles:  []RoleTokens{{Role: "agent", Tokens: int64(tph)}},
		})
	}
	base := mk(0.230, 100, now).Signature()
	if got := mk(0.231, 9000, now.Add(20*time.Second)).Signature(); got != base {
		t.Fatalf("token counts, roles and the clock must not change the signature: %q vs %q", got, base)
	}
	if got := mk(0.25, 100, now).Signature(); got == base {
		t.Fatal("a moved percent must change the signature")
	}

	// A new reset minute, or a changed word, is news too.
	moved := mk(0.230, 100, now)
	moved.Limits[0].ResetsAt += 5 * 60_000
	if moved.Signature() == base {
		t.Fatal("a reset that moved by minutes must change the signature")
	}
	hot := mk(0.230, 100, now)
	hot.Pace.Label = PaceHot
	if hot.Signature() == base {
		t.Fatal("a changed pace word must change the signature")
	}
	hot = mk(0.230, 100, now)
	hot.Runway = IntelStateRunway{Kind: RunwayLimit, Lasts: false}
	if hot.Signature() == base {
		t.Fatal("a changed runway must change the signature")
	}
	hot = mk(0.230, 100, now)
	hot.Trend.Label = TrendRising
	if hot.Signature() == base {
		t.Fatal("a changed trend word must change the signature")
	}
}

// E28: a snapshot taken after a window has reset leaves that limit out.
func TestBudgetEdge28_ResetWindowLeftOutOfSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), LedgerFile)
	now := at(25, 12, 0)
	r, c := openClock(t, path, "s", now)
	r.ObserveLimits([]PlanLimit{
		{Provider: "p", Window: WindowFiveHour, Used: 0.8, Reset: now.Add(30 * time.Minute), ObservedAt: now},
		{Provider: "p", Window: WindowSevenDay, Used: 0.2, Reset: now.Add(24 * time.Hour), ObservedAt: now},
	})
	if s := NewIntelState(r.Intel(c.now(), "p", nil)); len(s.Limits) != 2 {
		t.Fatalf("limits before the reset = %+v", s.Limits)
	}
	c.set(now.Add(31 * time.Minute))
	s := NewIntelState(r.Intel(c.now(), "p", nil))
	if len(s.Limits) != 1 || s.Limits[0].Window != WindowSevenDay {
		t.Fatalf("limits after the five hour reset = %+v, want only the weekly one", s.Limits)
	}
}

// E31: only the eight largest roles are kept.
func TestBudgetEdge31_CapsRoles(t *testing.T) {
	var roles []RoleTokens
	for i := 20; i > 0; i-- {
		roles = append(roles, RoleTokens{Role: "r", Tokens: int64(i)})
	}
	got := NewIntelState(Intel{Now: at(14, 10, 0), Roles: roles}).Roles
	if len(got) != maxIntelStateRoles || maxIntelStateRoles != 8 {
		t.Fatalf("roles = %d, want 8", len(got))
	}
	if got[0].Tokens != 20 || got[7].Tokens != 13 {
		t.Fatalf("roles kept = %+v, want the largest first", got)
	}
}
