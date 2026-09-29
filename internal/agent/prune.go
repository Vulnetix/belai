package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/transcript"
)

// Compaction by pruning keeps what the work ahead needs and removes the rest,
// leaving every kept word exactly as it was. A summary rewrites the whole
// conversation in one long model call and is a new piece of text to admit;
// pruning asks a decision backend two yes/no questions per tool call and only
// ever deletes or truncates, so nothing new needs classifying. compactTurns
// tries it first and falls back to the summary when the job is off, the
// backend cannot answer, or the pruned context is still over the trigger.
const (
	// pruneKeepRecent: the newest turns are never touched.
	pruneKeepRecent = 6
	// pruneHeadChars: how much of a result survives when only its call is kept.
	pruneHeadChars = 300
	// pruneTruncateSlack: a result is truncated only if longer than the head by
	// this much, so the note never makes it longer.
	pruneTruncateSlack = 120
	// pruneGoalTurns: how many recent user requests state the goal.
	pruneGoalTurns = 3
	// State budgets in bytes: the local model reads a small state; a remote
	// backend a larger one.
	pruneStateBytesLocal  = 7_000
	pruneStateBytesRemote = 24_000
)

// pruneInputCaps are the successive limits on a call's input in the digest.
var pruneInputCaps = []int{1000, 200, 60}

// pruneNote follows a truncated result.
const pruneNote = "\n[belai pruned %d characters of this tool result to keep the context small; run the tool again if you need them]"

// prunePair is one tool call and its result.
type prunePair struct {
	id       string // short id shown to the backend: t1, t2, ...
	callTurn int    // index of the assistant turn holding the call
	callID   string
	tool     string
	input    string
	resTurn  int
	chars    int
	pinned   bool
}

// pruneStats says what a prune did.
type pruneStats struct {
	pairs, candidates         int
	kept, truncated, dropped  int
	unknown                   int
	tokensBefore, tokensAfter int
	requests                  int
	identity                  string
}

// alwaysKeepTools are tools whose calls and results are state or instructions
// rather than re-runnable reads.
var alwaysKeepTools = map[string]bool{
	"update_plan": true, "ExitPlanMode": true, "AskUserQuestion": true, "Skill": true,
	"SkillDraft": true, "Task": true, "ToolSearch": true, "ReadResult": true,
}

// pruneTurns removes tool calls and results the work ahead does not need. ok
// is false, and turns are unchanged, when the job is off or unavailable, there
// is nothing to judge, or the pruned context would still be over the trigger.
func (s *Session) pruneTurns(ctx context.Context, turns []run.Turn) ([]run.Turn, pruneStats, bool) {
	var st pruneStats
	if s.jev == nil || !s.jev.Enabled(config.JevPruneCompaction) || len(turns) <= pruneKeepRecent+1 {
		return nil, st, false
	}
	pairs := s.collectPairs(turns)
	st.pairs = len(pairs)
	var items []jev.PruneItem
	for _, p := range pairs {
		if p.pinned {
			continue
		}
		st.candidates++
		items = append(items,
			jev.PruneItem{ID: "c:" + p.id, Statement: callStatement(p)},
			jev.PruneItem{ID: "r:" + p.id, Statement: resultStatement(p)},
		)
	}
	if st.candidates == 0 {
		return nil, st, false
	}
	st.tokensBefore = estimateTokens(turns)

	state, goal, ok := pruneState(turns, pairs, s.pruneBudget())
	if !ok {
		return nil, st, false
	}
	scores, res, err := s.jev.PrunePairs(ctx, state, goal, items)
	st.requests, st.identity = res.Requests, res.Identity
	if err != nil {
		return nil, st, false
	}
	st.unknown = len(res.Unknown)

	out, dropped, truncated := applyPrune(turns, pairs, scores)
	st.dropped, st.truncated = dropped, truncated
	st.kept = st.candidates - dropped - truncated
	if dropped+truncated == 0 {
		return nil, st, false
	}
	st.tokensAfter = estimateTokens(out)
	// The prune must bring the context under the trigger, or the summary runs
	// on the original turns instead.
	if st.tokensAfter*100 >= s.compactWindow()*compactThresholdPct {
		return nil, st, false
	}
	return out, st, true
}

// pruneBudget is the byte budget for the state shown to the backend.
func (s *Session) pruneBudget() int {
	if s.jev != nil && s.jev.Client != nil && s.jev.Client.Local() {
		return pruneStateBytesLocal
	}
	return pruneStateBytesRemote
}

func estimateTokens(turns []run.Turn) int {
	msgs := make([]transcript.Message, 0, len(turns))
	for _, t := range turns {
		msgs = append(msgs, transcript.Message{Role: t.Role, Content: t.Content, ToolName: t.ToolName})
	}
	return transcript.EstimateContext(msgs).Tokens
}

func callStatement(p prunePair) string {
	return fmt.Sprintf("Tool call %s (%s) should stay in the history: knowing this call was made, with its input, still matters for what the assistant does next.", p.id, p.tool)
}

func resultStatement(p prunePair) string {
	return fmt.Sprintf("The full output of tool call %s (%s, %d chars) should stay in the history verbatim: the assistant still needs its contents, and running the tool again would not help.", p.id, p.tool, p.chars)
}

// collectPairs pairs each tool call with its result by call id, in
// conversation order, and decides which are pinned. A call with no result, or
// a result with no call, is left alone: providers require the pairs to match
// and pruning must not create a mismatch that was not already there.
func (s *Session) collectPairs(turns []run.Turn) []prunePair {
	results := map[string]int{} // call id -> tool turn index
	for i, t := range turns {
		if t.Role == "tool" && t.ToolCallID != "" {
			if _, dup := results[t.ToolCallID]; !dup {
				results[t.ToolCallID] = i
			}
		}
	}
	firstUser := -1
	for i, t := range turns {
		if t.Role == "user" {
			firstUser = i
			break
		}
	}
	recentFrom := len(turns) - pruneKeepRecent
	var pairs []prunePair
	seen := map[string]bool{}
	n := 0
	for ti, t := range turns {
		if t.Role != "assistant" {
			continue
		}
		for _, c := range t.ToolCalls {
			ri, ok := results[c.ID]
			if !ok || seen[c.ID] || ri <= ti {
				continue
			}
			seen[c.ID] = true
			n++
			res := turns[ri]
			p := prunePair{
				id: fmt.Sprintf("t%d", n), callTurn: ti, callID: c.ID, tool: c.Name,
				input: argSummary(c), resTurn: ri, chars: len(res.Content),
			}
			p.pinned = ti <= firstUser || ti >= recentFrom || ri >= recentFrom ||
				len(t.Thinking) > 0 || s.mustKeep(c.Name, res.Content)
			pairs = append(pairs, p)
		}
	}
	return pairs
}

// mustKeep reports a pair that is never judged: state, instructions, a result
// that changed something, or one that failed (a failure is evidence).
func (s *Session) mustKeep(name, result string) bool {
	if alwaysKeepTools[name] || strings.HasPrefix(name, "Kanban") || strings.HasPrefix(name, "mcp__") {
		return true
	}
	if s.registry != nil {
		if t, ok := s.findCallable(name); ok && tools.Mutates(t) {
			return true
		}
	}
	return isFailure(result)
}

// isFailure recognises a result the harness withheld or rejected, or a command
// that exited non-zero.
func isFailure(result string) bool {
	return strings.HasPrefix(result, "tool result withheld:") ||
		strings.HasPrefix(result, "tool call rejected:") ||
		strings.Contains(result, "\nexit status ")
}

// argSummary renders a call's arguments as key=value pairs on one line, in
// key order, each value cleaned and bounded.
func argSummary(c rolemanager.ToolCall) string {
	if len(c.Args) == 0 {
		return sanitize.Line(c.RawArgs, 1000)
	}
	keys := make([]string, 0, len(c.Args))
	for k := range c.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, sanitize.Ident(k, 32)+"="+sanitize.Line(fmt.Sprint(c.Args[k]), 200))
	}
	return sanitize.Line(strings.Join(parts, " "), 1000)
}

// stateEntry is one message of the digest shown to the backend.
type stateEntry struct {
	I     int         `json:"i"`
	Role  string      `json:"role"`
	Text  string      `json:"text,omitempty"`
	Calls []stateCall `json:"tool_calls,omitempty"`
	// Lines replaces Calls once the calls are shrunk to single lines.
	Lines []string `json:"calls,omitempty"`

	pinned bool
	pairs  []prunePair
}

type stateCall struct {
	ID     string `json:"id"`
	Tool   string `json:"tool"`
	Input  string `json:"input,omitempty"`
	Result string `json:"result"`
}

// pruneState renders the conversation for the backend and fits it to budget
// bytes. Tool results appear only as a size, never as text; message text is
// shown in full at first and shrunk stage by stage, oldest first, never
// touching the first request or the newest turns: the inputs shorten, long
// messages are abridged, then collapsed, then calls become one line each, then
// old messages without calls are left out. ok is false if it cannot fit.
func pruneState(turns []run.Turn, pairs []prunePair, budget int) (state, goal sanitize.DecisionText, ok bool) {
	byCall := map[int][]prunePair{}
	for _, p := range pairs {
		byCall[p.callTurn] = append(byCall[p.callTurn], p)
	}
	firstUser := -1
	for i, t := range turns {
		if t.Role == "user" {
			firstUser = i
			break
		}
	}
	recentFrom := len(turns) - pruneKeepRecent
	var entries []stateEntry
	for i, t := range turns {
		switch t.Role {
		case "user", "assistant":
		default:
			continue
		}
		e := stateEntry{I: i, Role: t.Role, Text: sanitize.Line(t.Content, 2000), pinned: i <= firstUser || i >= recentFrom, pairs: byCall[i]}
		for _, p := range e.pairs {
			e.Calls = append(e.Calls, stateCall{ID: p.id, Tool: p.tool, Result: resultNote(turns[p.resTurn].Content)})
		}
		if e.Text == "" && len(e.Calls) == 0 {
			continue
		}
		entries = append(entries, e)
	}
	setInputs := func(cap int) {
		for i := range entries {
			for j := range entries[i].Calls {
				entries[i].Calls[j].Input = clip(entries[i].pairs[j].input, cap)
			}
		}
	}
	size := func() int {
		b, _ := json.Marshal(entries)
		return len(b)
	}

	fits := false
	for _, cap := range pruneInputCaps {
		setInputs(cap)
		if size() <= budget {
			fits = true
			break
		}
	}
	if !fits {
		// Abridge long text, oldest first.
		for i := range entries {
			if entries[i].pinned || utf8.RuneCountInString(entries[i].Text) <= 590 {
				continue
			}
			entries[i].Text = abridge(entries[i].Text)
			if fits = size() <= budget; fits {
				break
			}
		}
	}
	if !fits {
		// Collapse old messages to their length.
		for i := range entries {
			if entries[i].pinned || entries[i].Text == "" {
				continue
			}
			entries[i].Text = fmt.Sprintf("[… %d chars omitted …]", utf8.RuneCountInString(entries[i].Text))
			if fits = size() <= budget; fits {
				break
			}
		}
	}
	if !fits {
		// Old calls become one line each.
		for i := range entries {
			if entries[i].pinned || len(entries[i].Calls) == 0 {
				continue
			}
			for j, c := range entries[i].Calls {
				entries[i].Lines = append(entries[i].Lines, fmt.Sprintf("%s %s %s → %s", c.ID, c.Tool, clip(entries[i].pairs[j].input, 60), c.Result))
			}
			entries[i].Calls = nil
			if fits = size() <= budget; fits {
				break
			}
		}
	}
	if !fits {
		// Leave out old messages that hold no calls, oldest first.
		for i := 0; i < len(entries) && !fits; {
			if e := entries[i]; !e.pinned && len(e.Calls) == 0 && len(e.Lines) == 0 {
				entries = append(entries[:i], entries[i+1:]...)
				fits = size() <= budget
				continue
			}
			i++
		}
	}
	if !fits {
		fits = size() <= budget
	}
	if !fits {
		return sanitize.DecisionText{}, sanitize.DecisionText{}, false
	}
	b, _ := json.Marshal(entries)

	var recent []string
	for i := len(turns) - 1; i >= 0 && len(recent) < pruneGoalTurns; i-- {
		if turns[i].Role == "user" && strings.TrimSpace(turns[i].Content) != "" {
			recent = append([]string{sanitize.Line(turns[i].Content, 500)}, recent...)
		}
	}
	return sanitize.ForDecision(string(b), 0), sanitize.ForDecision(strings.Join(recent, "\n"), 1600), true
}

// resultNote is what the backend sees of a result: its kind and size.
func resultNote(content string) string {
	switch {
	case content == run.ClearedToolResult:
		return "cleared"
	case isFailure(content):
		return fmt.Sprintf("error, %d chars (omitted)", len(content))
	}
	return fmt.Sprintf("ok, %d chars (omitted)", len(content))
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// abridge keeps the start and end of a long message.
func abridge(s string) string {
	r := []rune(s)
	if len(r) <= 590 {
		return s
	}
	return string(r[:400]) + fmt.Sprintf("\n[… %d chars omitted …]\n", len(r)-550) + string(r[len(r)-150:])
}

// applyPrune applies the backend's scores. Per candidate pair: a result scoring
// at least KeepAt stays whole (and so does its call); otherwise a call scoring
// at least KeepAt stays with its result cut to a head and a note; otherwise
// both go. An unanswered question keeps. A message left with no text and no
// calls is removed. Returns the new turns and how many pairs were dropped and
// truncated.
func applyPrune(turns []run.Turn, pairs []prunePair, scores map[string]float64) (out []run.Turn, dropped, truncated int) {
	dropCall := map[string]bool{}  // call id -> drop the call
	dropTool := map[int]bool{}     // tool turn -> drop
	newContent := map[int]string{} // tool turn -> replacement content
	for _, p := range pairs {
		if p.pinned {
			continue
		}
		keepResult, okR := scores["r:"+p.id]
		keepCall, okC := scores["c:"+p.id]
		if !okR {
			keepResult = 1
		}
		if !okC {
			keepCall = 1
		}
		switch {
		case keepResult >= jev.KeepAt:
		case keepCall >= jev.KeepAt:
			content := turns[p.resTurn].Content
			if len(content) > pruneHeadChars+pruneTruncateSlack && !strings.Contains(content, "ReadResult") {
				cut := pruneHeadChars
				for cut > 0 && !utf8.RuneStart(content[cut]) {
					cut--
				}
				newContent[p.resTurn] = content[:cut] + fmt.Sprintf(pruneNote, len(content)-cut)
				truncated++
			}
		default:
			dropCall[p.callID] = true
			dropTool[p.resTurn] = true
			dropped++
		}
	}
	out = make([]run.Turn, 0, len(turns))
	for i, t := range turns {
		switch t.Role {
		case "assistant":
			if len(t.ToolCalls) == 0 {
				out = append(out, t)
				continue
			}
			var calls []rolemanager.ToolCall
			for _, c := range t.ToolCalls {
				if !dropCall[c.ID] {
					calls = append(calls, c)
				}
			}
			if len(calls) == len(t.ToolCalls) {
				out = append(out, t)
				continue
			}
			if len(calls) == 0 && strings.TrimSpace(t.Content) == "" && len(t.Thinking) == 0 {
				continue
			}
			// A new value, not a copy: the egress memo on the old one must not
			// carry over to a turn with different calls.
			out = append(out, run.Turn{
				Role: t.Role, Content: t.Content, ToolCalls: calls, Attachments: t.Attachments,
				Directive: t.Directive, Thinking: t.Thinking, ThinkingModel: t.ThinkingModel,
			})
		case "tool":
			if dropTool[i] {
				continue
			}
			if c, ok := newContent[i]; ok {
				out = append(out, run.Turn{Role: t.Role, Content: c, ToolCallID: t.ToolCallID, ToolName: t.ToolName})
				continue
			}
			out = append(out, t)
		default:
			out = append(out, t)
		}
	}
	return out, dropped, truncated
}
