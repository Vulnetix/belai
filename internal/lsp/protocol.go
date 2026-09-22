package lsp

import "encoding/json"

// Position is the minimal LSP position shape.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is the minimal LSP range shape.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// TextDocumentItem is used in textDocument/didOpen.
type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

// DidOpenTextDocumentParams params for textDocument/didOpen.
type DidOpenTextDocumentParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

// TextDocumentContentChangeEvent with full text.
type TextDocumentContentChangeEvent struct {
	Text string `json:"text"`
}

// DidChangeTextDocumentParams params for textDocument/didChange.
type DidChangeTextDocumentParams struct {
	TextDocument   VersionedTextDocumentIdentifier  `json:"textDocument"`
	ContentChanges []TextDocumentContentChangeEvent `json:"contentChanges"`
}

// VersionedTextDocumentIdentifier is a document reference with version.
type VersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

// TextDocumentIdentifier is a plain document reference.
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// DocumentDiagnosticParams params for textDocument/diagnostic.
type DocumentDiagnosticParams struct {
	Identifier       string                 `json:"identifier,omitempty"`
	TextDocument     TextDocumentIdentifier `json:"textDocument"`
	PreviousResultID string                 `json:"previousResultId,omitempty"`
}

// DocumentDiagnosticReport is the result of textDocument/diagnostic.
// Servers may return a FullDocumentDiagnosticReport or
// UnchangedDocumentDiagnosticReport.
type DocumentDiagnosticReport struct {
	Kind      string          `json:"kind"`
	ResultID  string          `json:"resultId,omitempty"`
	Items     []Diagnostic    `json:"items,omitempty"`
	ItemsJSON json.RawMessage `json:"-"`
}

// RawDiagnostic is the server-provided diagnostic with only the fields we read.
// relatedInformation, codeDescription.href, data and tags are decoded into
// json.RawMessage and dropped.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity *int   `json:"severity,omitempty"`
	Code     any    `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// PublishDiagnosticsParams params for textDocument/publishDiagnostics.
type PublishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Version     int          `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// WorkspaceFolder is sent during initialize.
type WorkspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

// ServerCapabilities subset of initialize result capabilities.
type ServerCapabilities struct {
	DiagnosticProvider *DiagnosticOptions `json:"diagnosticProvider,omitempty"`
}

// DiagnosticOptions may carry identifier/etc. We only need presence.
type DiagnosticOptions struct {
	Identifier            string `json:"identifier,omitempty"`
	InterFileDependencies bool   `json:"interFileDependencies,omitempty"`
	WorkspaceDiagnostics  bool   `json:"workspaceDiagnostics,omitempty"`
}

// InitializeResult from a server.
type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
}

// ClientInfo is the client name in initialize params.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// InitializeParams is the minimal initialize request.
type InitializeParams struct {
	ProcessID             int                `json:"processID"`
	ClientInfo            ClientInfo         `json:"clientInfo"`
	Locale                string             `json:"locale,omitempty"`
	RootURI               string             `json:"rootUri,omitempty"`
	WorkspaceFolders      []WorkspaceFolder  `json:"workspaceFolders,omitempty"`
	InitializationOptions json.RawMessage    `json:"initializationOptions,omitempty"`
	Capabilities          ClientCapabilities `json:"capabilities"`
}

// ClientCapabilities minimal shape.
type ClientCapabilities struct {
	TextDocument TextDocumentClientCapabilities `json:"textDocument"`
	Workspace    WorkspaceClientCapabilities    `json:"workspace"`
	General      GeneralClientCapabilities      `json:"general"`
	Window       WindowClientCapabilities       `json:"window"`
}

// TextDocumentClientCapabilities minimal shape.
type TextDocumentClientCapabilities struct {
	Synchronization    TextDocumentSyncClientCapabilities   `json:"synchronization"`
	PublishDiagnostics PublishDiagnosticsClientCapabilities `json:"publishDiagnostics"`
	Diagnostic         DiagnosticClientCapabilities         `json:"diagnostic"`
}

// TextDocumentSyncClientCapabilities minimal shape.
type TextDocumentSyncClientCapabilities struct {
	DynamicRegistration bool `json:"dynamicRegistration"`
	WillSave            bool `json:"willSave"`
	WillSaveWaitUntil   bool `json:"willSaveWaitUntil"`
	DidSave             bool `json:"didSave"`
}

// PublishDiagnosticsClientCapabilities minimal shape.
type PublishDiagnosticsClientCapabilities struct {
	RelatedInformation bool `json:"relatedInformation"`
	VersionSupport     bool `json:"versionSupport"`
	TagSupport         struct {
		ValueSet []int `json:"valueSet"`
	} `json:"tagSupport"`
}

// DiagnosticClientCapabilities minimal shape.
type DiagnosticClientCapabilities struct {
	DynamicRegistration bool `json:"dynamicRegistration"`
	RelatedDocument     bool `json:"relatedDocument"`
}

// WorkspaceClientCapabilities minimal shape.
type WorkspaceClientCapabilities struct {
	WorkspaceFolders bool `json:"workspaceFolders"`
	Configuration    struct {
		DynamicRegistration bool `json:"dynamicRegistration"`
	} `json:"configuration"`
}

// GeneralClientCapabilities minimal shape.
type GeneralClientCapabilities struct {
	PositionEncodings []string `json:"positionEncodings"`
}

// WindowClientCapabilities minimal shape.
type WindowClientCapabilities struct {
	WorkDoneProgress bool `json:"workDoneProgress"`
}

// DidChangeWatchedFilesParams params for workspace/didChangeWatchedFiles.
type DidChangeWatchedFilesParams struct {
	Changes []FileEvent `json:"changes"`
}

// FileEvent is one changed file notification.
type FileEvent struct {
	URI  string `json:"uri"`
	Type int    `json:"type"`
}

// ApplyEditParams is the inbound workspace/applyEdit request.
type ApplyEditParams struct {
	Label string          `json:"label,omitempty"`
	Edit  json.RawMessage `json:"edit"`
}

// ApplyEditResult is always {"applied":false}.
type ApplyEditResult struct {
	Applied bool `json:"applied"`
}

// ConfigurationItem and WorkspaceConfigurationParams for workspace/configuration.
type ConfigurationItem struct {
	ScopeURI string `json:"scopeUri,omitempty"`
	Section  string `json:"section,omitempty"`
}

// WorkspaceConfigurationParams params.
type WorkspaceConfigurationParams struct {
	Items []ConfigurationItem `json:"items"`
}

// WorkDoneProgressCreateParams params.
type WorkDoneProgressCreateParams struct {
	Token string `json:"token"`
}

const (
	fileChangeCreated = 1
	fileChangeChanged = 2
	fileChangeDeleted = 3
)
