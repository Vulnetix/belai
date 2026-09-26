package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/vulnetix/belai/internal/calltrace"
	"github.com/vulnetix/belai/internal/jsonrpc"
)

// maxHTTPBody caps one response from an http server.
const maxHTTPBody = 16 << 20

// httpTransport speaks MCP's streamable HTTP transport: every message is a
// POST, answered with JSON or a server-sent event stream.
type httpTransport struct {
	url     string
	headers map[string]string
	client  *http.Client

	mu        sync.Mutex
	nextID    int64
	sessionID string
	lastErr   string
}

func newHTTP(url string, headers map[string]string, client *http.Client) *httpTransport {
	return &httpTransport{url: url, headers: headers, client: client}
}

func (t *httpTransport) post(ctx context.Context, m *jsonrpc.Message) (*http.Response, error) {
	m.JSONRPC = "2.0"
	body, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	for k, v := range t.headers {
		req.Header.Set(k, expand(v))
	}
	t.mu.Lock()
	if t.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	t.mu.Unlock()
	calltrace.Apply(ctx, req.Header)
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}
	if resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		t.mu.Lock()
		t.lastErr = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
		t.mu.Unlock()
		return nil, fmt.Errorf("mcp server returned HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

func (t *httpTransport) call(ctx context.Context, method string, params, result any) error {
	t.mu.Lock()
	t.nextID++
	id := json.RawMessage(strconv.FormatInt(t.nextID, 10))
	t.mu.Unlock()
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = b
	}
	resp, err := t.post(ctx, &jsonrpc.Message{ID: id, Method: method, Params: raw})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body := io.LimitReader(resp.Body, maxHTTPBody)
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	var m *jsonrpc.Message
	if mt == "text/event-stream" {
		m, err = readSSE(body, id)
	} else {
		m = &jsonrpc.Message{}
		err = json.NewDecoder(body).Decode(m)
	}
	if err != nil {
		return fmt.Errorf("mcp response: %w", err)
	}
	if m.Error != nil {
		return m.Error
	}
	if result != nil && len(m.Result) > 0 {
		return json.Unmarshal(m.Result, result)
	}
	return nil
}

// readSSE reads events until the response whose id matches. Server
// requests and notifications on the stream are skipped: Belai offers the
// server nothing to call.
func readSSE(r io.Reader, id json.RawMessage) (*jsonrpc.Message, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxHTTPBody)
	var data strings.Builder
	flush := func() (*jsonrpc.Message, bool) {
		defer data.Reset()
		if data.Len() == 0 {
			return nil, false
		}
		var m jsonrpc.Message
		if json.Unmarshal([]byte(data.String()), &m) != nil {
			return nil, false
		}
		if m.Method == "" && string(m.ID) == string(id) {
			return &m, true
		}
		return nil, false
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if m, ok := flush(); ok {
				return m, nil
			}
			continue
		}
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(v, " "))
		}
	}
	if m, ok := flush(); ok {
		return m, nil
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("event stream ended without a response")
}

func (t *httpTransport) notify(ctx context.Context, method string, params any) error {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = b
	}
	resp, err := t.post(ctx, &jsonrpc.Message{Method: method, Params: raw})
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	return resp.Body.Close()
}

func (t *httpTransport) close() error {
	t.mu.Lock()
	sid := t.sessionID
	t.mu.Unlock()
	if sid == "" {
		return nil
	}
	req, err := http.NewRequest(http.MethodDelete, t.url, nil)
	if err != nil {
		return err
	}
	for k, v := range t.headers {
		req.Header.Set(k, expand(v))
	}
	req.Header.Set("Mcp-Session-Id", sid)
	if resp, err := t.client.Do(req); err == nil {
		resp.Body.Close()
	}
	return nil
}

func (t *httpTransport) diag() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastErr
}
