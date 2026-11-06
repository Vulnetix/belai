package headless

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/session"
)

// Resumed is a stored session loaded for a noninteractive run: the
// conversation to replay and the writer that continues the same transcript.
type Resumed struct {
	// Key and ID name the transcript the run appends to. For a session of
	// another project they are the fork this resume made in the current one.
	Key session.Key
	ID  string
	// History is the conversation as provider turns.
	History []run.Turn
	// Writer continues the transcript.
	Writer *session.Writer
	// Forked is the id of the session a cross-project resume copied, or "".
	Forked string
}

// LatestInProject returns the newest session of the project key cur: the
// -continue entry point.
func LatestInProject(store *session.Store, cur session.Key) (session.Key, string, error) {
	groups, err := store.AllSessions(cur)
	if err != nil {
		return "", "", err
	}
	for _, g := range groups {
		if g.Key != cur {
			continue
		}
		if len(g.Sessions) == 0 {
			break
		}
		return g.Key, g.Sessions[0].ID, nil
	}
	return "", "", errors.New("no sessions to continue for this project")
}

// ResolveResume finds the session named by idOrPrefix, preferring the current
// project and then every other, exactly as the interactive -resume does. An
// ambiguous or missing prefix is an error that names the candidates.
func ResolveResume(store *session.Store, cur session.Key, idOrPrefix string) (session.Key, string, error) {
	return store.ResolveAnywhere(cur, idOrPrefix)
}

// LoadResume opens the resolved session (key, id) for a run in the project
// cur. A session of another project is copied into cur under a new id and
// the run continues the copy, as the interactive resume does: the other
// project's directory is never entered, so a resume cannot widen the
// session's roots, and the trust gate that already admitted cur is the only
// one that applies. Stored history is replayed as it was admitted, not
// promoted again. With write false nothing is written: no copy, no writer.
func LoadResume(store *session.Store, cur, key session.Key, id string, write bool) (*Resumed, error) {
	entries, err := store.ReadFrom(key, id)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, errors.New("resume: the session has no entries")
	}
	r := &Resumed{Key: key, ID: id}
	r.History = TurnsFromEntries(session.ActivePath(entries))
	if !write {
		// -no-transcript: the history is read and nothing is written, so a
		// session of another project is not copied either.
		return r, nil
	}
	var originCwd string
	if m, ok := session.LatestMeta(entries); ok {
		originCwd = m.Cwd
	}
	if key != cur {
		newID, err := session.NewID()
		if err != nil {
			return nil, err
		}
		if err := store.ForkAcross(key, id, cur, newID); err != nil {
			return nil, fmt.Errorf("resume: %w", err)
		}
		r.Forked = id
		r.Key, r.ID = cur, newID
	}
	last := entries[len(entries)-1].ID
	r.Writer = session.ResumeWriter(store, r.Key, r.ID, last)
	if r.Forked != "" {
		r.Writer.Entry(session.Meta{ResumedFrom: r.Forked, OriginCwd: originCwd}.ToEntry(last))
	}
	return r, nil
}

func metaStr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// TurnsFromEntries rebuilds the provider conversation from a stored session.
// It keeps only what a model said and was told: user prompts, assistant
// replies and the tool calls that have a stored result, and those results. A
// call without a result and a result without a call are dropped, subagent
// rows and every render-only row are skipped, and a compaction summary stands
// in for what came before it. Adjacent user turns merge, as they do in the
// interactive resume.
func TurnsFromEntries(entries []session.Entry) []run.Turn {
	start := 0
	summary := ""
	for i, e := range entries {
		if e.Type == "summary" {
			start, summary = i+1, e.Content
		}
	}
	emit := entries[start:]

	results := map[string]int{}
	for i, e := range emit {
		if e.Type == "tool" && e.SubagentID == "" {
			if id := metaStr(e.Meta, "tool_call_id"); id != "" {
				results[id] = i
			}
		}
	}

	var turns []run.Turn
	if summary != "" {
		turns = append(turns,
			run.Turn{Role: "user", Content: rolemanager.SummaryPrefix + summary + rolemanager.SummarySuffix},
			run.Turn{Role: "assistant", Content: rolemanager.SummaryAck})
	}
	live := map[string]bool{}
	emitted := map[string]bool{}
	for i, e := range emit {
		if e.SubagentID != "" {
			continue
		}
		switch e.Type {
		case "user":
			turns = append(turns, run.Turn{Role: "user", Content: e.Content})
		case "assistant":
			turn := run.Turn{Role: "assistant", Content: e.Content}
			raw, _ := e.Meta["tool_calls"].([]any)
			for _, r := range raw {
				m, ok := r.(map[string]any)
				if !ok {
					continue
				}
				id := metaStr(m, "id")
				if id == "" {
					continue
				}
				if idx, ok := results[id]; !ok || idx <= i {
					continue
				}
				args := map[string]any{}
				if a := metaStr(m, "args"); a != "" {
					_ = json.Unmarshal([]byte(a), &args)
				}
				live[id] = true
				turn.ToolCalls = append(turn.ToolCalls, rolemanager.ToolCall{ID: id, Name: metaStr(m, "name"), Args: args})
			}
			if strings.TrimSpace(turn.Content) == "" && len(turn.ToolCalls) == 0 {
				continue
			}
			turns = append(turns, turn)
		case "tool":
			id := metaStr(e.Meta, "tool_call_id")
			if id == "" || !live[id] || emitted[id] {
				continue
			}
			emitted[id] = true
			turns = append(turns, run.Turn{Role: "tool", Content: e.Content, ToolCallID: id, ToolName: metaStr(e.Meta, "tool_name")})
		}
	}
	return mergeUserTurns(turns)
}

// mergeUserTurns joins adjacent user turns, which Anthropic refuses, with a
// blank line.
func mergeUserTurns(in []run.Turn) []run.Turn {
	out := make([]run.Turn, 0, len(in))
	for _, t := range in {
		if n := len(out); n > 0 && t.Role == "user" && out[n-1].Role == "user" {
			prev := &out[n-1]
			if prev.Content != "" && t.Content != "" {
				prev.Content += "\n\n" + t.Content
			} else {
				prev.Content += t.Content
			}
			continue
		}
		out = append(out, t)
	}
	return out
}
