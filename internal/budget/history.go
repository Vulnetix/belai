package budget

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
)

// History import. The ledger counts every call from the moment a belai with
// token budgets runs; sessions saved before that, or by an older binary still
// running, are known only through their transcripts. ImportHistory folds each
// transcript the ledger has never seen into the day totals once, so day and
// month budgets cover every session, not just the ones recorded live.
//
// A transcript does not keep every call's usage, so the import recovers what
// it can: a goal run's cumulative tokensUsed is exact for everything the goal
// spent (split across days by its goal_state timestamps); any other turn keeps
// only the usage of its last model call, so those turns are a lower bound, and
// classifier and role-manager calls outside a goal are not recoverable.

// transcriptLine is the part of a session.Entry the import reads.
type transcriptLine struct {
	Type      string          `json:"type"`
	Timestamp int64           `json:"timestamp"` // unix milliseconds
	Content   json.RawMessage `json:"content"`
	Meta      map[string]any  `json:"meta"`
}

// historyUsage is one transcript's recoverable usage: tokens by model key by
// local date.
type historyUsage map[string]map[string]int64

func (h historyUsage) add(key, day string, n int64) {
	if n <= 0 || key == "/" {
		return
	}
	if h[key] == nil {
		h[key] = map[string]int64{}
	}
	h[key][day] += n
}

// ImportHistory imports the usage of every session transcript under
// sessionsDir (<dir>/<project>/<session-id>.jsonl) that the ledger has not
// seen: not recorded live, not imported before, and not this recorder's own
// session. It returns how many sessions were imported. Transcripts are parsed
// outside the lock; the ledger is then re-read under the lock and only
// sessions still unseen are applied, so two processes importing at once count
// each session once.
func (r *Recorder) ImportHistory(sessionsDir string) (int, error) {
	files, _ := filepath.Glob(filepath.Join(sessionsDir, "*", "*.jsonl"))
	if len(files) == 0 {
		return 0, nil
	}
	r.mu.Lock()
	seen := func(id string) bool {
		_, live := r.disk.Sessions[id]
		_, done := r.disk.Imported[id]
		return live || done || id == r.session
	}
	var todo []string
	for _, f := range files {
		if !seen(strings.TrimSuffix(filepath.Base(f), ".jsonl")) {
			todo = append(todo, f)
		}
	}
	loc := r.now().Location()
	r.mu.Unlock()
	if len(todo) == 0 {
		return 0, nil
	}

	parsed := map[string]historyUsage{}
	for _, f := range todo {
		h, err := readTranscriptUsage(f, loc)
		if err != nil {
			continue // an unreadable transcript is skipped, and retried next start
		}
		parsed[strings.TrimSuffix(filepath.Base(f), ".jsonl")] = h
	}
	if len(parsed) == 0 {
		return 0, nil
	}

	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return 0, err
	}
	unlock, err := config.AcquireFileLock(r.path + ".lock")
	if err != nil {
		return 0, err
	}
	defer unlock()
	data, notice, err := readLedger(r.path)
	if err != nil {
		return 0, err
	}
	now := r.now()
	today := now.Format(dayLayout)
	imported := 0
	for id, h := range parsed {
		if _, live := data.Sessions[id]; live {
			continue
		}
		if _, done := data.Imported[id]; done {
			continue
		}
		for key, byDay := range h {
			if data.Days[key] == nil {
				data.Days[key] = map[string]int64{}
			}
			for d, n := range byDay {
				data.Days[key][d] += n
			}
		}
		data.Imported[id] = today
		imported++
	}
	if imported == 0 {
		return 0, nil
	}
	prune(&data, now, r.retention)
	buf, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := config.WriteGlobalFileAtomic(r.path, buf); err != nil {
		return 0, err
	}
	r.mu.Lock()
	r.disk = data
	r.loaded = now
	if notice != "" {
		r.notice = notice
	}
	r.mu.Unlock()
	return imported, nil
}

// readTranscriptUsage recovers one transcript's usage. Goal passes are taken
// from the goal_state rows: the growth of tokensUsed between consecutive rows
// of the same goal, on the local day of the later row, attributed to the model
// the goal's assistant rows name. Every other assistant row adds its own
// total_tokens under its own provider/model. Goal-mode assistant rows are
// skipped because the goal's tokensUsed already includes them.
func readTranscriptUsage(path string, loc *time.Location) (historyUsage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	h := historyUsage{}
	type goalPoint struct {
		day    string
		tokens int64
	}
	goalDeltas := map[string][]goalPoint{} // goal id → per-row growth
	lastGoalTokens := map[string]int64{}
	goalModels := map[string]int{} // model key → goal-mode assistant rows

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var e transcriptLine
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Timestamp <= 0 {
			continue
		}
		day := time.UnixMilli(e.Timestamp).In(loc).Format(dayLayout)
		switch e.Type {
		case "assistant":
			key := config.ModelKey(metaString(e.Meta, "provider"), metaString(e.Meta, "model"))
			if metaString(e.Meta, "mode") == "goal" {
				goalModels[key]++
				continue
			}
			h.add(key, day, metaInt(e.Meta, "total_tokens"))
		case "goal_state":
			var raw string
			if json.Unmarshal(e.Content, &raw) != nil {
				continue
			}
			var gs struct {
				ID         string `json:"id"`
				TokensUsed int64  `json:"tokensUsed"`
			}
			if json.Unmarshal([]byte(raw), &gs) != nil || gs.ID == "" {
				continue
			}
			if d := gs.TokensUsed - lastGoalTokens[gs.ID]; d > 0 {
				goalDeltas[gs.ID] = append(goalDeltas[gs.ID], goalPoint{day: day, tokens: d})
			}
			if gs.TokensUsed > lastGoalTokens[gs.ID] {
				lastGoalTokens[gs.ID] = gs.TokensUsed
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	goalKey, best := "", 0
	for k, n := range goalModels {
		if n > best || (n == best && k < goalKey) {
			goalKey, best = k, n
		}
	}
	if goalKey != "" {
		for _, pts := range goalDeltas {
			for _, p := range pts {
				h.add(goalKey, p.day, p.tokens)
			}
		}
	}
	return h, nil
}

func metaString(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func metaInt(m map[string]any, k string) int64 {
	switch v := m[k].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	}
	return 0
}
