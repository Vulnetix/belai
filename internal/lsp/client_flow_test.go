package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptedConn serves one pre-scripted response frame to the transport's read
// side and records writes, so a test can drive the transport's read loop and
// assert what was written.
//
// The response is released only after the transport writes a *request* (a
// message carrying both a method and an id). An eager reader let the async
// read loop consume the scripted response — and then hit EOF — before call()
// registered its pending slot, which made these tests intermittently fail with
// "transport closed". Gating the response on the request write removes that
// scheduling race entirely.
type scriptedConn struct {
	mu     sync.Mutex
	cond   *sync.Cond
	script []byte
	pos    int
	ready  bool
	buf    bytes.Buffer
	closed bool
}

func newScriptedConn(script []byte) *scriptedConn {
	c := &scriptedConn{script: script}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (c *scriptedConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for !c.ready && !c.closed {
		c.cond.Wait()
	}
	if c.pos >= len(c.script) {
		if c.closed {
			return 0, io.EOF
		}
		// No more scripted bytes, but not closed: block rather than EOF so
		// the read loop cannot tear the transport down mid-test.
		for !c.closed {
			c.cond.Wait()
		}
		return 0, io.EOF
	}
	n := copy(p, c.script[c.pos:])
	c.pos += n
	return n, nil
}

func (c *scriptedConn) Write(p []byte) (int, error) {
	n, err := c.buf.Write(p)
	c.mu.Lock()
	if !c.ready && isRequest(p) {
		c.ready = true
		c.cond.Broadcast()
	}
	c.mu.Unlock()
	return n, err
}

func (c *scriptedConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.cond.Broadcast()
	c.mu.Unlock()
	return nil
}

func (c *scriptedConn) Wait() error { return nil }

// isRequest reports whether a written JSON-RPC message is a request (has both
// an id and a method) rather than a notification or response.
func isRequest(p []byte) bool {
	var m rawMsg
	if err := json.Unmarshal(p, &m); err != nil {
		return false
	}
	return len(m.ID) > 0 && m.Method != ""
}

func frameError(id, code int, message string) []byte {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
	return append([]byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))), body...)
}

// newScriptedClient wires a client to a scripted conn that advertises a
// diagnostic provider (pull mode) and starts the read loop.
func newScriptedClient(script []byte) (*client, *scriptedConn) {
	conn := newScriptedConn(script)
	c := &client{
		lang:  &Language{ID: "go", Display: "Go"},
		roots: []string{"/repo"},
		caps:  ServerCapabilities{DiagnosticProvider: &DiagnosticOptions{}},
	}
	c.t = newTransport(conn)
	c.t.start(c.handleRequest, c.handleNotification)
	return c, conn
}

func fullReport(resultID string, items any) []byte {
	return frame(0, map[string]any{"kind": "full", "resultId": resultID, "items": items})
}

func TestPullFullReport(t *testing.T) {
	c, conn := newScriptedClient(fullReport("r1", []map[string]any{{
		"range":    map[string]any{"start": map[string]any{"line": 1, "character": 0}, "end": map[string]any{"line": 1, "character": 4}},
		"severity": 1,
		"message":  "undefined: foo",
	}}))
	report, err := c.pull(context.Background(), "file:///a.go", 1, "package main")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if report.Status != StatusReady || report.Language != "Go" {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Rows) != 1 || report.Rows[0].Line != 2 || report.Rows[0].Col != 1 || report.Rows[0].Severity != SeverityError {
		t.Fatalf("rows = %+v", report.Rows)
	}
	if c.resultID != "r1" {
		t.Fatalf("resultID = %q, want r1", c.resultID)
	}
	if got := conn.buf.String(); !strings.Contains(got, `"method":"textDocument/diagnostic"`) {
		t.Fatalf("diagnostic request not written: %q", got)
	}
}

func TestPullUnchangedUsesCachedDiagnostics(t *testing.T) {
	c, _ := newScriptedClient(frame(0, map[string]any{"kind": "unchanged", "resultId": "r2"}))
	c.diagnostics = []Diagnostic{{Range: Range{Start: Position{Line: 0, Character: 0}}, Severity: intPtr(2), Message: "cached"}}
	report, err := c.pull(context.Background(), "file:///a.go", 2, "x")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(report.Rows) != 1 || report.Rows[0].Severity != SeverityWarning || report.Rows[0].Message != "cached" {
		t.Fatalf("rows = %+v", report.Rows)
	}
}

func TestPullUnknownKind(t *testing.T) {
	c, _ := newScriptedClient(frame(0, map[string]any{"kind": "weird"}))
	if _, err := c.pull(context.Background(), "file:///a.go", 1, "x"); err == nil {
		t.Fatal("expected error for unknown report kind")
	}
}

func TestPullRPCError(t *testing.T) {
	c, _ := newScriptedClient(frameError(0, -32601, "boom"))
	_, err := c.pull(context.Background(), "file:///a.go", 1, "x")
	if err == nil || !strings.Contains(err.Error(), "-32601") {
		t.Fatalf("err = %v, want jsonrpc -32601", err)
	}
}

func TestWaitPushMatches(t *testing.T) {
	c := &client{lang: &Language{ID: "go", Display: "Go"}}
	c.publishURI = "file:///a.go"
	c.publishVersion = 2
	c.diagnostics = []Diagnostic{{Message: "m", Severity: intPtr(1)}}
	report, err := c.waitPush(context.Background(), "file:///a.go", 2)
	if err != nil {
		t.Fatalf("waitPush: %v", err)
	}
	if len(report.Rows) != 1 || report.Rows[0].Message != "m" {
		t.Fatalf("rows = %+v", report.Rows)
	}
}

func TestWaitPushContextDeadline(t *testing.T) {
	c := &client{lang: &Language{ID: "go", Display: "Go"}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.waitPush(ctx, "file:///a.go", 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestDiagnosePullPathWritesDidOpen(t *testing.T) {
	c, conn := newScriptedClient(fullReport("r1", []map[string]any{}))
	report, err := c.diagnose(context.Background(), "/repo/a.go", []byte("package main"))
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if report.Status != StatusReady {
		t.Fatalf("status = %q", report.Status)
	}
	w := conn.buf.String()
	for _, want := range []string{`"method":"textDocument/didOpen"`, `"method":"textDocument/didChange"`, `"method":"textDocument/diagnostic"`} {
		if !strings.Contains(w, want) {
			t.Fatalf("missing %s in writes: %q", want, w)
		}
	}
}

func TestDiagnosePushPath(t *testing.T) {
	conn := newScriptedConn(nil)
	t.Cleanup(func() { _ = conn.Close() })
	c := &client{lang: &Language{ID: "go", Display: "Go"}, roots: []string{"/repo"}}
	c.t = newTransport(conn)
	c.t.start(c.handleRequest, c.handleNotification)
	c.publishURI = "file:///repo/a.go"
	c.publishVersion = 1
	c.diagnostics = []Diagnostic{{Message: "m", Severity: intPtr(1)}}
	report, err := c.diagnose(context.Background(), "/repo/a.go", []byte("package main"))
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if len(report.Rows) != 1 || report.Rows[0].Message != "m" {
		t.Fatalf("rows = %+v", report.Rows)
	}
	if got := conn.buf.String(); !strings.Contains(got, `"method":"textDocument/didOpen"`) {
		t.Fatalf("missing didOpen: %q", got)
	}
}

func TestClientClose(t *testing.T) {
	conn := newScriptedConn(frame(0, nil))
	c := &client{lang: &Language{ID: "go", Display: "Go"}, roots: []string{"/repo"}, conn: conn}
	c.t = newTransport(conn)
	c.t.start(c.handleRequest, c.handleNotification)
	if err := c.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !conn.closed {
		t.Fatal("conn not closed")
	}
	if !c.closed {
		t.Fatal("client not marked closed")
	}
}

func TestReadLoopRequestAndNotification(t *testing.T) {
	req := []byte(`{"jsonrpc":"2.0","id":7,"method":"workspace/applyEdit","params":{}}`)
	notif := []byte(`{"jsonrpc":"2.0","method":"$/progress","params":{"token":"t"}}`)
	var stream bytes.Buffer
	for _, body := range [][]byte{req, notif} {
		fmt.Fprintf(&stream, "Content-Length: %d\r\n\r\n", len(body))
		stream.Write(body)
	}
	tp := newTransport(&readerConn{r: bytes.NewReader(stream.Bytes())})
	reqCh := make(chan string, 1)
	notifCh := make(chan string, 1)
	tp.start(func(method string, _ json.RawMessage) (any, error) {
		reqCh <- method
		return nil, nil
	}, func(method string, _ json.RawMessage) {
		notifCh <- method
	})

	select {
	case m := <-reqCh:
		if m != "workspace/applyEdit" {
			t.Fatalf("request method = %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request not delivered")
	}
	select {
	case m := <-notifCh:
		if m != "$/progress" {
			t.Fatalf("notification method = %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification not delivered")
	}
}
