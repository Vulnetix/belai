// Package jsonrpc is a small JSON-RPC 2.0 peer over newline-delimited JSON,
// the framing MCP's stdio transport and the Agent Client Protocol use. One
// Conn both calls the other side and answers it: requests from the peer are
// dispatched on their own goroutine, so a long-running handler never blocks
// the read loop, and responses are matched to pending calls by id.
package jsonrpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// MaxLine caps one message. A peer that sends more is disconnected.
const MaxLine = 16 << 20

// Standard error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Error is a JSON-RPC error object. A handler may return one to choose the
// code; any other error is reported as an internal error.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message) }

// Errorf builds an Error.
func Errorf(code int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Message is one JSON-RPC message on the wire.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// IsRequest reports whether m is a request (a method with an id).
func (m *Message) IsRequest() bool { return m.Method != "" && len(m.ID) > 0 }

// IsNotification reports whether m is a notification (a method, no id).
func (m *Message) IsNotification() bool { return m.Method != "" && len(m.ID) == 0 }

// Handler answers a request or notification from the peer. For a
// notification the result is discarded.
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// Conn is one JSON-RPC connection.
type Conn struct {
	w       io.Writer
	wmu     sync.Mutex
	handler Handler
	ctx     context.Context
	cancel  context.CancelFunc

	mu      sync.Mutex
	nextID  int64
	pending map[string]chan *Message
	err     error
	done    chan struct{}
}

// NewConn starts reading r. handler may be nil, in which case every request
// from the peer is answered with method-not-found.
func NewConn(r io.Reader, w io.Writer, handler Handler) *Conn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{w: w, handler: handler, ctx: ctx, cancel: cancel, pending: map[string]chan *Message{}, done: make(chan struct{})}
	go c.readLoop(r)
	return c
}

// Done is closed when the connection ends.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err is why the connection ended (io.EOF on a clean close).
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close stops the connection and fails every pending call. It does not close
// the underlying streams; the owner does.
func (c *Conn) Close() { c.shutdown(errors.New("connection closed")) }

func (c *Conn) shutdown(err error) {
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return
	default:
	}
	c.err = err
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	close(c.done)
	c.mu.Unlock()
	c.cancel()
}

func (c *Conn) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), MaxLine)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m Message
		if err := json.Unmarshal(line, &m); err != nil {
			_ = c.write(&Message{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: Errorf(CodeParseError, "parse error")})
			continue
		}
		switch {
		case m.Method != "":
			go c.dispatch(&m)
		case len(m.ID) > 0:
			c.mu.Lock()
			ch, ok := c.pending[string(m.ID)]
			delete(c.pending, string(m.ID))
			c.mu.Unlock()
			if ok {
				ch <- &m
			}
		}
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	c.shutdown(err)
}

func (c *Conn) dispatch(m *Message) {
	var result any
	err := error(Errorf(CodeMethodNotFound, "method not found: %s", m.Method))
	if c.handler != nil {
		result, err = c.handler(c.ctx, m.Method, m.Params)
	}
	if m.IsNotification() {
		return
	}
	resp := &Message{JSONRPC: "2.0", ID: m.ID}
	if err != nil {
		var rpcErr *Error
		if errors.As(err, &rpcErr) {
			resp.Error = rpcErr
		} else {
			resp.Error = &Error{Code: CodeInternalError, Message: err.Error()}
		}
	} else {
		raw, merr := json.Marshal(result)
		if merr != nil {
			resp.Error = &Error{Code: CodeInternalError, Message: merr.Error()}
		} else {
			resp.Result = raw
		}
	}
	_ = c.write(resp)
}

func (c *Conn) write(m *Message) error {
	m.JSONRPC = "2.0"
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.w.Write(append(data, '\n'))
	return err
}

// Call sends a request and waits for its response, decoding the result into
// result (which may be nil). A peer error is returned as *Error.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return fmt.Errorf("jsonrpc: connection closed: %v", c.err)
	default:
	}
	c.nextID++
	id := json.RawMessage(strconv.FormatInt(c.nextID, 10))
	ch := make(chan *Message, 1)
	c.pending[string(id)] = ch
	c.mu.Unlock()

	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = b
	}
	if err := c.write(&Message{ID: id, Method: method, Params: raw}); err != nil {
		c.forget(id)
		return err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return fmt.Errorf("jsonrpc: connection closed: %v", c.Err())
		}
		if m.Error != nil {
			return m.Error
		}
		if result != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, result)
		}
		return nil
	case <-ctx.Done():
		c.forget(id)
		return ctx.Err()
	}
}

func (c *Conn) forget(id json.RawMessage) {
	c.mu.Lock()
	delete(c.pending, string(id))
	c.mu.Unlock()
}

// Notify sends a notification.
func (c *Conn) Notify(method string, params any) error {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = b
	}
	return c.write(&Message{Method: method, Params: raw})
}
