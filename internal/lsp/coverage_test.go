package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- test Conn implementations -------------------------------------------

// readerConn serves a fixed byte stream to the transport's read side.
type readerConn struct {
	r       *bytes.Reader
	closed  bool
	waitErr error
}

func (c *readerConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *readerConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *readerConn) Close() error                { c.closed = true; return nil }
func (c *readerConn) Wait() error                 { return c.waitErr }

// recordConn captures writes so framing can be asserted.
type recordConn struct {
	buf     bytes.Buffer
	closed  bool
	waitErr error
}

func (c *recordConn) Read(p []byte) (int, error)  { return 0, io.EOF }
func (c *recordConn) Write(p []byte) (int, error) { return c.buf.Write(p) }
func (c *recordConn) Close() error                { c.closed = true; return nil }
func (c *recordConn) Wait() error                 { return c.waitErr }

func frame(id int, result any) []byte {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	return append([]byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))), body...)
}

// --- Detect / DetectedLanguages -------------------------------------------

type fakeProbe struct{ m map[string]string }

func (f fakeProbe) Lookup(_ context.Context, binary string) (string, bool) {
	p, ok := f.m[binary]
	return p, ok
}

func TestDetect(t *testing.T) {
	ctx := context.Background()

	// Only the server candidate is present.
	if got, ok := Detect(ctx, fakeProbe{map[string]string{"srv": "/a/srv"}}, "srv", []string{"alt"}); !ok || got != "/a/srv" {
		t.Fatalf("Detect(server hit) = (%q, %v), want (/a/srv, true)", got, ok)
	}
	// Server missing: first alternative present.
	if got, ok := Detect(ctx, fakeProbe{map[string]string{"alt1": "/a/alt1"}}, "srv", []string{"alt1", "alt2"}); !ok || got != "/a/alt1" {
		t.Fatalf("Detect(alt hit) = (%q, %v), want (/a/alt1, true)", got, ok)
	}
	// Server and alternative both present: at least one resolved path is returned.
	if got, ok := Detect(ctx, fakeProbe{map[string]string{"srv": "/a/srv", "alt": "/a/alt"}}, "srv", []string{"alt"}); !ok || (got != "/a/srv" && got != "/a/alt") {
		t.Fatalf("Detect(both) = (%q, %v), want one of the detected paths", got, ok)
	}
	// Nothing present.
	if got, ok := Detect(ctx, fakeProbe{map[string]string{}}, "srv", []string{"alt"}); ok || got != "" {
		t.Fatalf("Detect(miss) = (%q, %v), want (\"\", false)", got, ok)
	}
	// Empty candidate set.
	if got, ok := Detect(ctx, fakeProbe{}, "", nil); ok || got != "" {
		t.Fatalf("Detect(empty) = (%q, %v), want (\"\", false)", got, ok)
	}
	// nil probe falls back to a real PATH lookup of a non-existent binary.
	if got, ok := Detect(ctx, nil, "definitely-not-a-real-belai-binary", nil); ok || got != "" {
		t.Fatalf("Detect(nil probe) = (%q, %v), want (\"\", false)", got, ok)
	}
}

func TestDetectedLanguages(t *testing.T) {
	got := DetectedLanguages(map[string]string{"c": "/a", "go": "/b", "bash": "/c"})
	want := []string{"bash", "c", "go"}
	if len(got) != len(want) {
		t.Fatalf("DetectedLanguages = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DetectedLanguages = %v, want %v", got, want)
		}
	}
}

// --- path / manifest helpers ----------------------------------------------

func TestRootFor(t *testing.T) {
	cases := []struct {
		abs   string
		roots []string
		want  string
	}{
		{"/repo/src/a.go", []string{"/repo", "/other"}, "/repo"},
		{"/other/x/y.go", []string{"/repo", "/other"}, "/other"},
		{"/elsewhere/a.go", []string{"/repo"}, "/repo"}, // fall back to first root
		{"/tmp/a.go", nil, filepath.Dir("/tmp/a.go")},   // no roots: dir of file
		{"/repo/deep/a.go", []string{"/repo"}, "/repo"}, // prefix, not just substring
		{"/repo2/a.go", []string{"/repo"}, "/repo"},     // sibling dir must not match
	}
	for _, tc := range cases {
		if got := rootFor(tc.abs, tc.roots); got != tc.want {
			t.Errorf("rootFor(%q, %v) = %q, want %q", tc.abs, tc.roots, got, tc.want)
		}
	}
}

func TestIsManifest(t *testing.T) {
	for _, name := range []string{"go.mod", "go.sum", "package.json", "tsconfig.json", "Cargo.toml", "pyproject.toml"} {
		if !isManifest(filepath.Join("/repo", name)) {
			t.Errorf("isManifest(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"main.go", "a.txt", "go.mod.bak"} {
		if isManifest(filepath.Join("/repo", name)) {
			t.Errorf("isManifest(%q) = true, want false", name)
		}
	}
}

func TestPathURI(t *testing.T) {
	if got := pathToURI("a/b.go"); got != "file://a/b.go" {
		t.Fatalf("pathToURI = %q", got)
	}
	if got, err := uriToPath("file:///tmp/a.go"); err != nil || got != "/tmp/a.go" {
		t.Fatalf("uriToPath = (%q, %v), want (/tmp/a.go, nil)", got, err)
	}
	if _, err := uriToPath("http://x/y"); err == nil {
		t.Fatal("uriToPath(http) should fail")
	}
	if _, err := uriToPath("://bad"); err == nil {
		t.Fatal("uriToPath(bad) should fail")
	}
}

func TestCacheDirNonEmpty(t *testing.T) {
	if got := cacheDir(); got == "" {
		t.Fatal("cacheDir() returned empty")
	}
}

func TestDefaultNow(t *testing.T) {
	fixed := time.Unix(1, 0)
	if got := defaultNow(func() time.Time { return fixed })(); !got.Equal(fixed) {
		t.Fatalf("defaultNow(fn) = %v, want %v", got, fixed)
	}
	if got := defaultNow(nil)(); got.IsZero() {
		t.Fatal("defaultNow(nil) returned zero time")
	}
}

// --- severity / client helpers --------------------------------------------

func TestSeverityStringUnknown(t *testing.T) {
	if got := Severity(99).String(); got != "unknown" {
		t.Fatalf("Severity(99).String() = %q, want unknown", got)
	}
}

func TestSeverityFromAllValues(t *testing.T) {
	cases := map[int]Severity{1: SeverityError, 2: SeverityWarning, 3: SeverityInfo, 4: SeverityHint, 0: SeverityError, 99: SeverityError}
	for in, want := range cases {
		if got := severityFrom(intPtr(in)); got != want {
			t.Errorf("severityFrom(%d) = %v, want %v", in, got, want)
		}
	}
	if got := severityFrom(nil); got != SeverityError {
		t.Errorf("severityFrom(nil) = %v, want SeverityError", got)
	}
}

func TestClientHandleRequest(t *testing.T) {
	c := &client{roots: []string{"/repo"}}

	got, err := c.handleRequest("workspace/applyEdit", nil)
	if err != nil || got != (ApplyEditResult{Applied: false}) {
		t.Fatalf("applyEdit = (%+v, %v)", got, err)
	}

	got, err = c.handleRequest("workspace/workspaceFolders", nil)
	if err != nil {
		t.Fatalf("workspaceFolders: %v", err)
	}
	folders, ok := got.([]WorkspaceFolder)
	if !ok || len(folders) != 1 || folders[0].URI != "file:///repo" {
		t.Fatalf("workspaceFolders = %+v", got)
	}

	got, err = c.handleRequest("workspace/configuration", json.RawMessage(`{"items":[{"section":"x"},{"section":"y"}]}`))
	if err != nil {
		t.Fatalf("configuration: %v", err)
	}
	if arr, ok := got.([]any); !ok || len(arr) != 2 {
		t.Fatalf("configuration = %+v", got)
	}

	for _, m := range []string{"client/registerCapability", "client/unregisterCapability", "window/workDoneProgress/create", "window/showMessageRequest"} {
		if _, err := c.handleRequest(m, nil); err != nil {
			t.Errorf("%s: %v", m, err)
		}
	}

	if _, err := c.handleRequest("unknown/method", nil); err == nil {
		t.Fatal("unknown method should error")
	}
}

func TestClientHandleNotification(t *testing.T) {
	c := &client{}
	c.handleNotification("textDocument/publishDiagnostics", json.RawMessage(`{"uri":"file:///a.go","diagnostics":[{"message":"m"}],"version":3}`))
	if c.publishURI != "file:///a.go" || c.publishVersion != 3 || len(c.diagnostics) != 1 {
		t.Fatalf("publishDiagnostics state = %+v", c)
	}

	c.handleNotification("$/progress", json.RawMessage(`{"token":"t","value":{"kind":"end"}}`))
	if !c.isReady() {
		t.Fatal("progress end should set ready")
	}
	c.setReady(false)
	if c.isReady() {
		t.Fatal("setReady(false) should clear ready")
	}

	// Swallowed notifications must not panic or mutate state.
	c.handleNotification("window/logMessage", json.RawMessage(`{"message":"x"}`))
}

// --- report cache ---------------------------------------------------------

func TestReportCacheGetSetExpiry(t *testing.T) {
	now := time.Unix(100, 0)
	c := &reportCache{ttl: time.Second, now: func() time.Time { return now }, items: map[string]cacheItem{}}
	r := Report{Status: StatusReady, Rows: []Row{{Line: 1}}}

	if got := c.get("a.go", []byte("x")); got != nil {
		t.Fatalf("empty cache hit = %+v", got)
	}
	c.set("a.go", []byte("x"), r)
	if got := c.get("a.go", []byte("x")); got == nil || got.Status != StatusReady {
		t.Fatalf("cache miss after set = %+v", got)
	}
	// Different content hash misses.
	if got := c.get("a.go", []byte("y")); got != nil {
		t.Fatalf("different content should miss, got %+v", got)
	}
	// Expired entry is evicted.
	now = now.Add(2 * time.Second)
	if got := c.get("a.go", []byte("x")); got != nil {
		t.Fatalf("expired entry should miss, got %+v", got)
	}
}

func TestFileHashDeterministic(t *testing.T) {
	a := fileHash("/x/a.go", []byte("body"))
	b := fileHash("/x/a.go", []byte("body"))
	c := fileHash("/x/a.go", []byte("other"))
	if a != b {
		t.Fatal("fileHash not deterministic")
	}
	if a == c {
		t.Fatal("fileHash should vary with content")
	}
	if a == "" {
		t.Fatal("fileHash empty")
	}
}

// --- transport parsing ------------------------------------------------------

func newReaderTransport(data []byte) *transport {
	return &transport{
		conn:    &readerConn{r: bytes.NewReader(data)},
		reader:  bufio.NewReader(bytes.NewReader(data)),
		pending: map[int]*responseSlot{},
		done:    make(chan struct{}),
	}
}

func TestReadOneValid(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":7,"result":{"capabilities":{}}}`
	data := []byte(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body))
	tp := newReaderTransport(data)
	msg, err := tp.readOne()
	if err != nil {
		t.Fatalf("readOne: %v", err)
	}
	if string(msg.ID) != "7" || msg.Result == nil {
		t.Fatalf("readOne = %+v", msg)
	}
}

func TestReadOneRejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{"bad header", "no-colon-here\r\n\r\n"},
		{"non-numeric length", "Content-Length: abc\r\n\r\n"},
		{"negative length", "Content-Length: -1\r\n\r\n"},
		{"oversized length", fmt.Sprintf("Content-Length: %d\r\n\r\n", reqCap+1)},
		{"missing length", "X-Other: 1\r\n\r\nbody"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tp := newReaderTransport([]byte(tc.data))
			if _, err := tp.readOne(); err == nil {
				t.Fatalf("readOne(%q) should fail", tc.name)
			}
		})
	}
}

func TestReadOneRejectsBadJSON(t *testing.T) {
	body := "{not json"
	data := []byte(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body))
	tp := newReaderTransport(data)
	if _, err := tp.readOne(); err == nil {
		t.Fatal("readOne should reject bad JSON body")
	}
}

func TestNotifyJSONAndWriteFraming(t *testing.T) {
	conn := &recordConn{}
	tp := newTransport(conn)
	if err := tp.notifyJSON("initialized", map[string]any{}); err != nil {
		t.Fatalf("notifyJSON: %v", err)
	}
	out := conn.buf.String()
	if !strings.HasPrefix(out, "Content-Length: ") {
		t.Fatalf("missing framing: %q", out)
	}
	if !strings.Contains(out, `"method":"initialized"`) {
		t.Fatalf("missing method: %q", out)
	}
}

func TestCallClosedTransport(t *testing.T) {
	conn := &recordConn{}
	tp := newTransport(conn)
	tp.mu.Lock()
	tp.closed = true
	tp.mu.Unlock()
	if _, err := tp.call(context.Background(), "x", nil); !errors.Is(err, errTransportClosed) {
		t.Fatalf("call on closed transport = %v, want errTransportClosed", err)
	}
}

func TestHandleResponseAndDrain(t *testing.T) {
	tp := newTransport(&recordConn{})
	slot := &responseSlot{ch: make(chan rawMsg, 1)}
	tp.pending[5] = slot

	tp.handleResponse(rawMsg{ID: json.RawMessage("5"), Result: json.RawMessage(`"ok"`)})
	select {
	case resp := <-slot.ch:
		if string(resp.Result) != `"ok"` {
			t.Fatalf("response = %+v", resp)
		}
	default:
		t.Fatal("response not delivered")
	}

	// Unknown id is dropped.
	tp.handleResponse(rawMsg{ID: json.RawMessage("99")})

	// drain closes all pending channels.
	slot2 := &responseSlot{ch: make(chan rawMsg, 1)}
	tp.pending[6] = slot2
	tp.drainAllLocked(errTransportClosed)
	if _, ok := <-slot2.ch; ok {
		t.Fatal("drained slot channel should be closed")
	}
}

func TestHandleRequestWritesResponse(t *testing.T) {
	conn := &recordConn{}
	tp := newTransport(conn)
	tp.callback = func(method string, params json.RawMessage) (any, error) {
		if method == "fail" {
			return nil, errors.New("nope")
		}
		return map[string]any{"a": 1}, nil
	}
	tp.handleRequest(rawMsg{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "ok"})
	if got := conn.buf.String(); !strings.Contains(got, `"result":{"a":1}`) {
		t.Fatalf("response missing result: %q", got)
	}
	tp.handleRequest(rawMsg{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "fail"})
	if got := conn.buf.String(); !strings.Contains(got, `"code":-32601`) {
		t.Fatalf("error response missing code: %q", got)
	}
}

func TestTransportCloseIdempotent(t *testing.T) {
	conn := &recordConn{}
	tp := newTransport(conn)
	if tp.isClosed() {
		t.Fatal("fresh transport should be open")
	}
	if err := tp.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !tp.isClosed() {
		t.Fatal("transport should be closed")
	}
	if !conn.closed {
		t.Fatal("conn should be closed")
	}
	if err := tp.close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

// --- manager internals ------------------------------------------------------

func TestAdjustStrike(t *testing.T) {
	m := &Manager{}
	e := &entry{budget: time.Second}

	e.strikes = 0
	m.adjustStrike(e)
	if e.budget != time.Second {
		t.Fatalf("no strike should keep budget, got %v", e.budget)
	}

	e.strikes = strikeBudgetHalve
	e.budget = time.Second
	m.adjustStrike(e)
	if e.budget != 500*time.Millisecond {
		t.Fatalf("halve strike budget = %v", e.budget)
	}

	e.strikes = strikeDisable
	e.budget = time.Second
	m.adjustStrike(e)
	if e.budget != 0 {
		t.Fatalf("disable strike budget = %v", e.budget)
	}

	// Halving clamps at 100ms.
	e.strikes = strikeBudgetHalve
	e.budget = 150 * time.Millisecond
	m.adjustStrike(e)
	if e.budget != 100*time.Millisecond {
		t.Fatalf("clamped budget = %v", e.budget)
	}
}
