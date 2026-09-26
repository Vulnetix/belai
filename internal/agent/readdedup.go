package agent

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/vulnetix/belai/internal/readindex"
	"github.com/vulnetix/belai/internal/run"
)

// Repeated-read answering. The session's readindex records every Read result
// it delivered; a later Read of the same unchanged file, whose earlier result
// is still in the conversation, is answered with a harness note pointing at
// that result instead of the same bytes again. The note is composed by the
// harness from paths, sizes and ids — never file content — so it is not a new
// trust question. A result cleared from context, a file changed on disk or by
// any tool, and a file whose read was withheld all go to disk as normal.

// readKey resolves a Read call's arguments to its index key, the way the Read
// tool itself would resolve them. ok is false for anything that is not a Read
// the index can answer.
func (s *Session) readKey(name string, args map[string]any) (readindex.Key, bool) {
	if name != "Read" || s.reads == nil {
		return readindex.Key{}, false
	}
	cwd := s.registry.Cwd()
	if cwd == nil {
		return readindex.Key{}, false
	}
	raw, _ := args["file_path"].(string)
	if raw == "" {
		return readindex.Key{}, false
	}
	abs, err := cwd.ResolveAbs(raw)
	if err != nil {
		return readindex.Key{}, false
	}
	if _, withheld := s.flagged.lookup(abs, "", ""); withheld {
		return readindex.Key{}, false
	}
	return readindex.Key{Path: abs, Offset: intArg(args["offset"]), Limit: intArg(args["limit"])}, true
}

// intArg reads a JSON number argument; absent or malformed is zero, which
// the Read tool treats as its default too.
func intArg(v any) int64 {
	switch n := v.(type) {
	case float64:
		if n > 0 {
			return int64(n)
		}
	case int:
		if n > 0 {
			return int64(n)
		}
	case int64:
		if n > 0 {
			return n
		}
	}
	return 0
}

// repeatedRead returns the harness note answering a Read whose content the
// model already has in the conversation, or "" when the read must run.
func (s *Session) repeatedRead(key readindex.Key, turns []run.Turn) string {
	e, ok := s.reads.Lookup(key, func(e readindex.Entry) bool { return liveResult(turns, e.ResultHash) })
	if !ok {
		return ""
	}
	if e.CallID == prefetchCallID {
		return fmt.Sprintf("[Read: %s is unchanged since the harness attached it to this turn's user message, and that attachment is still above. Use it instead of reading the file again. Any file changed since — by any tool or on disk — is always read fresh.]", e.Describe(s.readRoot()))
	}
	return fmt.Sprintf("[Read: %s is unchanged since you read it earlier in this conversation, and that earlier Read result is still above. Use it instead of reading the file again. Any file changed since — by any tool or on disk — is always read fresh.]", e.Describe(s.readRoot()))
}

// liveResult reports whether content with exactly this hash is still in the
// conversation: a tool result not cleared by context clearing and not
// compacted away, or a file attachment the harness prefetched onto a user
// turn. Matching the delivered bytes rather than the call id keeps a
// provider that reuses ids from vouching for a result that is gone.
func liveResult(turns []run.Turn, resultHash string) bool {
	if resultHash == "" {
		return false
	}
	for i := len(turns) - 1; i >= 0; i-- {
		t := turns[i]
		if t.Role == "tool" && t.Content != run.ClearedToolResult && readindex.HashResult(t.Content) == resultHash {
			return true
		}
		if t.Role == "user" {
			for _, a := range t.Attachments {
				if a.Kind == "file" && readindex.HashResult(a.Body) == resultHash {
					return true
				}
			}
		}
	}
	return false
}

// readTrailerRE matches the Read tool's paging trailer.
var readTrailerRE = regexp.MustCompile(`\[Read: lines (\d+)–(\d+) of (\d+);`)

// recordRead notes a delivered Read result in the index. A result with no
// paging trailer showed the whole file; one with a trailer showed the span it
// names. Withheld and past-the-end results are not recorded.
func (s *Session) recordRead(key readindex.Key, callID, result string) {
	if strings.HasPrefix(result, "tool result withheld") || strings.HasPrefix(result, "[Read: ") {
		return
	}
	whole, span := true, ""
	if m := readTrailerRE.FindStringSubmatch(result); m != nil {
		whole = m[1] == "1" && m[2] == m[3]
		span = fmt.Sprintf("lines %s–%s of %s", m[1], m[2], m[3])
	}
	s.reads.Record(key, callID, result, whole, span)
}

// readRoot is the root paths in the read index are shown relative to.
func (s *Session) readRoot() string {
	if cwd := s.registry.Cwd(); cwd != nil {
		return cwd.Root()
	}
	return s.workdir
}

// readSummary renders the read index as harness facts for the per-turn
// repository status: the files whose content is still in the conversation
// and unchanged on disk. Paths, extents, sizes and blob ids only — never
// contents — the same class of fact the repo map carries.
func (s *Session) readSummary(turns []run.Turn) string {
	lines := s.reads.Summary(s.readRoot(), maxPlanReadPaths, func(e readindex.Entry) bool {
		return e.Current() && liveResult(turns, e.ResultHash)
	})
	if len(lines) == 0 {
		return ""
	}
	return "Files already read and unchanged since, with their content still in the conversation above (harness-tracked; reading one again returns a pointer to the earlier result):\n- " + strings.Join(lines, "\n- ")
}
