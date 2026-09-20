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
	// KindUpdatePlan is the shaped, harness-composed result of the update_plan
	// checklist-progress tool. It is read-only and sanitise-only.
	KindUpdatePlan Kind = "update_plan"
	// KindNative identifies the first-class read-only command tools from the
	// local catalogue (Cat, Find, Git, JQ, …). Every native tool shells out
	// to a fixed command with a fixed argument shape, so the kind is read-only
	// by construction.
	KindNative Kind = "native"
	// KindRemote identifies first-class read-only tools whose output is text
	// written off this machine (GH, Glab). It is read-only like KindNative but
	// its results are arbitrary third-party content, so they are classified
	// before promotion.
	KindRemote Kind = "remote"
	// KindProcess is a supervised process's own arbitrary stdout/stderr,
	// read back by SubAgentLog. It classifies because the bytes are exactly as
	// unconstrained as KindBash.
	KindProcess Kind = "process"
	// KindProcessCtl is the harness-composed confirmation from ProcessRestart.
	// It is mutating and sanitise-only.
	KindProcessCtl Kind = "process_ctl"
	// KindAgentStore identifies the read-only SearchSessions, ReadSession and
	// SearchMemory tools. Their content is other agents' transcript and memory
	// text — arbitrary content written by other models — so the kind is
	// read-only but classifies before promotion.
	KindAgentStore Kind = "agent_store"
)

// AllKinds is every registered Kind, in declaration order. Tests iterate it to
// pin that the read-only classification can never silently admit a new
// mutating kind into the concurrent read-only fan-out.
var AllKinds = []Kind{
	KindRead, KindWebSearch, KindWebFetch, KindBash, KindGrep, KindGlob,
	KindExplore, KindWrite, KindEdit, KindNative, KindRemote, KindUpdatePlan,
	KindProcess, KindProcessCtl, KindAgentStore,
}

// readOnlyKinds is the closed allowlist of kinds that only read. A Kind absent
// from this map is mutating: that is the fail-closed default, so adding a new
// kind without registering it here makes it run on the sequential path rather
// than racing the concurrent read-only fan-out.
var readOnlyKinds = map[Kind]bool{
	KindRead:       true,
	KindWebSearch:  true,
	KindWebFetch:   true,
	KindGrep:       true,
	KindGlob:       true,
	KindExplore:    true,
	KindNative:     true,
	KindRemote:     true,
	KindUpdatePlan: true,
	KindProcess:    true,
	KindAgentStore: true,
}

// ReadOnly reports whether a tool of this kind only reads and never mutates
// the workspace. It gates the concurrent read-only tool run in the agent loop.
func (k Kind) ReadOnly() bool {
	return readOnlyKinds[k]
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
