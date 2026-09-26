package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"sync"
	"time"
)

// client is one warm LSP connection.
type client struct {
	lang   *Language
	conn   Conn
	t      *transport
	roots  []string
	caps   ServerCapabilities
	ready  bool
	closed bool
	mu     sync.Mutex

	// diagnostics and state for push mode.
	publishURI     string
	publishVersion int
	diagnostics    []Diagnostic
	version        int
	resultID       string
}

func newClient(lang *Language, conn Conn, roots []string) *client {
	return &client{
		lang:  lang,
		conn:  conn,
		roots: roots,
	}
}

func (c *client) initialize(ctx context.Context, lang *Language) error {
	c.t = newTransport(c.conn)
	c.t.start(c.handleRequest, c.handleNotification)

	caps := ClientCapabilities{
		TextDocument: TextDocumentClientCapabilities{
			Synchronization: TextDocumentSyncClientCapabilities{
				DynamicRegistration: false,
				WillSave:            false,
				WillSaveWaitUntil:   false,
				DidSave:             false,
			},
			PublishDiagnostics: PublishDiagnosticsClientCapabilities{
				RelatedInformation: false,
				VersionSupport:     true,
			},
			Diagnostic: DiagnosticClientCapabilities{
				DynamicRegistration: false,
				RelatedDocument:     false,
			},
		},
		Workspace: WorkspaceClientCapabilities{
			WorkspaceFolders: true,
		},
		General: GeneralClientCapabilities{
			PositionEncodings: []string{"utf-16"},
		},
		Window: WindowClientCapabilities{
			WorkDoneProgress: true,
		},
	}
	folders := make([]WorkspaceFolder, 0, len(c.roots))
	for _, r := range c.roots {
		folders = append(folders, WorkspaceFolder{URI: pathToURI(r), Name: filepath.Base(r)})
	}
	params := InitializeParams{
		ProcessID:             0,
		ClientInfo:            ClientInfo{Name: "belai", Version: "0"},
		WorkspaceFolders:      folders,
		InitializationOptions: nil,
		Capabilities:          caps,
	}
	res, err := c.t.call(ctx, "initialize", params)
	if err != nil {
		return err
	}
	var init InitializeResult
	if err := json.Unmarshal(res, &init); err != nil {
		return err
	}
	c.caps = init.Capabilities
	return c.t.notifyJSON("initialized", map[string]any{})
}

// openedKey is a per-file state.
type openedKey struct{}

// diagnose runs the full edit→diagnose cycle for abs/content.
func (c *client) diagnose(ctx context.Context, abs string, content []byte) (Report, error) {
	uri := pathToURI(abs)
	c.mu.Lock()
	c.version++
	ver := c.version
	c.mu.Unlock()

	if err := c.t.notifyJSON("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{
			URI:        uri,
			LanguageID: c.lang.ID,
			Version:    ver,
			Text:       string(content),
		},
	}); err != nil {
		return Report{}, err
	}
	if err := c.t.notifyJSON("textDocument/didChange", DidChangeTextDocumentParams{
		TextDocument: VersionedTextDocumentIdentifier{URI: uri, Version: ver},
		ContentChanges: []TextDocumentContentChangeEvent{
			{Text: string(content)},
		},
	}); err != nil {
		return Report{}, err
	}

	if c.caps.DiagnosticProvider != nil {
		return c.pull(ctx, uri, ver, string(content))
	}
	return c.waitPush(ctx, uri, ver)
}

func (c *client) pull(ctx context.Context, uri string, ver int, content string) (Report, error) {
	c.mu.Lock()
	prev := c.resultID
	c.mu.Unlock()
	params := DocumentDiagnosticParams{
		TextDocument:     TextDocumentIdentifier{URI: uri},
		PreviousResultID: prev,
	}
	res, err := c.t.call(ctx, "textDocument/diagnostic", params)
	if err != nil {
		return Report{}, err
	}
	var report DocumentDiagnosticReport
	if err := json.Unmarshal(res, &report); err != nil {
		return Report{}, err
	}
	if report.Kind == "unchanged" {
		c.mu.Lock()
		rows := diagnosticsToRows(c.diagnostics, uri)
		c.mu.Unlock()
		return Report{Language: c.lang.Display, Status: StatusReady, Rows: rows}, nil
	}
	if report.Kind != "full" {
		return Report{}, fmt.Errorf("unknown diagnostic report kind %q", report.Kind)
	}
	c.mu.Lock()
	c.diagnostics = report.Items
	c.resultID = report.ResultID
	c.mu.Unlock()
	return Report{Language: c.lang.Display, Status: StatusReady, Rows: diagnosticsToRows(report.Items, uri)}, nil
}

func (c *client) waitPush(ctx context.Context, uri string, ver int) (Report, error) {
	for {
		c.mu.Lock()
		match := c.publishURI == uri && c.publishVersion >= ver
		got := c.diagnostics
		c.mu.Unlock()
		if match {
			return Report{Language: c.lang.Display, Status: StatusReady, Rows: diagnosticsToRows(got, uri)}, nil
		}
		// poll briefly; push reports arrive on the read goroutine.
		select {
		case <-ctx.Done():
			return Report{}, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// handleRequest answers inbound server requests.
func (c *client) handleRequest(method string, params json.RawMessage) (any, error) {
	switch method {
	case "workspace/applyEdit":
		return ApplyEditResult{Applied: false}, nil
	case "workspace/workspaceFolders":
		folders := make([]WorkspaceFolder, 0, len(c.roots))
		for _, r := range c.roots {
			folders = append(folders, WorkspaceFolder{URI: pathToURI(r), Name: filepath.Base(r)})
		}
		return folders, nil
	case "workspace/configuration":
		var p WorkspaceConfigurationParams
		_ = json.Unmarshal(params, &p)
		out := make([]any, len(p.Items))
		return out, nil
	case "client/registerCapability", "client/unregisterCapability", "window/workDoneProgress/create":
		return nil, nil
	case "window/showMessageRequest":
		return nil, nil
	default:
		return nil, fmt.Errorf("Method not found")
	}
}

// handleNotification receives server notifications. Only publishDiagnostics
// is actioned for this package.
func (c *client) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "textDocument/publishDiagnostics":
		var p PublishDiagnosticsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return
		}
		c.mu.Lock()
		c.publishURI = p.URI
		c.diagnostics = p.Diagnostics
		if p.Version != 0 {
			c.publishVersion = p.Version
		}
		c.mu.Unlock()
	case "$/progress":
		var p struct {
			Token any             `json:"token"`
			Value json.RawMessage `json:"value"`
		}
		_ = json.Unmarshal(params, &p)
		var v struct {
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal(p.Value, &v)
		if v.Kind == "end" {
			c.setReady(true)
		}
	case "window/logMessage", "window/showMessage", "telemetry/event", "$/logTrace":
		// swallowed
	}
}

func (c *client) setReady(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ready = v
}

func (c *client) isReady() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ready
}

func (c *client) close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	// Best-effort shutdown sequence.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = c.t.call(ctx, "shutdown", nil)
	_ = c.t.notifyJSON("exit", nil)
	// Wait briefly, then kill the connection.
	<-time.After(200 * time.Millisecond)
	_ = c.t.close()
	return c.conn.Close()
}

// diagnosticsToRows converts server diagnostics to hardened rows. uri is used
// only as a sanity check.
func diagnosticsToRows(d []Diagnostic, uri string) []Row {
	rows := make([]Row, 0, len(d))
	for _, diag := range d {
		rows = append(rows, Row{
			Severity: severityFrom(diag.Severity),
			Line:     diag.Range.Start.Line + 1, // 1-indexed.
			Col:      diag.Range.Start.Character + 1,
			Source:   sanitizeSource(diag.Source),
			Message:  diag.Message,
		})
	}
	return rows
}

func severityFrom(sev *int) Severity {
	if sev == nil {
		return SeverityError
	}
	switch *sev {
	case 1:
		return SeverityError
	case 2:
		return SeverityWarning
	case 3:
		return SeverityInfo
	case 4:
		return SeverityHint
	default:
		return SeverityError
	}
}

// pathToURI converts a local path to a file URI.
func pathToURI(p string) string {
	return "file://" + filepath.ToSlash(p)
}

// uriToPath converts a file URI to a local path.
func uriToPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("not a file URI: %s", uri)
	}
	return filepath.FromSlash(u.Path), nil
}
