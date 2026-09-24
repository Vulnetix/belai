package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

const reqCap = 1 << 20 // 1 MiB per LSP message, hard cap.

var errTransportClosed = errors.New("transport closed")

type rawMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	// Data intentionally dropped.
}

type rpcRequest struct {
	Method string
	Params json.RawMessage
}

// onRequestCallback must return either a json-serialisable result or an error.
type onRequestCallback func(method string, params json.RawMessage) (any, error)

type responseSlot struct {
	ch    chan rawMsg
	ready bool
}

type transport struct {
	conn     Conn
	reader   *bufio.Reader
	writeMu  sync.Mutex
	nextID   int
	pending  map[int]*responseSlot
	mu       sync.Mutex
	closed   bool
	done     chan struct{}
	callback onRequestCallback
	notify   func(method string, params json.RawMessage)
}

func newTransport(conn Conn) *transport {
	return &transport{
		conn:    conn,
		reader:  bufio.NewReader(conn),
		pending: map[int]*responseSlot{},
		done:    make(chan struct{}),
	}
}

func (t *transport) start(callback onRequestCallback, notify func(method string, params json.RawMessage)) {
	t.callback = callback
	t.notify = notify
	go t.readLoop()
}

// call sends a request and waits for a response, or the context.
func (t *transport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := t.nextID
	t.nextID++
	paramsJSON := json.RawMessage("null")
	if params != nil {
		var err error
		paramsJSON, err = json.Marshal(params)
		if err != nil {
			return nil, err
		}
	}
	msg := rawMsg{JSONRPC: "2.0", ID: json.RawMessage(strconv.Itoa(id)), Method: method, Params: paramsJSON}
	slot := &responseSlot{ch: make(chan rawMsg, 1)}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, errTransportClosed
	}
	t.pending[id] = slot
	t.mu.Unlock()
	if err := t.write(msg); err != nil {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, err
	}
	select {
	case resp, ok := <-slot.ch:
		if !ok {
			return nil, errTransportClosed
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("jsonrpc error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	case <-ctx.Done():
		go t.close()
		return nil, ctx.Err()
	case <-t.done:
		return nil, errTransportClosed
	}
}

// notify sends a fire-and-forget notification.
func (t *transport) notifyJSON(method string, params any) error {
	paramsJSON := json.RawMessage("null")
	if params != nil {
		var err error
		paramsJSON, err = json.Marshal(params)
		if err != nil {
			return err
		}
	}
	return t.write(rawMsg{JSONRPC: "2.0", Method: method, Params: paramsJSON})
}

func (t *transport) write(msg rawMsg) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if len(body) > reqCap {
		return fmt.Errorf("request body %d exceeds cap", len(body))
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n", len(body))
	b.Write(body)
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if t.isClosed() {
		return errTransportClosed
	}
	_, err = t.conn.Write(b.Bytes())
	return err
}

// close shuts down the transport. It is safe to call more than once.
func (t *transport) close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.drainAllLocked(errTransportClosed)
	t.mu.Unlock()
	t.writeMu.Lock()
	err := t.conn.Close()
	t.writeMu.Unlock()
	return err
}

func (t *transport) readLoop() {
	defer close(t.done)
	for {
		msg, err := t.readOne()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.DeadlineExceeded) {
				// Deadline exceeded only used during intentional shutdown.
			}
			t.mu.Lock()
			t.closed = true
			t.drainAllLocked(errTransportClosed)
			t.mu.Unlock()
			t.writeMu.Lock()
			_ = t.conn.Close()
			t.writeMu.Unlock()
			return
		}

		if msg.ID != nil && msg.Method == "" {
			// Response.
			t.handleResponse(msg)
			continue
		}
		if msg.Method != "" && msg.ID != nil && t.callback != nil {
			// Request.
			go t.handleRequest(msg)
			continue
		}
		if msg.Method != "" && msg.ID == nil && t.notify != nil {
			t.notify(msg.Method, msg.Params)
		}
	}
}

func (t *transport) readOne() (rawMsg, error) {
	var msg rawMsg
	var length int
	for {
		line, err := t.reader.ReadString('\n')
		if err != nil {
			return msg, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return msg, fmt.Errorf("bad header: %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil || n < 0 || n > reqCap {
				return msg, fmt.Errorf("bad content length: %q", val)
			}
			length = n
		}
	}
	if length == 0 {
		return msg, errors.New("missing Content-Length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(t.reader, body); err != nil {
		return msg, err
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return msg, err
	}
	return msg, nil
}

func (t *transport) handleResponse(msg rawMsg) {
	var id int
	if err := json.Unmarshal(msg.ID, &id); err != nil {
		return
	}
	t.mu.Lock()
	slot, ok := t.pending[id]
	if ok {
		delete(t.pending, id)
	}
	t.mu.Unlock()
	if ok {
		slot.ch <- msg
	}
}

func (t *transport) handleRequest(msg rawMsg) {
	var resp rawMsg
	resp.JSONRPC = "2.0"
	resp.ID = msg.ID
	if t.callback == nil {
		resp.Error = &rpcError{Code: -32601, Message: "Method not found"}
	} else {
		result, err := t.callback(msg.Method, msg.Params)
		if err != nil {
			resp.Error = &rpcError{Code: -32601, Message: err.Error()}
		} else {
			b, _ := json.Marshal(result)
			resp.Result = b
		}
	}
	_ = t.write(resp)
}

func (t *transport) drainAllLocked(err error) {
	for id, slot := range t.pending {
		delete(t.pending, id)
		close(slot.ch)
	}
}

// closed reports whether the transport has been shut down.
func (t *transport) isClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}
