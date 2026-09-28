// Package sessionsync mirrors Belai sessions to the Vulnetix website and
// carries prompts typed there back into the live session.
//
// The host is the source of truth. The Syncer never builds a transcript entry
// of its own: it tails the session's JSONL file and uploads each line with its
// line index (seq), so what the website shows is exactly what is on disk, an
// upload can be retried or repeated without duplicating anything, and a host
// that restarts resumes from the server's high-water mark. A prompt from the
// website is a request (RemotePrompt) that the TUI admits and writes to the
// JSONL like a typed prompt; only then, through the same tail, does it reach
// the website as a transcript line.
//
// Requests go only to the Vulnetix console (https://*.vulnetix.com, or a
// loopback origin for local development) with the Vulnetix CLI's own
// credential in the Authorization header. See docs/session-sync.md.
package sessionsync

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultWebURL is the Vulnetix console origin; $VULNETIX_WEB_URL overrides it
// exactly as it does for the CLI device login.
const DefaultWebURL = "https://www.vulnetix.com"

// apiPath is where the console proxies vdb-site's /v1/belai routes.
const apiPath = "/api/site/v1/belai"

// requestTimeout bounds every call but the inbox long-poll.
const requestTimeout = 20 * time.Second

// ErrNotFound is a 404: the session or host is not the caller's, or is gone.
var ErrNotFound = errors.New("sessionsync: not found")

// ErrUnauthorized is a 401: the credential was refused.
var ErrUnauthorized = errors.New("sessionsync: the Vulnetix credential was refused")

// BaseURL returns the belai API base for a console origin ("" = default).
func BaseURL(origin string) string {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if origin == "" {
		origin = DefaultWebURL
	}
	return origin + apiPath
}

// AllowedOrigin reports whether the credential may be sent to rawURL: https on
// vulnetix.com or a subdomain, or any scheme on a loopback host (local dev).
func AllowedOrigin(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.User != nil || u.Host == "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if h == "localhost" {
		return u.Scheme == "http" || u.Scheme == "https"
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return u.Scheme == "http" || u.Scheme == "https"
	}
	return u.Scheme == "https" && (h == "vulnetix.com" || strings.HasSuffix(h, ".vulnetix.com"))
}

// Client is the thin HTTP client for /v1/belai. AuthHeader is read on every
// request so a re-login takes effect without a restart.
type Client struct {
	Base       string
	AuthHeader func() (string, error)
	HTTP       *http.Client
}

// NewClient validates base and returns a client for it.
func NewClient(base string, auth func() (string, error), hc *http.Client) (*Client, error) {
	if !AllowedOrigin(base) {
		return nil, fmt.Errorf("sessionsync: refusing to send the Vulnetix credential to %s", base)
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{Base: strings.TrimRight(base, "/"), AuthHeader: auth, HTTP: hc}, nil
}

// Host is what the website shows for a machine.
type Host struct {
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	BelaiVersion string `json:"belaiVersion"`
	// RC is sent only by `belai rc`: the directories it will start sessions
	// in and how many it runs at once.
	RC *RCInfo `json:"rc,omitempty"`
}

// RCInfo is a remote-control daemon's advertisement.
type RCInfo struct {
	Dirs        []RCDir `json:"dirs"`
	MaxSessions int     `json:"maxSessions"`
}

// RCDir is one directory an rc daemon offers. Source is "trusted" (a project
// the host already trusted) or "arg" (passed as `belai rc --dir`).
type RCDir struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

// Dispatch is a website request to an rc daemon: start a session in Cwd with
// Prompt, or (Kind "stop") stop SessionID. Everything in it is untrusted: the
// daemon re-checks Cwd against its own list and the prompt goes through the
// same admission as a typed one.
type Dispatch struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Cwd       string `json:"cwd"`
	Mode      string `json:"mode"`
	Prompt    string `json:"prompt"`
	SessionID string `json:"sessionId"`
	CreatedAt int64  `json:"createdAt"`
}

// Dispatch outcomes the daemon reports back.
const (
	DispatchStarted = "started"
	DispatchStopped = "stopped"
	DispatchRefused = "refused"
)

// SessionMeta is the session's registration and display metadata.
type SessionMeta struct {
	HostID          string `json:"hostId"`
	ProjectKey      string `json:"projectKey,omitempty"`
	ProjectName     string `json:"projectName,omitempty"`
	Cwd             string `json:"cwd,omitempty"`
	Name            string `json:"name,omitempty"`
	Model           string `json:"model,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Mode            string `json:"mode,omitempty"`
	ParentSessionID string `json:"parentSessionId,omitempty"`
	ResumedFromID   string `json:"resumedFromId,omitempty"`
	RemotePrompts   bool   `json:"remotePrompts"`
	// RemoteAnswers says the host takes web answers to its open asks.
	RemoteAnswers bool  `json:"remoteAnswers"`
	CreatedAt     int64 `json:"createdAt,omitempty"`
	// DispatchID is the website request that started this session on an rc
	// daemon; empty for a session someone started at the terminal.
	DispatchID string `json:"dispatchId,omitempty"`
}

// Entry is one JSONL line as uploaded: the session.Entry fields plus seq.
type Entry struct {
	Seq        int64           `json:"seq"`
	ID         string          `json:"id"`
	ParentID   string          `json:"parentId,omitempty"`
	Type       string          `json:"type"`
	Role       string          `json:"role,omitempty"`
	Content    string          `json:"content,omitempty"`
	Timestamp  int64           `json:"timestamp"`
	Meta       json.RawMessage `json:"meta,omitempty"`
	SubagentID string          `json:"subagent_id,omitempty"`
}

// RemotePrompt is a prompt typed on the website, claimed from the inbox.
type RemotePrompt struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"createdAt"`
}

// RemoteAnswer is a web answer to a question the host asked, claimed from the
// inbox. AskID is the host's ask entry id; Payload is untrusted JSON the host
// validates against the ask that is actually open (see the TUI's
// handleRemoteAnswer).
type RemoteAnswer struct {
	ID        string          `json:"id"`
	SessionID string          `json:"sessionId"`
	AskID     string          `json:"askId"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt int64           `json:"createdAt"`
}

// Prompt outcomes the host reports back.
const (
	AckQueued   = "queued"
	AckAccepted = "accepted"
	AckRefused  = "refused"
)

// gzipMinBytes is the body size from which an entry upload is compressed.
// Diffs make lines large and compress well; small batches are not worth it.
const gzipMinBytes = 8 << 10

func (c *Client) do(ctx context.Context, method, path string, in, out any, timeout time.Duration) error {
	return c.doBody(ctx, method, path, in, out, timeout, false)
}

func (c *Client) doBody(ctx context.Context, method, path string, in, out any, timeout time.Duration, compress bool) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	gzipped := false
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		if compress && len(b) >= gzipMinBytes {
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			if _, err := zw.Write(b); err == nil && zw.Close() == nil {
				b, gzipped = buf.Bytes(), true
			}
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return err
	}
	auth, err := c.AuthHeader()
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if gzipped {
		req.Header.Set("Content-Encoding", "gzip")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("sessionsync: %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// PutHost registers or refreshes this machine.
func (c *Client) PutHost(ctx context.Context, hostID string, h Host) error {
	return c.do(ctx, http.MethodPut, "/hosts/"+url.PathEscape(hostID), h, nil, requestTimeout)
}

// PutSession registers the session as live and returns the server's lastSeq
// (-1 when it holds no lines yet).
func (c *Client) PutSession(ctx context.Context, sessionID string, m SessionMeta) (int64, error) {
	var out struct {
		LastSeq int64 `json:"lastSeq"`
	}
	err := c.do(ctx, http.MethodPut, "/sessions/"+url.PathEscape(sessionID), m, &out, requestTimeout)
	return out.LastSeq, err
}

// PostEntries uploads lines; repeats are ignored server-side.
func (c *Client) PostEntries(ctx context.Context, sessionID string, entries []Entry) (int64, error) {
	var out struct {
		LastSeq int64 `json:"lastSeq"`
	}
	err := c.doBody(ctx, http.MethodPost, "/sessions/"+url.PathEscape(sessionID)+"/entries",
		map[string]any{"entries": entries}, &out, requestTimeout, true)
	return out.LastSeq, err
}

// Heartbeat keeps the session live.
func (c *Client) Heartbeat(ctx context.Context, sessionID string) error {
	return c.do(ctx, http.MethodPost, "/sessions/"+url.PathEscape(sessionID)+"/heartbeat", nil, nil, requestTimeout)
}

// End moves the session into History.
func (c *Client) End(ctx context.Context, sessionID string) error {
	return c.do(ctx, http.MethodPost, "/sessions/"+url.PathEscape(sessionID)+"/end", nil, nil, requestTimeout)
}

// Inbox long-polls for web prompts and web answers addressed to sessionID, a
// live session of this host. A server that predates answers sends none; one
// that predates the session filter hands over the whole host's inbox, which
// the TUI refuses per prompt as before.
func (c *Client) Inbox(ctx context.Context, hostID, sessionID string, wait time.Duration) ([]RemotePrompt, []RemoteAnswer, error) {
	var out struct {
		Prompts []RemotePrompt `json:"prompts"`
		Answers []RemoteAnswer `json:"answers"`
	}
	path := fmt.Sprintf("/hosts/%s/inbox?wait=%d", url.PathEscape(hostID), int(wait/time.Second))
	if sessionID != "" {
		path += "&session=" + url.QueryEscape(sessionID)
	}
	err := c.do(ctx, http.MethodGet, path, nil, &out, wait+requestTimeout)
	return out.Prompts, out.Answers, err
}

// AckAnswer reports what the host did with a web answer: accepted (applied,
// with the ask_answer entry id) or refused (with a reason).
func (c *Client) AckAnswer(ctx context.Context, answerID, status, reason, entryID string) error {
	return c.do(ctx, http.MethodPost, "/answers/"+url.PathEscape(answerID)+"/ack",
		map[string]string{"status": status, "reason": reason, "entryId": entryID}, nil, requestTimeout)
}

// Ack reports what the host did with a web prompt.
func (c *Client) Ack(ctx context.Context, promptID, status, reason, entryID string) error {
	return c.do(ctx, http.MethodPost, "/prompts/"+url.PathEscape(promptID)+"/ack",
		map[string]string{"status": status, "reason": reason, "entryId": entryID}, nil, requestTimeout)
}

// RCHeartbeat keeps an rc daemon online and reports how many sessions it runs.
func (c *Client) RCHeartbeat(ctx context.Context, hostID string, running int) error {
	return c.do(ctx, http.MethodPost, "/hosts/"+url.PathEscape(hostID)+"/rc/heartbeat",
		map[string]int{"running": running}, nil, requestTimeout)
}

// RCOffline marks the rc daemon stopped; requests still waiting expire.
func (c *Client) RCOffline(ctx context.Context, hostID string) error {
	return c.do(ctx, http.MethodPost, "/hosts/"+url.PathEscape(hostID)+"/rc/offline", nil, nil, requestTimeout)
}

// Dispatches long-polls for website requests to this rc daemon.
func (c *Client) Dispatches(ctx context.Context, hostID string, wait time.Duration) ([]Dispatch, error) {
	var out struct {
		Dispatches []Dispatch `json:"dispatches"`
	}
	path := fmt.Sprintf("/hosts/%s/dispatch?wait=%d", url.PathEscape(hostID), int(wait/time.Second))
	err := c.do(ctx, http.MethodGet, path, nil, &out, wait+requestTimeout)
	return out.Dispatches, err
}

// AckDispatch reports what the daemon did with a request: started (with the
// session id it minted), stopped, or refused (with a reason).
func (c *Client) AckDispatch(ctx context.Context, dispatchID, status, sessionID, reason string) error {
	return c.do(ctx, http.MethodPost, "/dispatches/"+url.PathEscape(dispatchID)+"/ack",
		map[string]string{"status": status, "sessionId": sessionID, "reason": reason}, nil, requestTimeout)
}
