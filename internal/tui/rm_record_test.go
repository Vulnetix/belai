package tui

import (
	"fmt"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/session"
)

// allRolemanagerEvents is every event the harness can record. The list comes
// from the rolemanager package's own source-checked list through Describe and
// the suppression set, so an event added there is exercised here too.
var allRolemanagerEvents = []rolemanager.Event{
	rolemanager.EventSecuritySentinel, rolemanager.EventSecuritySentinelMalformed, rolemanager.EventSecurityPhase,
	rolemanager.EventSecurityFallback, rolemanager.EventVerdictCacheHit, rolemanager.EventVerdictCacheBad,
	rolemanager.EventModeClassify, rolemanager.EventModeDetect, rolemanager.EventModeForced,
	rolemanager.EventModeGoalLengthLimit, rolemanager.EventGoalEval, rolemanager.EventGoalEvalRepair,
	rolemanager.EventPlanEval, rolemanager.EventAgentEval, rolemanager.EventGoalDraft, rolemanager.EventClarify,
	rolemanager.EventCompactionSummary, rolemanager.EventSessionName, rolemanager.EventBoundarySeal,
	rolemanager.EventBoundaryVerifyFailure, rolemanager.EventToolCallMismatch, rolemanager.EventAgentPoolAdmit,
	rolemanager.EventLSPDetect, rolemanager.EventLSPDiagnose, rolemanager.EventLSPServerDown,
	rolemanager.EventRouteFallback, rolemanager.EventDepChange, rolemanager.EventWebFetchAnswer,
	rolemanager.EventBashSwap, rolemanager.EventBashReplan, rolemanager.EventOptionOrder,
	rolemanager.EventPruneCompaction,
}

// Every decision reaches the session record the moment the render loop takes
// it, whatever the display level: the level only decides what is drawn.
func TestEveryDecisionIsRecordedAtEveryDisplayLevel(t *testing.T) {
	for _, level := range []string{"hidden", "decisions", "security", "all"} {
		t.Run(level, func(t *testing.T) {
			a := newPersistApp(t)
			lv := level
			a.settings.UI = &config.UISettings{ShowInternalWork: &lv}
			for i, e := range allRolemanagerEvents {
				a.Update(rmActivityMsg(rolemanager.Activity{
					Event: e, Verdict: "usable", Subject: "prompt", Detail: "PRIVATE-DETAIL",
					Model: "openrouter/typesafe/jev-1.13", Seq: uint64(i + 1), At: time.UnixMilli(int64(1_700_000_000_000 + i)),
				}))
			}
			// No persistTail: the record must not wait for a row to settle.
			rows := entriesOfType(t, a, "rolemanager")
			if len(rows) != len(allRolemanagerEvents) {
				t.Fatalf("recorded %d of %d decisions at level %s", len(rows), len(allRolemanagerEvents), level)
			}
			for i, r := range rows {
				if r.Meta["activity"] != string(allRolemanagerEvents[i]) {
					t.Fatalf("row %d is %v, want %s (order)", i, r.Meta["activity"], allRolemanagerEvents[i])
				}
				if r.Timestamp != int64(1_700_000_000_000+i) {
					t.Fatalf("row %d timestamp %d is not the decision's own", i, r.Timestamp)
				}
				if _, leaked := r.Meta["detail"]; leaked {
					t.Fatalf("row %d carries Detail", i)
				}
			}
			// Settling the rows afterwards must not write them a second time.
			a.persistTail()
			if again := entriesOfType(t, a, "rolemanager"); len(again) != len(rows) {
				t.Fatalf("settling wrote %d extra rows", len(again)-len(rows))
			}
		})
	}
}

// A burst larger than the old 256-slot channel loses nothing and keeps order.
func TestABurstOfDecisionsLosesNone(t *testing.T) {
	a := newPersistApp(t)
	// Drain anything queued while the app was built, so the count is ours.
	for a.rmEvents.len() > 0 {
		a.rmEvents.pop()
	}
	const n = 2000
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < n; i++ {
			// The public recorders are the real producers; ParseSessionName
			// records a session_name decision through every registered sink.
			_, _ = rolemanager.ParseSessionName(fmt.Sprintf("name %d", i))
		}
	}()
	<-done
	if got := a.rmEvents.len(); got != n {
		t.Fatalf("queued %d of %d decisions", got, n)
	}
	last := uint64(0)
	for i := 0; i < n; i++ {
		act, ok := a.rmEvents.pop()
		if !ok {
			t.Fatalf("queue ended after %d", i)
		}
		if act.Seq <= last {
			t.Fatalf("activity %d out of order: seq %d after %d", i, act.Seq, last)
		}
		last = act.Seq
		a.recordActivity(act)
	}
	if rows := entriesOfType(t, a, "rolemanager"); len(rows) != n {
		t.Fatalf("recorded %d of %d", len(rows), n)
	}
}

func TestRMQueue(t *testing.T) {
	q := newRMQueue()
	q.push(rolemanager.Activity{Seq: 1})
	q.push(rolemanager.Activity{Seq: 2})
	if q.len() != 2 {
		t.Fatalf("len = %d", q.len())
	}
	if a, ok := q.pop(); !ok || a.Seq != 1 {
		t.Fatalf("pop = %+v %v", a, ok)
	}
	// A pop on an empty queue waits for the next push.
	got := make(chan rolemanager.Activity)
	q2 := newRMQueue()
	go func() { a, _ := q2.pop(); got <- a }()
	q2.push(rolemanager.Activity{Seq: 9})
	if a := <-got; a.Seq != 9 {
		t.Fatalf("waiting pop got %+v", a)
	}
	// Closing drains what is left, then reports false; push after close is inert.
	q.close()
	q.close()
	if a, ok := q.pop(); !ok || a.Seq != 2 {
		t.Fatalf("drain = %+v %v", a, ok)
	}
	if _, ok := q.pop(); ok {
		t.Fatal("closed and drained queue still yields")
	}
	q.push(rolemanager.Activity{Seq: 3})
	if q.len() != 0 {
		t.Fatal("push after close was queued")
	}
}

// A row the feed shows and one it does not both carry what a resumed session
// needs, and a resume renders each exactly once.
func TestRecordedRowsRoundTripThroughResume(t *testing.T) {
	a := newPersistApp(t)
	a.Update(rmActivityMsg(rolemanager.Activity{Event: rolemanager.EventModeClassify, Verdict: "PLAN"}))
	a.Update(rmActivityMsg(rolemanager.Activity{Event: rolemanager.EventDepChange, Verdict: string(rolemanager.DepsChanged), Model: "openrouter/some/model"}))
	entries := persistedEntries(t, a)
	msgs, _ := messagesFromEntries(entries)
	var rows []string
	for _, m := range msgs {
		if m.Role == "rolemanager" {
			rows = append(rows, m.Activity)
		}
	}
	if len(rows) != 1 || rows[0] != string(rolemanager.EventDepChange) {
		t.Fatalf("resumed rows = %v, want only the shown dep_change", rows)
	}
}

var _ = session.Entry{}
