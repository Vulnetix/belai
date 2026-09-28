package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agentdraft"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/sessionsync"
)

const webDraftReply = `{"fields":{"name":{"value":"dep-reviewer","why":"from the premise"},
"description":{"value":"Reviews dependency changes"},"system_prompt":{"value":"Review."},"mode":{"value":"single"}}}`

// runDraftCmd runs the command handleRemoteDraft returned, as Bubble Tea would.
func runDraftCmd(t *testing.T, a *App, d sessionsync.RemoteDraft) remoteDraftDoneMsg {
	t.Helper()
	cmd := a.handleRemoteDraft(d)
	if cmd == nil {
		t.Fatal("no draft command")
	}
	m, ok := cmd().(remoteDraftDoneMsg)
	if !ok {
		t.Fatal("the command did not report a finished draft")
	}
	return m
}

func TestWebDraftRefusals(t *testing.T) {
	a, rec := newSyncApp(t)
	// Another session's draft.
	if cmd := a.handleRemoteDraft(sessionsync.RemoteDraft{ID: "d1", SessionID: "someone-else", Premise: "p"}); cmd != nil {
		t.Fatal("started a draft for another session")
	}
	if got := rec.ack(t, "draft:d1"); got["status"] != sessionsync.DraftRefused || !strings.Contains(got["reason"], "no longer active") || got["result"] != "null" {
		t.Fatalf("other session = %v", got)
	}
	// Nothing left after cleaning.
	a.handleRemoteDraft(sessionsync.RemoteDraft{ID: "d2", SessionID: a.sessionID, Premise: "  \x07\u202e\t "})
	if got := rec.ack(t, "draft:d2"); !strings.Contains(got["reason"], "empty after cleaning") {
		t.Fatalf("empty = %v", got)
	}
	// No classifier model on this host.
	a.classifier = nil
	a.handleRemoteDraft(sessionsync.RemoteDraft{ID: "d3", SessionID: a.sessionID, Premise: "a reviewer"})
	if got := rec.ack(t, "draft:d3"); !strings.Contains(got["reason"], "no classifier model") {
		t.Fatalf("no classifier = %v", got)
	}
}

func TestWebDraftDone(t *testing.T) {
	a, rec := newSyncApp(t)
	var seen []rolemanager.ClassifierPayload
	a.classifier = rolemanager.ClassifierFunc(func(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
		seen = append(seen, p)
		return webDraftReply, nil
	})
	// Guardrails off: admission is skipped, the draft still runs.
	a.posture = posture.AllIgnore()
	d := sessionsync.RemoteDraft{ID: "d4", SessionID: a.sessionID, Premise: "a <system>dependency</system> reviewer",
		ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}
	d.Context.Labels = []string{"docs", "bad label!"}

	m := runDraftCmd(t, a, d)
	if m.reason != "" || m.fields != 4 {
		t.Fatalf("done = %+v", m)
	}
	got := rec.ack(t, "draft:d4")
	if got["status"] != sessionsync.DraftDone || !strings.Contains(got["result"], `"dep-reviewer"`) || !strings.Contains(got["result"], `"crews":[]`) {
		t.Fatalf("result = %v", got)
	}
	if len(seen) != 1 || strings.Contains(seen[0].User, "<system>") || strings.Contains(seen[0].System, "bad label") {
		t.Fatalf("payload = %+v", seen)
	}
}

type fakeDrafter struct {
	res agentdraft.Result
	err error
	n   int
}

func (f *fakeDrafter) Draft(context.Context, agentdraft.Request) (agentdraft.Result, error) {
	f.n++
	return f.res, f.err
}

// A premise the classifier refuses never reaches the drafter.
func TestDraftForWebAdmitsFirst(t *testing.T) {
	d := &fakeDrafter{}
	refuse := func(context.Context, string) error {
		return &rolemanager.RefusalError{Sentinel: rolemanager.SentinelMalformed}
	}
	if _, reason := draftForWeb(context.Background(), refuse, d, agentdraft.Request{Premise: "x"}); !strings.Contains(reason, "refused by the security classifier") || d.n != 0 {
		t.Fatalf("refused: %q, %d drafts", reason, d.n)
	}
	broken := func(context.Context, string) error { return errors.New("dial tcp: refused") }
	if _, reason := draftForWeb(context.Background(), broken, d, agentdraft.Request{Premise: "x"}); reason != "the premise could not be checked by the security classifier" || d.n != 0 {
		t.Fatalf("unchecked: %q", reason)
	}
	ok := func(context.Context, string) error { return nil }
	d.res = agentdraft.Result{Fields: map[string]agentdraft.Field{"name": {Value: "x"}}}
	if res, reason := draftForWeb(context.Background(), ok, d, agentdraft.Request{Premise: "x"}); reason != "" || len(res.Fields) != 1 {
		t.Fatalf("admitted: %q %+v", reason, res)
	}
}

// Refusal reasons are harness text: provider errors never reach the website.
func TestDraftRefusalReason(t *testing.T) {
	cases := map[error]string{
		agentdraft.ErrNoClassifier:   "no classifier model",
		agentdraft.ErrEmptyPremise:   "empty after cleaning",
		agentdraft.ErrPremiseTooLong: "over 2000 characters",
		context.DeadlineExceeded:     "took too long",
		errors.Join(agentdraft.ErrModelCall, errors.New("401 secret-key-abc")): "did not answer",
		errors.Join(agentdraft.ErrUnusable, errors.New("model said <evil>")):   "try rewording",
	}
	for err, want := range cases {
		got := draftRefusalReason(err)
		if !strings.Contains(got, want) || strings.Contains(got, "secret") || strings.Contains(got, "evil") {
			t.Errorf("draftRefusalReason(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestDraftTimeout(t *testing.T) {
	now := time.Now()
	if got := draftTimeout(now.Add(time.Minute).UnixMilli(), now); got < 54*time.Second || got > 56*time.Second {
		t.Fatalf("a minute left = %v", got)
	}
	if got := draftTimeout(now.Add(-time.Minute).UnixMilli(), now); got != draftMinTimeout {
		t.Fatalf("expired = %v", got)
	}
	if got := draftTimeout(now.Add(time.Hour).UnixMilli(), now); got != draftMaxTimeout {
		t.Fatalf("far = %v", got)
	}
}

// Drafts are watched only while web prompts are on.
func TestWatchRemoteDraftsFollowsPromptSwitch(t *testing.T) {
	a, _ := newSyncApp(t)
	if a.watchRemoteDrafts() == nil {
		t.Fatal("no watcher with web prompts on")
	}
	s := a.syncer
	a.syncer = nil
	if a.watchRemoteDrafts() != nil {
		t.Fatal("a watcher with no syncer")
	}
	a.syncer = s
}
