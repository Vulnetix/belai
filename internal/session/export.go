package session

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/transcript"
)

// ExportOptions configures ExportMarkdown.
type ExportOptions struct {
	// MaxToolResultRunes caps each tool result in the export. Zero means the
	// default (4000 runes).
	MaxToolResultRunes int
	// IncludeReasoning renders reasoning entries; the default omits them
	// because reasoning can contain chain-of-thought material the user did not
	// choose to share.
	IncludeReasoning bool
	// ID is the session id shown in the header. The entries alone do not carry
	// the session id (the on-disk file name does), so the caller supplies it.
	ID string
}

// defaultMaxExportToolResultRunes is the exported tool-result cap. It is
// larger than the persisted transcript cap so a session exported from disk can
// still be useful, but bounded so an enormous result cannot make the export
// unwieldy.
const defaultMaxExportToolResultRunes = 4000

// ExportMarkdown renders a session's persisted entries as deterministic,
// shareable Markdown. Only entry data is used: no clock, no randomness, no
// filesystem. The header carries the session identity and aggregate facts; the
// body replays user, assistant, tool and summary rows in append order. State
// entries (session_meta, goal_state, todo_list, plan_state), role-manager and
// system rows, and subagent rows are skipped, and reasoning is skipped unless
// opted in.
func ExportMarkdown(entries []Entry, opts ExportOptions) string {
	maxTool := opts.MaxToolResultRunes
	if maxTool <= 0 {
		maxTool = defaultMaxExportToolResultRunes
	}
	fence := exportFence(entries)

	var b strings.Builder
	writeExportHeader(&b, entries, opts)

	for _, e := range entries {
		if e.SubagentID != "" {
			continue
		}
		switch e.Type {
		case "user":
			b.WriteString("## User\n\n")
			b.WriteString(e.Content)
			b.WriteString("\n\n")
		case "assistant":
			b.WriteString("## Assistant\n\n")
			b.WriteString(e.Content)
			b.WriteString("\n\n")
			for _, call := range assistantToolCalls(e.Meta) {
				fmt.Fprintf(&b, "### Tool call: %s\n\n", call.Name)
				args := call.Args
				if args == "" {
					args = "{}"
				}
				b.WriteString(fence)
				b.WriteString("\n")
				b.WriteString(args)
				b.WriteString("\n")
				b.WriteString(fence)
				b.WriteString("\n\n")
			}
		case "tool":
			name := metaString(e.Meta, "tool_name")
			fmt.Fprintf(&b, "### Tool result: %s\n\n", name)
			b.WriteString(transcript.TruncateRunes(e.Content, maxTool))
			if metaBool(e.Meta, "truncated") {
				b.WriteString("\n\n*(tool result was truncated)*")
			}
			b.WriteString("\n\n")
		case "summary":
			b.WriteString("> ")
			b.WriteString(strings.ReplaceAll(e.Content, "\n", "\n> "))
			b.WriteString("\n\n")
		case "reasoning":
			if !opts.IncludeReasoning {
				continue
			}
			b.WriteString("## Reasoning\n\n")
			b.WriteString(e.Content)
			b.WriteString("\n\n")
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// writeExportHeader writes the deterministic header: name, id, cwd, created
// time, the provider/model sets seen on assistant entries, and the token total
// summed from assistant meta.total_tokens.
func writeExportHeader(b *strings.Builder, entries []Entry, opts ExportOptions) {
	meta, _ := LatestMeta(entries)

	b.WriteString("# Session export\n\n")
	fmt.Fprintf(b, "- name: %s\n", Name(entries))
	fmt.Fprintf(b, "- id: %s\n", opts.ID)
	fmt.Fprintf(b, "- cwd: %s\n", meta.Cwd)
	if meta.CreatedAt != 0 {
		fmt.Fprintf(b, "- created: %s\n", time.UnixMilli(meta.CreatedAt).UTC().Format(time.RFC3339))
	}

	providers := map[string]bool{}
	models := map[string]bool{}
	tokens := 0
	for _, e := range entries {
		if e.Type != "assistant" {
			continue
		}
		if p := metaString(e.Meta, "provider"); p != "" {
			providers[p] = true
		}
		if m := metaString(e.Meta, "model"); m != "" {
			models[m] = true
		}
		tokens += metaInt(e.Meta, "total_tokens")
	}
	fmt.Fprintf(b, "- providers: %s\n", strings.Join(sortedKeys(providers), ", "))
	fmt.Fprintf(b, "- models: %s\n", strings.Join(sortedKeys(models), ", "))
	fmt.Fprintf(b, "- tokens: %d\n", tokens)
	b.WriteString("\n")
}

// exportFence returns a backtick fence one longer than the longest run of
// backticks anywhere in the exported content, so repository text cannot break
// out of a fenced block. It is never shorter than three backticks.
func exportFence(entries []Entry) string {
	longest := 0
	note := func(s string) {
		if n := maxBacktickRun(s); n > longest {
			longest = n
		}
	}
	for _, e := range entries {
		note(e.Content)
		if e.Type == "tool" {
			note(metaString(e.Meta, "tool_name"))
		}
		for _, call := range assistantToolCalls(e.Meta) {
			note(call.Name)
			note(call.Args)
		}
	}
	if longest < 3 {
		longest = 3
	}
	return strings.Repeat("`", longest+1)
}

// maxBacktickRun returns the length of the longest consecutive run of
// backtick runes in s.
func maxBacktickRun(s string) int {
	best, cur := 0, 0
	for _, r := range s {
		if r == '`' {
			cur++
			if cur > best {
				best = cur
			}
		} else {
			cur = 0
		}
	}
	return best
}

// assistantToolCalls decodes the persisted tool_calls meta of an assistant
// entry. Malformed records are skipped rather than fatal, and a missing or
// non-string args value is rendered as "{}".
func assistantToolCalls(meta map[string]any) []toolCall {
	raw, ok := meta["tool_calls"].([]any)
	if !ok {
		return nil
	}
	out := make([]toolCall, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		args := ""
		switch v := m["args"].(type) {
		case string:
			args = v
		case nil:
			args = ""
		default:
			if data, err := json.Marshal(v); err == nil {
				args = string(data)
			}
		}
		out = append(out, toolCall{Name: name, Args: args})
	}
	return out
}

type toolCall struct {
	Name string
	Args string
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func metaString(meta map[string]any, key string) string {
	if v, ok := meta[key].(string); ok {
		return v
	}
	return ""
}

func metaBool(meta map[string]any, key string) bool {
	switch v := meta[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	}
	return false
}

func metaInt(meta map[string]any, key string) int {
	switch v := meta[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}
