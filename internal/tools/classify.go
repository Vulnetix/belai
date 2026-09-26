package tools

// This file holds the rule that decides which tool results are sent to the
// security classifier before they may be promoted into the conversation.
//
// Sanitising is not part of this decision. Every tool result, without
// exception, has its harness delimiter markup and nonce/integrity attributes
// stripped and is then egress-verified. What this rule decides is only
// whether a model round trip happens on top of that.

// classifierKinds is the closed set of result kinds that go to the
// classifier. The line is drawn at whether the *content* is arbitrary, not at
// whether the call was well formed:
//
//   - Bash runs an arbitrary command string. Neither what runs nor what comes
//     back is constrained by the harness.
//   - WebFetch and WebSearch return text written by someone off this machine
//     with no relationship to the task — the shape a prompt injection takes.
//   - Read returns whatever is in a file. The call is confined, but the bytes
//     are not: a repository can carry a poisoned file exactly as a web page
//     can carry a poisoned paragraph.
//   - KindRemote natives run a shaped argv, but the bytes they bring back are
//     written by a third party on a hosting platform (PR bodies, issue
//     comments, file contents). They carry the same prompt-injection risk as
//     a web fetch, so they classify too.
//   - KindAgentStore reads other agents' transcript and memory stores. The
//     call is confined to the static registry, but the bytes are written by
//     other models — the textbook prompt-injection carrier — so they classify
//     too.
//   - KindSubagent is the result of the Task tool, a report written by a
//     read-only subagent. Because the report is model-written arbitrary text,
//     it classifies before promotion.
//
// Every other kind is both shaped and controlled: Grep returns matching lines
// for a pattern the harness passed as one argument, Glob returns paths, Write
// and Edit return a terse confirmation the harness wrote itself (optionally
// followed by a sealed diagnostics block from a language server), and a local
// native runs a fixed argv the harness built. Those are sanitised and
// promoted.
//
// A kind absent from this map is sanitise-only, which is the cheap default,
// so adding a tool whose content is arbitrary means adding its kind here
// deliberately.
var classifierKinds = map[Kind]bool{
	KindBash:       true,
	KindWebFetch:   true,
	KindWebSearch:  true,
	KindRead:       true,
	KindRemote:     true,
	KindProcess:    true,
	KindAgentStore: true,
	KindSubagent:   true,
	KindHook:       true,
}

// NeedsClassifier reports whether a result of this kind must go through the
// security classifier before promotion.
func (k Kind) NeedsClassifier() bool {
	return classifierKinds[k]
}
