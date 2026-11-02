package sessionsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// docs/session-sync.md, "The host is the source of truth": NULs are removed
// before upload, and one line the server cannot store does not wedge the
// session.

func TestParseLineRemovesNUL(t *testing.T) {
	raw := `{"id":"a\u0000","type":"tool","role":"to\u0000ol","content":"ELF\u0000\u0001bin",` +
		`"meta":{"stdout":"x\u0000y","k\u0000":[1,"\u0000"],"big":12345678901234567890,"h":"<a&b>"},"subagent_id":"s\u0000"}` + "\n"
	e := parseLine([]byte(raw), 4)
	if e.ID != "a" || e.Role != "tool" || e.Content != "ELF\x01bin" || e.SubagentID != "s" || e.Seq != 4 {
		t.Fatalf("entry = %+v", e)
	}
	if strings.Contains(string(e.Meta), `\u0000`) || !json.Valid(e.Meta) {
		t.Fatalf("meta = %s", e.Meta)
	}
	var m map[string]any
	_ = json.Unmarshal(e.Meta, &m)
	if m["stdout"] != "xy" || m["h"] != "<a&b>" || m["k"] == nil || !strings.Contains(string(e.Meta), "12345678901234567890") {
		t.Fatalf("meta = %s", e.Meta)
	}
}

func TestParseLineLeavesCleanMetaByteForByte(t *testing.T) {
	raw := `{"id":"a","type":"user","content":"hi","meta":{"z":"<b>","a":1.50}}` + "\n"
	if e := parseLine([]byte(raw), 0); string(e.Meta) != `{"z":"<b>","a":1.50}` {
		t.Fatalf("meta = %s", e.Meta)
	}
}

func TestParseLineRefusesATypeTheServerWouldRefuse(t *testing.T) {
	for _, raw := range []string{
		`{"id":"a","type":""}`,
		`{"id":"a","type":"\u0000"}`,
		`{"id":"a","type":"` + strings.Repeat("t", 65) + `"}`,
	} {
		if e := parseLine([]byte(raw+"\n"), 2); e.Type != "invalid" || e.ID != "invalid-2" {
			t.Errorf("%s -> %+v", raw, e)
		}
	}
}

func poisonSyncer(t *testing.T, fake *fakeServer) (*Syncer, *tail) {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL+apiPath, func() (string, error) { return "ApiKey org:hex", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Client: c, HostID: testHost}), &tail{info: SessionInfo{ID: testSess}, registered: true, serverLast: -1}
}

func threeLines() []Entry {
	return []Entry{
		{Seq: 0, ID: "a", Type: "user", Content: "one"},
		{Seq: 1, ID: "b", Type: "tool", Content: "poison"},
		{Seq: 2, ID: "c", Type: "assistant", Content: "three"},
	}
}

// A line the server can never store is replaced after the third rejection,
// and the lines around it are stored.
func TestPoisonLineIsReplacedAfterRepeatedRejection(t *testing.T) {
	fake := newFake()
	fake.poison = "poison"
	s, tl := poisonSyncer(t, fake)
	ctx := context.Background()

	for i := 1; i < rejectsBeforeSplit; i++ {
		if _, err := s.postBatch(ctx, tl, threeLines()); !rejectedBatch(err) {
			t.Fatalf("attempt %d: err = %v, want a rejection", i, err)
		}
	}
	if fake.count(testSess) != 0 {
		t.Fatal("lines were stored before the batch was split")
	}
	last, err := s.postBatch(ctx, tl, threeLines())
	if err != nil || last != 2 {
		t.Fatalf("split upload: last=%d err=%v", last, err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	got := fake.entries[testSess]
	if len(got) != 3 || got[0].Content != "one" || got[2].Content != "three" {
		t.Fatalf("stored %+v", got)
	}
	if got[1].ID != "unsynced-1" || got[1].Type != "system" || !strings.Contains(got[1].Content, "not synced") {
		t.Fatalf("seq 1 = %+v, want the placeholder", got[1])
	}
	if tl.rejects != 0 {
		t.Fatalf("rejects = %d after the split", tl.rejects)
	}
	if !strings.Contains(s.Status().LastError, "line 1 not synced") {
		t.Fatalf("status error = %q", s.Status().LastError)
	}
}

// A server that rejects everything is down, not handed a bad line: nothing is
// replaced, and the batch goes up whole once it recovers.
func TestServerOutageReplacesNothing(t *testing.T) {
	fake := newFake()
	fake.failPost = 1 << 20
	s, tl := poisonSyncer(t, fake)
	ctx := context.Background()

	for i := 0; i < rejectsBeforeSplit+2; i++ {
		if _, err := s.postBatch(ctx, tl, threeLines()[:1]); err == nil {
			t.Fatalf("attempt %d succeeded during the outage", i)
		}
	}
	if fake.count(testSess) != 0 {
		t.Fatal("a placeholder was stored during an outage")
	}
	fake.mu.Lock()
	fake.failPost = 0
	fake.mu.Unlock()
	if last, err := s.postBatch(ctx, tl, threeLines()[:1]); err != nil || last != 0 {
		t.Fatalf("after recovery: last=%d err=%v", last, err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.entries[testSess][0].Content != "one" {
		t.Fatalf("stored %+v", fake.entries[testSess][0])
	}
}

// Rejections count per batch: a new batch (another offset) starts again.
func TestRejectionsCountPerBatch(t *testing.T) {
	fake := newFake()
	fake.poison = "poison"
	s, tl := poisonSyncer(t, fake)
	ctx := context.Background()
	for i := 1; i < rejectsBeforeSplit; i++ {
		_, _ = s.postBatch(ctx, tl, threeLines())
	}
	tl.offset = 99
	if _, err := s.postBatch(ctx, tl, threeLines()); !rejectedBatch(err) || tl.rejects != 1 {
		t.Fatalf("err=%v rejects=%d, want a fresh count", err, tl.rejects)
	}
}

// Only an answered upload the server could not take counts as a rejection.
func TestRejectedBatch(t *testing.T) {
	for status, want := range map[int]bool{500: true, 502: true, 503: true, 400: true, 413: true,
		429: false, 403: false} {
		if got := rejectedBatch(&StatusError{Method: "POST", Path: "/x", Status: status}); got != want {
			t.Errorf("HTTP %d: rejected = %v, want %v", status, got, want)
		}
	}
	for _, err := range []error{nil, ErrNotFound, ErrUnauthorized, ErrConflict, errors.New("dial tcp: refused")} {
		if rejectedBatch(err) {
			t.Errorf("%v counted as a rejection", err)
		}
	}
	if e := (&StatusError{Method: http.MethodPost, Path: "/p", Status: 500}); e.Error() != "sessionsync: POST /p: HTTP 500" {
		t.Errorf("Error() = %q", e.Error())
	}
}

func TestSessionSyncDocNamesTheNULAndPoisonRules(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/session-sync.md")), " ")
	for _, want := range []string{
		"U+0000", "`\\u0000`", "The file on disk is never changed",
		"three times in a row", "one entry at a time", "`unsynced-<seq>`",
		"[line not synced: the server could not store it]", "5xx, 400 or 413",
		"the placeholder itself is accepted",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/session-sync.md no longer says %q", want)
		}
	}
	if rejectsBeforeSplit != 3 {
		t.Errorf("rejectsBeforeSplit = %d; the page says three", rejectsBeforeSplit)
	}
}
