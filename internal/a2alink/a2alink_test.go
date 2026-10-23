package a2alink

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/gitsync"
	"github.com/vulnetix/belai/internal/posture"
)

type rec struct {
	mu      sync.Mutex
	entries []string
	metas   []map[string]any
}

func (r *rec) record(typ, content string, meta map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, typ+": "+content)
	r.metas = append(r.metas, meta)
}

func (r *rec) wait(t *testing.T, typ string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		for i, e := range r.entries {
			if strings.HasPrefix(e, typ+":") {
				m := r.metas[i]
				r.mu.Unlock()
				return m
			}
		}
		r.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Fatalf("no %s entry in %v", typ, r.entries)
	return nil
}

func gitJSON() func() json.RawMessage {
	b, _ := json.Marshal(gitsync.Info{Root: "/r", Remote: "o/r", Branch: "main"})
	return func() json.RawMessage { return b }
}

func TestLinksBroadcastAndAnswerWithoutASession(t *testing.T) {
	dir := t.TempDir()
	const host = "11111111-1111-4111-8111-111111111111"
	off := false
	settings := config.Settings{Guardrails: &off}
	ra, rb := &rec{}, &rec{}
	open := func(sess string, r *rec) *Link {
		l := Open(context.Background(), Options{Dir: dir, HostID: host, SessionID: sess, Settings: settings,
			Posture: posture.AllIgnore(), Git: gitJSON(), Record: r.record, PollEvery: 20 * time.Millisecond, Debounce: time.Millisecond})
		if l == nil {
			t.Fatal("no link")
		}
		return l
	}
	b := open("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", rb)
	defer b.Close()
	a := open("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", ra)
	a.Node().Broadcaster().AddFiles("x.go")
	defer a.Close()

	in := rb.wait(t, EntryBroadcastIn)
	if in["peerSessionId"] != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" || in["repo"] != "o/r" {
		t.Fatalf("broadcast_in meta %v", in)
	}
	// The directive waits for the session (it is queued just after the
	// entry is recorded).
	var directive string
	for deadline := time.Now().Add(5 * time.Second); directive == "" && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		b.mu.Lock()
		if len(b.pending) > 0 {
			directive = b.pending[0].Directive
		}
		b.mu.Unlock()
	}
	if !strings.Contains(directive, "A2AAck{messageId:") {
		t.Fatalf("pending directive %q", directive)
	}
	ra.wait(t, EntryBroadcastOut)

	// A message to an agent with no session yet is answered by the harness.
	if _, _, err := b.Hub().A2AMessage(context.Background(), a.Node().Self().Display(), "Are you editing x.go?"); err != nil {
		t.Fatal(err)
	}
	rb.wait(t, EntryMessageOut)
	ra.wait(t, EntryMessageIn)
	ra.wait(t, EntryReplyOut)
	rep := rb.wait(t, EntryReplyIn)
	if rep["referenceMessageId"] == nil {
		t.Fatalf("reply not tied to the question: %v", rep)
	}
	if ok, err := b.Hub().A2AAck(context.Background(), in["messageId"].(string), "ignore", "no overlap"); ok || err != nil {
		t.Fatalf("ack %v %v", ok, err)
	}
	rb.wait(t, EntryAck)
}

func TestOpenOffAndNil(t *testing.T) {
	off := false
	if Open(context.Background(), Options{SessionID: "x", Settings: config.Settings{A2A: &config.A2ASettings{Enabled: &off}}}) != nil {
		t.Fatal("a2a.enabled off opened a link")
	}
	var l *Link
	l.Close()
	l.SetTicket("x")
	l.SetRecorder(nil)
	if l.Hub() != nil || l.Install(nil) != nil {
		t.Fatal("a nil link did something")
	}
}
