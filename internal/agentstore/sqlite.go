package agentstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/proc"
)

// sqliteQuery runs a read-only sqlite3 query and returns stdout. The binary
// path is resolved at probe time; a missing binary makes the agent
// unavailable rather than erroring.
func sqliteQuery(ctx context.Context, sqlite, db, query string) (string, error) {
	ec := exec.CommandContext(ctx, sqlite, "-readonly", db, query)
	ec.Env = proc.ScrubbedEnv()
	out, err := ec.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// sqlQuote quotes a value for use inside a single-quoted SQL literal.
func sqlQuote(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// gooseAdapter reads the goose sessions SQLite database.
type gooseAdapter struct{ sqlite string }

func (g gooseAdapter) Sources(path string) ([]Source, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sqliteQuery(ctx, g.sqlite, path, "SELECT id, working_dir, updated_at FROM sessions ORDER BY updated_at DESC LIMIT 5000;")
	if err != nil {
		return nil, err
	}
	return parseSQLiteSessions(out, "goose", FormatSQLiteGoose, path, fi.ModTime()), nil
}

// parseSQLiteSessions parses "id|working_dir|mtime" tab-separated rows.
func parseSQLiteSessions(out, agent string, format Format, path string, defMtime time.Time) []Source {
	var srcs []Source
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}
		id, dir, mtimeStr := parts[0], parts[1], parts[2]
		mtime := defMtime
		if n, err := parseInt64(mtimeStr); err == nil {
			mtime = numberTime(n)
		}
		srcs = append(srcs, Source{
			Agent:     agent,
			Format:    format,
			Path:      path,
			SessionID: id,
			Project:   dir,
			ModTime:   mtime,
		})
	}
	return srcs
}

func parseInt64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	return n, err
}

func (g gooseAdapter) Turns(src Source, from, to int) ([]Turn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sqliteQuery(ctx, g.sqlite, src.Path,
		"SELECT role, content_json, created_timestamp FROM messages WHERE session_id = '"+sqlQuote(src.SessionID)+"' ORDER BY id;")
	if err != nil {
		return nil, err
	}
	turns := gooseTurns(out)
	if from > 0 {
		if from > len(turns) {
			from = len(turns)
		}
		turns = turns[from:]
	}
	if to > 0 && to < len(turns) {
		turns = turns[:to]
	}
	return turns, nil
}

// gooseTurns parses goose message rows into turns.
func gooseTurns(out string) []Turn {
	var turns []Turn
	idx := 0
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		role, rest, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		contentJSON, tsStr, ok := strings.Cut(rest, "|")
		if !ok {
			continue
		}
		text := gooseContentText(contentJSON)
		at := time.Time{}
		if n, err := parseInt64(tsStr); err == nil {
			at = time.Unix(n, 0)
		}
		turns = append(turns, Turn{Index: idx, Role: role, Text: text, At: at})
		idx++
	}
	return turns
}

// gooseContentText extracts text from a goose content_json array of blocks.
func gooseContentText(raw string) string {
	var blocks []struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		ToolResult struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"toolResult"`
	}
	if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
		// Not an array; fall back to the raw string trimmed of quotes.
		return strings.Trim(raw, `"`)
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
			continue
		}
		for _, c := range b.ToolResult.Content {
			if c.Type == "text" && c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func (g gooseAdapter) Scan(ctx context.Context, src Source, re *regexp.Regexp, caps Caps) ([]Hit, error) {
	turns, err := g.Turns(src, 0, 0)
	if err != nil {
		return nil, err
	}
	hits, _, err := scanTurns(ctx, src, re, caps, func(fn func(Turn) bool) error {
		for _, t := range turns {
			if !fn(t) {
				return errStopScan
			}
		}
		return nil
	})
	return hits, err
}

// opencodeAdapter reads the opencode SQLite database. Text lives in part.data
// (opaque JSON), joined via message to session(directory).
type opencodeAdapter struct{ sqlite string }

func (o opencodeAdapter) Sources(path string) ([]Source, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sqliteQuery(ctx, o.sqlite, path, "SELECT id, directory, time_created FROM session ORDER BY time_created DESC LIMIT 5000;")
	if err != nil {
		return nil, err
	}
	var srcs []Source
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}
		at := fi.ModTime()
		if n, err := parseInt64(parts[2]); err == nil {
			at = numberTime(n)
		}
		srcs = append(srcs, Source{
			Agent:     "opencode",
			Format:    FormatSQLiteOpenCode,
			Path:      path,
			SessionID: parts[0],
			Project:   parts[1],
			ModTime:   at,
		})
	}
	return srcs, nil
}

func (o opencodeAdapter) Turns(src Source, from, to int) ([]Turn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sqliteQuery(ctx, o.sqlite, src.Path,
		"SELECT m.data, p.data FROM part p JOIN message m ON m.id = p.message_id WHERE p.session_id = '"+sqlQuote(src.SessionID)+"' ORDER BY m.time_created, p.time_created;")
	if err != nil {
		return nil, err
	}
	turns := opencodeTurns(out)
	if from > 0 {
		if from > len(turns) {
			from = len(turns)
		}
		turns = turns[from:]
	}
	if to > 0 && to < len(turns) {
		turns = turns[:to]
	}
	return turns, nil
}

// opencodeTurns groups part rows by message and builds one turn per message.
func opencodeTurns(out string) []Turn {
	type partRow struct {
		msgData  string
		partData string
	}
	var rows []partRow
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		msgData, partData, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		rows = append(rows, partRow{msgData: msgData, partData: partData})
	}
	var turns []Turn
	curID := ""
	cur := Turn{}
	flush := func() {
		if cur.Role != "" || cur.Text != "" {
			cur.Index = len(turns)
			turns = append(turns, cur)
		}
		cur = Turn{}
	}
	for _, r := range rows {
		var msg struct {
			ID   string `json:"id"`
			Role string `json:"role"`
		}
		_ = json.Unmarshal([]byte(r.msgData), &msg)
		if msg.ID != curID {
			flush()
			curID = msg.ID
			cur.Role = msg.Role
		}
		var part struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(r.partData), &part) == nil {
			if part.Type == "text" && part.Text != "" {
				if cur.Text != "" {
					cur.Text += "\n"
				}
				cur.Text += part.Text
			}
		}
	}
	flush()
	return turns
}

func (o opencodeAdapter) Scan(ctx context.Context, src Source, re *regexp.Regexp, caps Caps) ([]Hit, error) {
	turns, err := o.Turns(src, 0, 0)
	if err != nil {
		return nil, err
	}
	hits, _, err := scanTurns(ctx, src, re, caps, func(fn func(Turn) bool) error {
		for _, t := range turns {
			if !fn(t) {
				return errStopScan
			}
		}
		return nil
	})
	return hits, err
}
