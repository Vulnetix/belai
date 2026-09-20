package agentstore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/session"
)

// scanJSONL streams a JSONL file line by line, calling fn for each non-empty
// line. The buffer is 16 MiB, matching internal/session's store scanner.
func scanJSONL(ctx context.Context, path string, fn func(line []byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := fn(line); err != nil {
			return err
		}
	}
	return sc.Err()
}

// contentText extracts the human text from a message.content value, which is
// either a JSON string or an array of blocks carrying type/text/thinking.
func contentText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
		Name     string `json:"name"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text", "input_text", "output_text":
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		case "thinking":
			if b.Thinking != "" {
				parts = append(parts, b.Thinking)
			}
		case "tool_use":
			if b.Name != "" {
				parts = append(parts, "[tool_use "+b.Name+"]")
			}
		}
	}
	return strings.Join(parts, "\n")
}

// parseTime parses an RFC3339 timestamp or a Unix epoch in milliseconds or
// seconds. It returns the zero time on failure.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		switch {
		case n > 1e15:
			return time.UnixMilli(n)
		case n > 1e11:
			return time.Unix(0, n*int64(time.Millisecond))
		default:
			return time.Unix(n, 0)
		}
	}
	return time.Time{}
}

// matchSnippet returns the single line of text containing the match, bounded
// to max bytes.
func matchSnippet(text string, loc []int, max int) string {
	start := loc[0]
	if start < 0 || start > len(text) {
		start = 0
	}
	lineStart := strings.LastIndexByte(text[:start], '\n') + 1
	lineEnd := strings.IndexByte(text[start:], '\n')
	var line string
	if lineEnd < 0 {
		line = text[lineStart:]
	} else {
		line = text[lineStart : start+lineEnd]
	}
	line = strings.TrimSpace(line)
	if len(line) > max {
		line = line[:max] + "…"
	}
	return line
}

// errStopScan stops a turn stream without being treated as a failure.
var errStopScan = errors.New("stop scan")

// scanTurns applies a regex over a stream of attributed turns, honouring caps
// and the context deadline. The reader calls fn for each turn in order and
// stops when fn returns false.
func scanTurns(ctx context.Context, src Source, re *regexp.Regexp, caps Caps, reader func(fn func(Turn) bool) error) ([]Hit, bool, error) {
	var hits []Hit
	total := 0
	truncated := false
	err := reader(func(t Turn) bool {
		if err := ctx.Err(); err != nil {
			return false
		}
		if t.Text == "" {
			return true
		}
		loc := re.FindStringIndex(t.Text)
		if loc == nil {
			return true
		}
		snip := matchSnippet(t.Text, loc, caps.MaxSnippet)
		total += len(snip)
		if total > caps.MaxTotalBytes || len(hits) >= caps.MaxMatches {
			truncated = true
			return false
		}
		hits = append(hits, Hit{
			Agent:     src.Agent,
			SessionID: src.SessionID,
			Path:      src.Path,
			Project:   src.Project,
			Role:      t.Role,
			Turn:      t.Index,
			At:        t.At,
			Snippet:   snip,
		})
		return true
	})
	if err != nil && !errors.Is(err, errStopScan) {
		return hits, truncated, err
	}
	if ctx.Err() != nil {
		return hits, true, nil
	}
	return hits, truncated, nil
}

// jsonlTurn is a self-describing transcript line shared by several dialects.
type jsonlTurn struct {
	Type      string       `json:"type"`
	SessionID string       `json:"sessionId"`
	ID        string       `json:"id"`
	Cwd       string       `json:"cwd"`
	Timestamp string       `json:"timestamp"`
	Message   jsonlMessage `json:"message"`
	Payload   jsonlPayload `json:"payload"`
}

type jsonlMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type jsonlPayload struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Cwd     string          `json:"cwd"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// claudeAdapter parses Claude Code project transcripts.
type claudeAdapter struct{}

func (claudeAdapter) Sources(path string) ([]Source, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	src := Source{Agent: "claude-code", Format: FormatJSONLClaude, Path: path, ModTime: fi.ModTime()}
	foundID, foundCwd := false, false
	err = scanJSONL(context.Background(), path, func(line []byte) error {
		var t jsonlTurn
		if json.Unmarshal(line, &t) != nil {
			return nil
		}
		if !foundID && t.SessionID != "" {
			src.SessionID = t.SessionID
			foundID = true
		}
		if !foundCwd && t.Cwd != "" {
			src.Project = t.Cwd
			foundCwd = true
		}
		if foundID && foundCwd {
			return errStopScan
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopScan) {
		return nil, err
	}
	return []Source{src}, nil
}

func (claudeAdapter) Turns(src Source, from, to int) ([]Turn, error) {
	return collectJSONLTurns(src, from, to, func(t *jsonlTurn) (Turn, bool) {
		if t.Type != "user" && t.Type != "assistant" {
			return Turn{}, false
		}
		if t.Message.Role == "" {
			return Turn{}, false
		}
		return Turn{Role: t.Message.Role, Text: contentText(t.Message.Content), At: parseTime(t.Timestamp)}, true
	})
}

func (claudeAdapter) Scan(ctx context.Context, src Source, re *regexp.Regexp, caps Caps) ([]Hit, error) {
	hits, trunc, err := scanTurns(ctx, src, re, caps, func(fn func(Turn) bool) error {
		return streamJSONLTurns(src, fn, func(t *jsonlTurn) (Turn, bool) {
			if t.Type != "user" && t.Type != "assistant" {
				return Turn{}, false
			}
			if t.Message.Role == "" {
				return Turn{}, false
			}
			return Turn{Role: t.Message.Role, Text: contentText(t.Message.Content), At: parseTime(t.Timestamp)}, true
		})
	})
	_ = trunc
	return hits, err
}

// codexAdapter parses Codex rollout files. cwd and session id appear only on
// line 1 (session_meta).
type codexAdapter struct{}

func (codexAdapter) Sources(path string) ([]Source, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	src := Source{Agent: "codex", Format: FormatJSONLCodex, Path: path, ModTime: fi.ModTime()}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	if sc.Scan() {
		var t jsonlTurn
		if json.Unmarshal(bytes.TrimSpace(sc.Bytes()), &t) == nil && t.Type == "session_meta" {
			src.SessionID = t.Payload.ID
			src.Project = t.Payload.Cwd
		}
	}
	return []Source{src}, nil
}

func (codexAdapter) Turns(src Source, from, to int) ([]Turn, error) {
	return collectJSONLTurns(src, from, to, func(t *jsonlTurn) (Turn, bool) {
		if t.Type != "response_item" || t.Payload.Type != "message" {
			return Turn{}, false
		}
		role := t.Payload.Role
		if role == "" {
			role = t.Payload.Type
		}
		return Turn{Role: role, Text: contentText(t.Payload.Content), At: parseTime(t.Timestamp)}, true
	})
}

func (codexAdapter) Scan(ctx context.Context, src Source, re *regexp.Regexp, caps Caps) ([]Hit, error) {
	hits, _, err := scanTurns(ctx, src, re, caps, func(fn func(Turn) bool) error {
		return streamJSONLTurns(src, fn, func(t *jsonlTurn) (Turn, bool) {
			if t.Type != "response_item" || t.Payload.Type != "message" {
				return Turn{}, false
			}
			role := t.Payload.Role
			if role == "" {
				role = t.Payload.Type
			}
			return Turn{Role: role, Text: contentText(t.Payload.Content), At: parseTime(t.Timestamp)}, true
		})
	})
	return hits, err
}

// piAdapter parses pi session files. cwd and session id appear only on line 1
// (type "session").
type piAdapter struct{}

func (piAdapter) Sources(path string) ([]Source, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	src := Source{Agent: "pi", Format: FormatJSONLPi, Path: path, ModTime: fi.ModTime()}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	if sc.Scan() {
		var t jsonlTurn
		if json.Unmarshal(bytes.TrimSpace(sc.Bytes()), &t) == nil && t.Type == "session" {
			src.SessionID = t.ID
			src.Project = t.Cwd
		}
	}
	return []Source{src}, nil
}

func (piAdapter) Turns(src Source, from, to int) ([]Turn, error) {
	return collectJSONLTurns(src, from, to, func(t *jsonlTurn) (Turn, bool) {
		if t.Type != "message" {
			return Turn{}, false
		}
		role := t.Message.Role
		if role == "" {
			return Turn{}, false
		}
		return Turn{Role: role, Text: contentText(t.Message.Content), At: parseTime(t.Timestamp)}, true
	})
}

func (piAdapter) Scan(ctx context.Context, src Source, re *regexp.Regexp, caps Caps) ([]Hit, error) {
	hits, _, err := scanTurns(ctx, src, re, caps, func(fn func(Turn) bool) error {
		return streamJSONLTurns(src, fn, func(t *jsonlTurn) (Turn, bool) {
			if t.Type != "message" {
				return Turn{}, false
			}
			role := t.Message.Role
			if role == "" {
				return Turn{}, false
			}
			return Turn{Role: role, Text: contentText(t.Message.Content), At: parseTime(t.Timestamp)}, true
		})
	})
	return hits, err
}

// streamJSONLTurns opens a JSONL source and feeds attributed turns to fn in
// order, incrementing the turn index per emitted turn.
func streamJSONLTurns(src Source, fn func(Turn) bool, pick func(*jsonlTurn) (Turn, bool)) error {
	idx := 0
	return scanJSONL(context.Background(), src.Path, func(line []byte) error {
		var t jsonlTurn
		if json.Unmarshal(line, &t) != nil {
			return nil
		}
		turn, ok := pick(&t)
		if !ok {
			return nil
		}
		turn.Index = idx
		idx++
		if !fn(turn) {
			return errStopScan
		}
		return nil
	})
}

// collectJSONLTurns collects a turn range from a JSONL source.
func collectJSONLTurns(src Source, from, to int, pick func(*jsonlTurn) (Turn, bool)) ([]Turn, error) {
	var out []Turn
	idx := 0
	err := scanJSONL(context.Background(), src.Path, func(line []byte) error {
		var t jsonlTurn
		if json.Unmarshal(line, &t) != nil {
			return nil
		}
		turn, ok := pick(&t)
		if !ok {
			return nil
		}
		if from > 0 && idx < from {
			idx++
			return nil
		}
		if to > 0 && idx >= to {
			return errStopScan
		}
		turn.Index = idx
		out = append(out, turn)
		idx++
		return nil
	})
	if err != nil && !errors.Is(err, errStopScan) {
		return nil, err
	}
	return out, nil
}

// signetAdapter reads signet sessions through internal/session.Store.
type signetAdapter struct {
	store *session.Store
}

func (s signetAdapter) Sources(path string) ([]Source, error) {
	// path is a concrete ~/.vulnetix/signet/sessions/<key>/<id>.jsonl file.
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	key := filepath.Base(dir)
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	src := Source{Agent: "signet", Format: FormatJSONLSignet, Path: path, SessionID: id, ModTime: fi.ModTime()}
	if s.store != nil {
		if entries, err := s.store.ReadFrom(session.Key(key), id); err == nil {
			if m, ok := session.LatestMeta(entries); ok && m.Cwd != "" {
				src.Project = m.Cwd
			}
		}
	}
	return []Source{src}, nil
}

func (s signetAdapter) Turns(src Source, from, to int) ([]Turn, error) {
	key := filepath.Base(filepath.Dir(src.Path))
	id := src.SessionID
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(src.Path), ".jsonl")
	}
	entries, err := s.store.ReadFrom(session.Key(key), id)
	if err != nil {
		return nil, err
	}
	return signetTurns(entries, from, to), nil
}

func (s signetAdapter) Scan(ctx context.Context, src Source, re *regexp.Regexp, caps Caps) ([]Hit, error) {
	key := filepath.Base(filepath.Dir(src.Path))
	id := src.SessionID
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(src.Path), ".jsonl")
	}
	entries, err := s.store.ReadFrom(session.Key(key), id)
	if err != nil {
		return nil, err
	}
	turns := signetTurns(entries, 0, 0)
	hits, trunc, err := scanTurns(ctx, src, re, caps, func(fn func(Turn) bool) error {
		for _, t := range turns {
			if !fn(t) {
				return errStopScan
			}
		}
		return nil
	})
	_ = trunc
	return hits, err
}

// signetTurns maps signet entries to attributed turns.
func signetTurns(entries []session.Entry, from, to int) []Turn {
	var turns []Turn
	idx := 0
	for _, e := range entries {
		role := e.Role
		if role == "" {
			role = e.Type
		}
		if role != "user" && role != "assistant" {
			continue
		}
		if from > 0 && idx < from {
			idx++
			continue
		}
		if to > 0 && idx >= to {
			break
		}
		turns = append(turns, Turn{
			Index: idx,
			Role:  role,
			Text:  e.Content,
			Model: metaString(e.Meta, "model"),
			At:    time.UnixMilli(e.Timestamp),
		})
		idx++
	}
	return turns
}

func metaString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// promptsAdapter parses history.jsonl prompt indexes. The exact field names
// differ per agent, so it dispatches on the source's agent name.
type promptsAdapter struct{}

func (promptsAdapter) Sources(path string) ([]Source, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	// The agent is derived by the caller from the registry entry; it is set
	// on the Source before Scan is invoked.
	return []Source{{Format: FormatJSONLPrompts, Path: path, ModTime: fi.ModTime()}}, nil
}

func (promptsAdapter) Turns(src Source, from, to int) ([]Turn, error) {
	return nil, fmt.Errorf("prompt indexes have no turns; use SearchSessions")
}

func (promptsAdapter) Scan(ctx context.Context, src Source, re *regexp.Regexp, caps Caps) ([]Hit, error) {
	var hits []Hit
	total := 0
	truncated := false
	err := scanJSONL(ctx, src.Path, func(line []byte) error {
		var rec struct {
			Display        string `json:"display"`
			Text           string `json:"text"`
			Timestamp      any    `json:"timestamp"`
			Ts             any    `json:"ts"`
			Project        string `json:"project"`
			Workspace      string `json:"workspace"`
			SessionID      string `json:"sessionId"`
			SessionIDSnake string `json:"session_id"`
			ConversationID string `json:"conversationId"`
		}
		if json.Unmarshal(line, &rec) != nil {
			return nil
		}
		text := rec.Display
		if text == "" {
			text = rec.Text
		}
		loc := re.FindStringIndex(text)
		if loc == nil {
			return nil
		}
		project := rec.Project
		if project == "" {
			project = rec.Workspace
		}
		sessionID := rec.SessionID
		if sessionID == "" {
			sessionID = rec.SessionIDSnake
		}
		if sessionID == "" {
			sessionID = rec.ConversationID
		}
		at := promptTime(rec.Timestamp, rec.Ts)
		snip := matchSnippet(text, loc, caps.MaxSnippet)
		total += len(snip)
		if total > caps.MaxTotalBytes || len(hits) >= caps.MaxMatches {
			truncated = true
			return errStopScan
		}
		hits = append(hits, Hit{
			Agent:     src.Agent,
			SessionID: sessionID,
			Path:      src.Path,
			Project:   project,
			Role:      "user",
			At:        at,
			Snippet:   snip,
		})
		return nil
	})
	_ = truncated
	if err != nil && !errors.Is(err, errStopScan) {
		return hits, err
	}
	return hits, nil
}

// promptTime parses the timestamp shapes history.jsonl indexes use.
func promptTime(ts, tsSnake any) time.Time {
	raw := ts
	if raw == nil {
		raw = tsSnake
	}
	switch v := raw.(type) {
	case float64:
		return numberTime(int64(v))
	case string:
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t
		}
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return numberTime(n)
		}
	}
	return time.Time{}
}

func numberTime(n int64) time.Time {
	switch {
	case n > 1e15:
		return time.UnixMilli(n)
	case n > 1e11:
		return time.Unix(0, n*int64(time.Millisecond))
	default:
		return time.Unix(n, 0)
	}
}
