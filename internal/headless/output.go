package headless

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/vaultenv"
)

// Format is how a noninteractive run reports to its caller (docs/headless.md).
type Format string

const (
	// FormatText prints the reply and nothing else: the original contract.
	FormatText Format = "text"
	// FormatJSON prints one JSON object, the result, when the run ends.
	FormatJSON Format = "json"
	// FormatStreamJSON prints one JSON object per line: events as they happen,
	// then the result.
	FormatStreamJSON Format = "stream-json"
)

// ParseFormat reads a -output-format value. The empty string is text.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case "", FormatText:
		return FormatText, nil
	case FormatJSON:
		return FormatJSON, nil
	case FormatStreamJSON:
		return FormatStreamJSON, nil
	}
	return "", fmt.Errorf("-output-format must be text, json or stream-json, not %q", s)
}

// Structured reports whether the format is one a program reads.
func (f Format) Structured() bool { return f == FormatJSON || f == FormatStreamJSON }

// The closed set of stream-json event types. An agent event with no entry
// here is dropped, and so are reasoning, permission asks and clarify asks.
const (
	TypeInit       = "init"
	TypeText       = "text"
	TypeToolStart  = "tool_start"
	TypeToolResult = "tool_result"
	TypeWarning    = "warning"
	TypeResult     = "result"
)

// EventTypes lists the stream-json event types, in the order docs/headless.md
// describes them.
var EventTypes = []string{TypeInit, TypeText, TypeToolStart, TypeToolResult, TypeWarning, TypeResult}

// The result subtypes, which also decide the exit code.
const (
	SubtypeSuccess       = "success"
	SubtypeError         = "error"
	SubtypeRunCap        = "run_cap"
	SubtypeSchemaFailure = "schema_failure"
	SubtypeDenied        = "denied"
)

// Exit codes of a noninteractive run. 1 and 2 are the process's own usage and
// failure codes; 3 predates this contract (run caps); 4 and 5 are new.
const (
	ExitOK            = 0
	ExitError         = 1
	ExitUsage         = 2
	ExitRunCap        = 3
	ExitDenied        = 4
	ExitSchemaFailure = 5
)

// ExitCode maps a result subtype to the process exit code.
func ExitCode(subtype string) int {
	switch subtype {
	case SubtypeSuccess:
		return ExitOK
	case SubtypeRunCap:
		return ExitRunCap
	case SubtypeDenied:
		return ExitDenied
	case SubtypeSchemaFailure:
		return ExitSchemaFailure
	}
	return ExitError
}

// Usage is the run's token usage, from the same summary -usage-json writes.
type Usage struct {
	Calls            int `json:"calls"`
	TotalTokens      int `json:"total_tokens"`
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// Final is the result: the last line of stream-json, and the whole of json.
// Reply and Structured are model text: a consumer treats them as untrusted
// data (docs/headless.md).
type Final struct {
	Type         string          `json:"type"`
	Subtype      string          `json:"subtype"`
	IsError      bool            `json:"is_error"`
	SessionID    string          `json:"session_id,omitempty"`
	Mode         string          `json:"mode,omitempty"`
	StopReason   string          `json:"stop_reason,omitempty"`
	Passes       int             `json:"passes,omitempty"`
	Reply        string          `json:"reply"`
	Structured   json.RawMessage `json:"structured"`
	SchemaError  *SchemaError    `json:"schema_error,omitempty"`
	AsksWithheld []string        `json:"asks_withheld,omitempty"`
	Usage        *Usage          `json:"usage,omitempty"`
	DurationMS   int64           `json:"duration_ms"`
	// Error is harness text: a cleaned, capped one-line reason.
	Error string `json:"error,omitempty"`
}

// EmitterOptions tunes an Emitter.
type EmitterOptions struct {
	// TextCap is the most bytes of text one event carries; zero takes the
	// default. Text longer than the cap is split into several text events and
	// a longer tool result is cut with truncated set.
	TextCap int
	// Args adds a tool call's arguments to tool_start (the -verbose flag).
	// They are model text, sanitised and capped like a result.
	Args bool
}

// Emitter writes a run's protocol to one writer. Stdout carries only the
// protocol: the caller sends human lines to stderr. It is safe for
// concurrent use.
type Emitter struct {
	format Format
	opts   EmitterOptions

	mu   sync.Mutex
	w    io.Writer
	enc  *json.Encoder
	buf  strings.Builder // text not yet emitted: whole lines only go out
	werr error
}

// NewEmitter returns an Emitter for format. Text keeps the original contract:
// events are ignored and the result is the reply and a newline.
func NewEmitter(format Format, w io.Writer, opts EmitterOptions) *Emitter {
	if opts.TextCap <= 0 {
		opts.TextCap = config.DefaultHeadlessEventTextCap
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &Emitter{format: format, opts: opts, w: w, enc: enc}
}

// Format is the emitter's format.
func (e *Emitter) Format() Format { return e.format }

// clean is the sink for any string that came from a model or a tool: control,
// bidi and delimiter runes out, then a vault value replaced.
func clean(s string) string {
	return vaultenv.Default.Scrub(sanitize.Text(s))
}

// ident is the sink for a name or id that must be safe anywhere.
func ident(s string) string { return sanitize.Ident(s, 128) }

// cutBytes cuts s to at most n bytes on a rune boundary.
func cutBytes(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// splitBytes splits s into pieces of at most n bytes, each cut on a rune
// boundary.
func splitBytes(s string, n int) []string {
	var out []string
	for len(s) > n {
		head, _ := cutBytes(s, n)
		if head == "" {
			// A cap smaller than one rune: take the rune whole.
			_, size := utf8.DecodeRuneInString(s)
			head = s[:size]
		}
		out = append(out, head)
		s = s[len(head):]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

func (e *Emitter) write(v any) {
	if e.werr != nil {
		return
	}
	e.werr = e.enc.Encode(v)
}

// Init announces the session, first on a stream-json run.
func (e *Emitter) Init(sessionID, provider, model string) {
	if e.format != FormatStreamJSON {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.write(struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		Provider  string `json:"provider"`
		Model     string `json:"model"`
	}{TypeInit, ident(sessionID), sanitize.Line(provider, 128), sanitize.Line(model, 128)})
}

type textEvent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// flushText emits s as text events of at most the cap, cleaned. The caller
// holds the lock.
func (e *Emitter) flushText(s string) {
	s = clean(s)
	for _, piece := range splitBytes(s, e.opts.TextCap) {
		e.write(textEvent{TypeText, piece})
	}
}

// flushAll emits everything buffered. The caller holds the lock.
func (e *Emitter) flushAll() {
	if e.buf.Len() == 0 {
		return
	}
	s := e.buf.String()
	e.buf.Reset()
	e.flushText(s)
}

// Observe maps one agent event to protocol lines. The mapping is explicit:
// a kind not named here is dropped, which is how reasoning, permission asks
// and clarify asks stay out. Text goes out in whole lines so the cleaning
// sees a delimiter that a delta boundary would otherwise split.
func (e *Emitter) Observe(ev agent.Event) {
	if e.format != FormatStreamJSON {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	switch ev.Kind {
	case agent.EventTextKind:
		e.buf.WriteString(ev.Text)
		s := e.buf.String()
		if i := strings.LastIndexByte(s, '\n'); i >= 0 {
			e.buf.Reset()
			e.buf.WriteString(s[i+1:])
			e.flushText(s[:i+1])
		} else if len(s) >= e.opts.TextCap {
			e.flushAll()
		}
	case agent.EventToolStartKind:
		if ev.Tool == nil {
			return
		}
		e.flushAll()
		out := struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
			Args string `json:"args,omitempty"`
			Trun bool   `json:"truncated,omitempty"`
		}{Type: TypeToolStart, ID: ident(ev.Tool.ID), Name: ident(ev.Tool.Name)}
		if e.opts.Args {
			args := ev.Tool.RawArgs
			if args == "" && len(ev.Tool.Args) > 0 {
				if b, err := json.Marshal(ev.Tool.Args); err == nil {
					args = string(b)
				}
			}
			out.Args, out.Trun = cutBytes(clean(args), e.opts.TextCap)
		}
		e.write(out)
	case agent.EventToolResultKind:
		e.flushAll()
		text, trun := cutBytes(clean(ev.ToolResult), e.opts.TextCap)
		e.write(struct {
			Type string `json:"type"`
			ID   string `json:"id,omitempty"`
			Name string `json:"name"`
			Text string `json:"text"`
			Trun bool   `json:"truncated,omitempty"`
		}{TypeToolResult, ident(ev.ToolCallID), ident(ev.ToolName), text, trun})
	case agent.EventWarningKind:
		if ev.Warning == "" {
			return
		}
		e.flushAll()
		text, _ := cutBytes(sanitize.Line(clean(ev.Warning), 0), e.opts.TextCap)
		e.write(textEvent{TypeWarning, text})
	}
}

// Result ends the run. Text prints the reply; json and stream-json print the
// result object, last on its stream. It returns the first write error.
func (e *Emitter) Result(f Final) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.flushAll()
	f.Type = TypeResult
	f.Reply = clean(f.Reply)
	f.SessionID = ident(f.SessionID)
	f.StopReason = ident(f.StopReason)
	f.Mode = ident(f.Mode)
	f.Error = sanitize.Line(clean(f.Error), 400)
	if len(f.Structured) == 0 {
		f.Structured = json.RawMessage("null")
	}
	if f.AsksWithheld != nil {
		names := make([]string, 0, len(f.AsksWithheld))
		for _, n := range f.AsksWithheld {
			names = append(names, ident(n))
		}
		f.AsksWithheld = names
	}
	switch e.format {
	case FormatText:
		if e.werr == nil {
			_, e.werr = fmt.Fprintln(e.w, f.Reply)
		}
	default:
		e.write(f)
	}
	return e.werr
}
