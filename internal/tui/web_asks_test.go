package tui

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/tui/components"
)

func entriesOfType(t *testing.T, a *App, typ string) []session.Entry {
	t.Helper()
	var out []session.Entry
	for _, e := range persistedEntries(t, a) {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func openPermissionAsk(t *testing.T, a *App) (chan agent.PermissionAskReply, string) {
	t.Helper()
	a.cancel = func() {}
	reply := make(chan agent.PermissionAskReply, 1)
	preview := filediff.Preview("main.go", "package main\n", "package main\n\nfunc main() {}\n")
	ask := &agent.AskRequest{Name: "Edit", Subject: "main.go", Args: map[string]any{"path": "main.go"}, Preview: &preview}
	a.Update(agentEventMsg{Kind: agent.EventPermissionAskKind, Ask: ask, AskReply: reply})
	asks := entriesOfType(t, a, "ask")
	if len(asks) != 1 {
		t.Fatalf("ask entries = %d, want 1", len(asks))
	}
	return reply, asks[0].ID
}

// A permission ask is written to the session as an ask entry keyed by its
// ask id, carrying what the website needs to decide: tool, subject, the rule
// allow-always would add, and the diff.
func TestPermissionAskIsRecorded(t *testing.T) {
	a, _ := newSyncApp(t)
	_, askID := openPermissionAsk(t, a)
	e := entriesOfType(t, a, "ask")[0]
	if e.Meta["kind"] != askPermission || e.Meta["ask_id"] != askID || e.Meta["tool"] != "Edit" ||
		e.Meta["rule"] != "Edit(main.go)" {
		t.Fatalf("ask meta = %+v", e.Meta)
	}
	w := filediff.WireFromMeta(e.Meta["diff"])
	if w == nil || len(w.Files) != 1 || w.Files[0].Adds == 0 {
		t.Fatalf("ask diff = %+v", e.Meta["diff"])
	}
	if a.permAskState.askID != askID {
		t.Fatal("open ask id not tracked")
	}
}

// Allow-always from the web writes the same rule the host key writes,
// releases the agent, records the answer as the web's, and acks it accepted
// with the ask_answer entry id.
func TestWebAnswerAllowAlways(t *testing.T) {
	a, rec := newSyncApp(t)
	reply, askID := openPermissionAsk(t, a)
	payload, _ := json.Marshal(map[string]string{"decision": askAllowAlways})

	a.handleRemoteAnswer(sessionsync.RemoteAnswer{ID: "w1", SessionID: a.sessionID, AskID: askID, Kind: askPermission, Payload: payload})

	select {
	case r := <-reply:
		if !r.Allow {
			t.Fatal("allow_always must allow")
		}
	case <-time.After(time.Second):
		t.Fatal("agent was not released")
	}
	if !slices.Contains(a.settings.Permissions.Allow, "Edit(main.go)") {
		t.Fatalf("allow rules = %v", a.settings.Permissions.Allow)
	}
	answers := entriesOfType(t, a, "ask_answer")
	if len(answers) != 1 || answers[0].ID != answerEntryID(askID) || answers[0].Meta["source"] != answerFromWeb ||
		answers[0].Meta["decision"] != askAllowAlways || answers[0].Meta["remote_answer_id"] != "w1" {
		t.Fatalf("ask_answer = %+v", answers)
	}
	if got := rec.ack(t, "answer:w1"); got["status"] != sessionsync.AckAccepted || got["entryId"] != answerEntryID(askID) {
		t.Fatalf("ack = %v", got)
	}
	if a.view == viewPermissionAsk {
		t.Fatal("the approval view is still up")
	}
}

// The first answer wins: once the host answered, a web answer for the same
// ask is refused and changes nothing.
func TestWebAnswerLosesToHost(t *testing.T) {
	a, rec := newSyncApp(t)
	reply, askID := openPermissionAsk(t, a)
	a.settlePermissionAsk(askDeny, answerFromHost, "")
	<-reply

	payload, _ := json.Marshal(map[string]string{"decision": askAllowOnce})
	a.handleRemoteAnswer(sessionsync.RemoteAnswer{ID: "w2", SessionID: a.sessionID, AskID: askID, Kind: askPermission, Payload: payload})
	if got := rec.ack(t, "answer:w2"); got["status"] != sessionsync.AckRefused {
		t.Fatalf("ack = %v, want refused", got)
	}
	select {
	case <-reply:
		t.Fatal("a second decision reached the agent")
	default:
	}
	if n := len(entriesOfType(t, a, "ask_answer")); n != 1 {
		t.Fatalf("ask_answer entries = %d, want 1", n)
	}
}

func TestWebAnswerRejectsUnknownDecisionAndWrongKind(t *testing.T) {
	a, rec := newSyncApp(t)
	_, askID := openPermissionAsk(t, a)
	a.handleRemoteAnswer(sessionsync.RemoteAnswer{ID: "w3", SessionID: a.sessionID, AskID: askID, Kind: askPermission,
		Payload: json.RawMessage(`{"decision":"sudo"}`)})
	if got := rec.ack(t, "answer:w3"); got["status"] != sessionsync.AckRefused {
		t.Fatalf("unknown decision ack = %v", got)
	}
	a.handleRemoteAnswer(sessionsync.RemoteAnswer{ID: "w4", SessionID: a.sessionID, AskID: askID, Kind: askClarify,
		Payload: json.RawMessage(`{"answers":[]}`)})
	if got := rec.ack(t, "answer:w4"); got["status"] != sessionsync.AckRefused {
		t.Fatalf("wrong kind ack = %v", got)
	}
	if a.permAskState.reply == nil {
		t.Fatal("a refused answer closed the ask")
	}
}

var testQuestionnaire = clarify.Questionnaire{Groups: []clarify.Group{
	{Context: "Which database?", Options: []clarify.Option{{Label: "Postgres"}, {Label: "SQLite"}}},
	{Context: "Which features?", Multi: true, Options: []clarify.Option{{Label: "Auth"}, {Label: "Search"}, {Label: "Export"}}},
}}

func TestWebAnswerClarify(t *testing.T) {
	a, rec := newSyncApp(t)
	a.cancel = func() {}
	reply := make(chan clarify.Answers, 1)
	a.Update(agentEventMsg{Kind: agent.EventClarifyAskKind, Clarify: &testQuestionnaire, Reply: reply})
	asks := entriesOfType(t, a, "ask")
	if len(asks) != 1 || asks[0].Meta["kind"] != askClarify || asks[0].Meta["questionnaire"] == nil {
		t.Fatalf("ask = %+v", asks)
	}
	askID := asks[0].ID

	// Two choices for a single-choice question is refused, and the ask stays open.
	a.handleRemoteAnswer(sessionsync.RemoteAnswer{ID: "c1", SessionID: a.sessionID, AskID: askID, Kind: askClarify,
		Payload: json.RawMessage(`{"answers":[{"group":0,"chosen":[0,1]}]}`)})
	if got := rec.ack(t, "answer:c1"); got["status"] != sessionsync.AckRefused {
		t.Fatalf("ack = %v", got)
	}

	a.handleRemoteAnswer(sessionsync.RemoteAnswer{ID: "c2", SessionID: a.sessionID, AskID: askID, Kind: askClarify,
		Payload: json.RawMessage(`{"answers":[{"group":1,"chosen":[0,2],"note":"keep it small\u001b[2J"}]}`)})
	var got clarify.Answers
	select {
	case got = <-reply:
	case <-time.After(time.Second):
		t.Fatal("answers never reached the agent")
	}
	if len(got.Items) != 2 || !got.Items[0].Skipped || !slices.Equal(got.Items[1].Chosen, []int{0, 2}) ||
		strings.Contains(got.Items[1].Note, "\x1b") {
		t.Fatalf("answers = %+v", got)
	}
	if ack := rec.ack(t, "answer:c2"); ack["status"] != sessionsync.AckAccepted {
		t.Fatalf("ack = %v", ack)
	}
	if n := len(entriesOfType(t, a, "ask_answer")); n != 1 {
		t.Fatalf("ask_answer entries = %d", n)
	}
}

func TestParseWebAnswers(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr bool
		decline bool
	}{
		{"decline", `{"decline":true}`, false, true},
		{"unknown group", `{"answers":[{"group":5,"chosen":[0]}]}`, true, false},
		{"negative option", `{"answers":[{"group":0,"chosen":[-1]}]}`, true, false},
		{"option out of range", `{"answers":[{"group":1,"chosen":[3]}]}`, true, false},
		{"twice", `{"answers":[{"group":0,"chosen":[0]},{"group":0,"chosen":[1]}]}`, true, false},
		{"not json", `nope`, true, false},
		{"ok", `{"answers":[{"group":0,"chosen":[1]},{"group":1,"skipped":true}]}`, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, declined, err := parseWebAnswers(testQuestionnaire, json.RawMessage(c.payload))
			if (err != nil) != c.wantErr || declined != c.decline {
				t.Fatalf("err=%v declined=%v", err, declined)
			}
			if err == nil && !declined && len(got.Items) != len(testQuestionnaire.Groups) {
				t.Fatalf("items = %d, want one per group", len(got.Items))
			}
		})
	}
}

// A turn records its start and, only after its last rows are on disk, its
// end — so a reader never sees a turn end before its reply.
func TestTurnStateBracketsTheTurn(t *testing.T) {
	a := newPersistApp(t)
	a.openTurn()
	a.cancel = func() {}
	a.messages = append(a.messages, components.Message{Role: "system", Content: "working"})
	a.closeTurn("ended")
	a.persistTail() // still in flight: the end waits
	if n := len(entriesOfType(t, a, "turn_state")); n != 1 {
		t.Fatalf("turn_state entries mid-turn = %d, want 1", n)
	}
	a.cancel = nil
	a.persistTail()
	entries := persistedEntries(t, a)
	last := entries[len(entries)-1]
	if last.Type != "turn_state" || last.Meta["state"] != "ended" {
		t.Fatalf("last entry = %+v, want the turn's end", last)
	}
	if entries[0].Type != "turn_state" || entries[0].Meta["state"] != "started" {
		t.Fatalf("first entry = %+v", entries[0])
	}
}

// An ask still open when its turn ends is closed in the record.
func TestTurnEndClosesOpenAsks(t *testing.T) {
	a, _ := newSyncApp(t)
	a.openTurn()
	_, askID := openPermissionAsk(t, a)
	a.cancel = nil
	a.closeTurn("interrupted")
	a.flushTurnEnd()
	answers := entriesOfType(t, a, "ask_answer")
	if len(answers) != 1 || answers[0].ID != answerEntryID(askID) || answers[0].Meta["closed"] != true {
		t.Fatalf("ask_answer = %+v", answers)
	}
}

// Every classifier verdict is recorded; those the feed hides are marked so a
// resumed session does not show them twice.
func TestDecisionsRecordedWithFacts(t *testing.T) {
	a := newPersistApp(t)
	a.Update(rmActivityMsg(rolemanager.Activity{Event: rolemanager.EventModeClassify, Verdict: "PLAN", Model: "or/jev", Duration: 40 * time.Millisecond}))
	a.Update(rmActivityMsg(rolemanager.Activity{Event: rolemanager.EventSecuritySentinel, Verdict: string(rolemanager.SentinelSafe), Subject: "Read"}))
	a.persistTail()
	rows := entriesOfType(t, a, "rolemanager")
	var hidden, shown *session.Entry
	for i := range rows {
		if rows[i].Meta["hidden"] == true {
			hidden = &rows[i]
		} else {
			shown = &rows[i]
		}
	}
	if hidden == nil || hidden.Meta["activity"] != string(rolemanager.EventModeClassify) || hidden.Meta["verdict"] != "PLAN" {
		t.Fatalf("hidden decision = %+v", hidden)
	}
	if shown == nil || shown.Meta["verdict"] != string(rolemanager.SentinelSafe) ||
		shown.Meta["verdict_label"] != rolemanager.SentinelSafe.Label() || shown.Meta["subject"] != "Read" {
		t.Fatalf("shown decision = %+v", shown)
	}
	if _, ok := shown.Meta["detail"]; ok {
		t.Fatal("decision detail must never be persisted")
	}
}

// Resume restores diffs, rebuilds ask notices, and ignores the records that
// exist only for the website.
func TestRehydrateWebRecords(t *testing.T) {
	ch := filediff.Preview("a.go", "x\n", "y\n")
	wire, _ := json.Marshal(ch.Wire(maxDiffWireBytes))
	var diff any
	_ = json.Unmarshal(wire, &diff)
	entries := []session.Entry{
		{Type: "turn_state", Meta: map[string]any{"state": "started"}},
		{Type: "user", Content: "go"},
		{Type: "tool_start", Meta: map[string]any{"tool_call_id": "c1", "tool_name": "Edit"}},
		{Type: "assistant", Meta: map[string]any{"tool_calls": []any{map[string]any{"id": "c1", "name": "Edit", "args": "{}"}}}},
		{Type: "tool", Content: "ok", Meta: map[string]any{"tool_call_id": "c1", "tool_name": "Edit", "diff": diff}},
		{Type: "ask", Content: "Clarification needed:\n1. Which?", Meta: map[string]any{"kind": askClarify}},
		{Type: "ask", Content: "permission: Edit a.go", Meta: map[string]any{"kind": askPermission}},
		{Type: "ask_answer", Content: "permission for Edit: allow once", Meta: map[string]any{"kind": askPermission}},
		{Type: "ask_answer", Meta: map[string]any{"closed": true}},
		{Type: "rolemanager", Content: "mode_classify", Meta: map[string]any{"hidden": true}},
		{Type: "turn_state", Meta: map[string]any{"state": "ended"}},
	}
	msgs, dropped := messagesFromEntries(entries)
	if dropped != 0 {
		t.Fatalf("dropped = %d", dropped)
	}
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
		if m.Role == "tool" && (m.Diff() == nil || len(m.Diff().Files) != 1 || len(m.Diff().Files[0].Rows()) == 0) {
			t.Fatalf("tool diff not restored: %+v", m.Diff())
		}
	}
	want := []string{"user", "assistant", "tool", "system", "system"}
	if !slices.Equal(roles, want) {
		t.Fatalf("roles = %v, want %v", roles, want)
	}
}

// The setting reaches the syncer and /sync status.
func TestRemoteAnswersSettingReachesStatus(t *testing.T) {
	a, _ := newSyncApp(t)
	if !strings.Contains(a.syncStatusText(), "web answers on") {
		t.Fatalf("status = %q", a.syncStatusText())
	}
	off := false
	s := config.Settings{Sync: &config.SyncSettings{RemoteAnswers: &off}}
	if s.SyncRemoteAnswersEnabled() {
		t.Fatal("remote_answers=false must be off")
	}
}
