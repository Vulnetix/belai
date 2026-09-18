package tools

import "context"

// Kind identifies a tool.
type Kind string

const (
	KindRead      Kind = "read"
	KindWebSearch Kind = "web_search"
	KindWebFetch  Kind = "web_fetch"
	KindBash      Kind = "bash"
	KindGrep      Kind = "grep"
	KindGlob      Kind = "glob"
	KindExplore   Kind = "explore"
	KindWrite     Kind = "write"
	KindEdit      Kind = "edit"
	// KindNative identifies the first-class read-only command tools from the
	// native catalogue (Cat, Find, Git, JQ, cloud CLIs, …). Every native tool
	// shells out to a fixed command with a fixed argument shape, so the kind is
	// read-only by construction.
	KindNative Kind = "native"
)

// AllKinds is every registered Kind, in declaration order. Tests iterate it to
// pin that the read-only classification can never silently admit a new
// mutating kind into the concurrent read-only fan-out.
var AllKinds = []Kind{
	KindRead, KindWebSearch, KindWebFetch, KindBash, KindGrep, KindGlob,
	KindExplore, KindWrite, KindEdit, KindNative,
}

// readOnlyKinds is the closed allowlist of kinds that only read. A Kind absent
// from this map is mutating: that is the fail-closed default, so adding a new
// kind without registering it here makes it run on the sequential path rather
// than racing the concurrent read-only fan-out.
var readOnlyKinds = map[Kind]bool{
	KindRead:      true,
	KindWebSearch: true,
	KindWebFetch:  true,
	KindGrep:      true,
	KindGlob:      true,
	KindExplore:   true,
	KindNative:    true,
}

// ReadOnly reports whether a tool of this kind only reads and never mutates
// the workspace. It gates the concurrent read-only tool run in the agent loop.
func (k Kind) ReadOnly() bool {
	return readOnlyKinds[k]
}

// classifierKinds is the closed set of result kinds that are sent to the
// security classifier before they may be promoted into the conversation.
//
// Only Bash is on it. Every other builtin tool has a fixed argument shape and
// a fixed output shape: a path that is confined and sanitised, a pattern, a
// URL, a filter. What the harness gets back is the output of a command the
// harness itself constructed, so the call cannot be steered into producing
// something other than what it names. Bash is the exception, and the only
// one: its argument is an arbitrary command string, so neither what runs nor
// what comes back is constrained by the harness at all.
//
// Sanitising still happens for every kind — delimiter markup is stripped from
// every tool result, so nothing a tool returns can forge a harness block.
// What a non-Bash kind skips is the model round trip, not the scrubbing.
//
// The trade this makes, stated plainly: a file or a web page whose text tries
// to instruct the model now reaches the model with only the delimiter
// stripping in front of it. The sealed <tools> block tells the model that
// tool results are data rather than instructions, and that is what stands in
// for the classifier on those paths.
var classifierKinds = map[Kind]bool{
	KindBash: true,
}

// NeedsClassifier reports whether a result of this kind must go through the
// classifier before promotion. A kind absent from the set is sanitised only.
func (k Kind) NeedsClassifier() bool {
	return classifierKinds[k]
}

// Result is a tool output.
type Result struct {
	Kind    Kind
	Content string
	Meta    map[string]any
}

// Progress is a chunk of tool output reported while the tool is still running.
// Text holds one or more whole lines with no trailing newline: partial lines
// are held back until they complete, so a consumer never has to reassemble
// them. Stream names the origin for display only — ordering is already correct
// and the two streams are interleaved as the process wrote them.
//
// Progress is render-only. It never reaches a model, and nothing downstream
// may treat it as tool output: the authoritative result is the Result the tool
// eventually returns.
type Progress struct {
	Stream string // "stdout" or "stderr"
	Text   string
}

// Sink receives Progress as it is produced. It may be called from a goroutine
// other than the one that called ExecuteStream, and it may block — blocking is
// how backpressure reaches the subprocess.
type Sink func(Progress)

// StreamingTool is a Tool that can report its output as it is produced rather
// than only when it finishes.
//
// Execute must remain equivalent to ExecuteStream with a nil sink, so a caller
// that does not care about progress can ignore this interface entirely.
type StreamingTool interface {
	Tool
	ExecuteStream(ctx context.Context, args map[string]any, sink Sink) (Result, error)
}

// ReadResult constructs an untrusted Read tool result.
func ReadResult(content string) Result {
	return Result{Kind: KindRead, Content: content}
}

// ReadResultMeta constructs a Read tool result with attached metadata.
func ReadResultMeta(content string, meta map[string]any) Result {
	return Result{Kind: KindRead, Content: content, Meta: meta}
}

// WriteResult constructs a Write tool result.
func WriteResult(content string) Result {
	return Result{Kind: KindWrite, Content: content}
}

// EditResult constructs an Edit tool result.
func EditResult(content string) Result {
	return Result{Kind: KindEdit, Content: content}
}

// WebFetchResult constructs an untrusted WebFetch tool result.
func WebFetchResult(content string) Result {
	return Result{Kind: KindWebFetch, Content: content}
}

// WebSearchResult constructs an untrusted WebSearch tool result.
func WebSearchResult(content string) Result {
	return Result{Kind: KindWebSearch, Content: content}
}
