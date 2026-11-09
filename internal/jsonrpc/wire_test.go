package jsonrpc

// The tests in this file pin the wire contract: they read the raw bytes a Conn
// writes, where the rest of the suite (which drives Conn against itself)
// cannot see a renamed key, a changed version string or a missing reply.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// wireSink records everything written to it, safe for concurrent use by the
// Conn's writer goroutine and the test.
type wireSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *wireSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *wireSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// wireConn builds a Conn whose outgoing messages land verbatim in out (one
// newline-delimited line per message) and returns the pipe writer for feeding
// it input.
func wireConn(out *wireSink, handler Handler) (*Conn, *io.PipeWriter) {
	pr, pw := io.Pipe()
	return NewConn(pr, out, handler), pw
}

// waitLine blocks until the sink holds a complete line, or fails the test.
func waitLine(t *testing.T, s *wireSink) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if line := s.String(); line != "" {
			return line
		}
		if time.Now().After(deadline) {
			t.Fatal("conn wrote no message within 2s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// assertNoLine fails if the sink receives any message within wait.
func assertNoLine(t *testing.T, s *wireSink, wait time.Duration) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if line := s.String(); line != "" {
			t.Fatalf("unexpected reply line: %s", line)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A Call goes out as one line with exactly the keys jsonrpc, id, method and
// params, and id as the decimal integer string the code builds.
func TestWireCallFormat(t *testing.T) {
	out := &wireSink{}
	c, _ := wireConn(out, nil)
	defer c.Close()
	params := struct {
		A int `json:"a"`
		B int `json:"b"`
	}{A: 1, B: 2}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	// No peer answers, so Call times out; the request is on the wire first.
	if err := c.Call(ctx, "echo", params, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call err = %v", err)
	}
	line := out.String()
	want := `{"jsonrpc":"2.0","id":1,"method":"echo","params":{"a":1,"b":2}}` + "\n"
	if line != want {
		t.Fatalf("wire line = %s, want %s", line, want)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSuffix(line, "\n")), &keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"jsonrpc", "id", "method", "params"} {
		if _, ok := keys[k]; !ok {
			t.Fatalf("missing top-level key %q in %s", k, line)
		}
	}
	if len(keys) != 4 {
		t.Fatalf("top-level keys = %v, want exactly jsonrpc, id, method, params", keys)
	}
}

// A malformed line is answered with the pinned parse-error reply.
func TestWireParseErrorReply(t *testing.T) {
	out := &wireSink{}
	c, pw := wireConn(out, nil)
	defer c.Close()
	defer pw.Close()
	if _, err := pw.Write([]byte("{not json\n")); err != nil {
		t.Fatal(err)
	}
	line := waitLine(t, out)
	want := `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}` + "\n"
	if line != want {
		t.Fatalf("reply = %s, want %s", line, want)
	}
}

// A notification gets no reply line, and a handler-returned *Error keeps its
// code on the wire.
func TestWireNotificationAndHandlerError(t *testing.T) {
	out := &wireSink{}
	c, pw := wireConn(out, func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method == "boom" {
			return nil, Errorf(CodeInvalidParams, "nope")
		}
		return "ok", nil
	})
	defer c.Close()
	defer pw.Close()
	if _, err := pw.Write([]byte(`{"jsonrpc":"2.0","method":"ping"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	// A notification must not be answered.
	assertNoLine(t, out, 250*time.Millisecond)
	if _, err := pw.Write([]byte(`{"jsonrpc":"2.0","id":7,"method":"boom"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	line := waitLine(t, out)
	want := `{"jsonrpc":"2.0","id":7,"error":{"code":-32602,"message":"nope"}}` + "\n"
	if line != want {
		t.Fatalf("reply = %s, want %s", line, want)
	}
	// The whole conversation is still exactly that one line.
	if got := out.String(); got != want {
		t.Fatalf("wire content = %s, want %s", got, want)
	}
}
