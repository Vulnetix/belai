package sessionmeasure

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"sort"

	"github.com/vulnetix/belai/internal/filediff"
)

// CheckCounts is how many checks of each kind a session ran.
type CheckCounts struct {
	Build int `json:"build"`
	Test  int `json:"test"`
	Lint  int `json:"lint"`
	Gate  int `json:"gate"`
}

// ModelUsage is one model's share of a session. CostMicroUSD is null unless
// every call of the model carried a cost, so a partly priced model reads as
// tokens only instead of as an undercount; Priced says which.
type ModelUsage struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	CostMicroUSD     *int64 `json:"cost_micro_usd"`
	Priced           bool   `json:"priced"`
}

// Rollup is what a session did, added up when it finishes. It rides the
// session-finish call as `rollup`:
//
//	{"active_ms":812000,"wait_ms":45000,"tool_calls":132,
//	 "check_counts":{"build":3,"test":5,"lint":1,"gate":0},
//	 "files_changed":9,"additions":410,"deletions":73,
//	 "models":[{"provider":"anthropic","model":"claude-sonnet-4-5","prompt_tokens":120000,
//	            "completion_tokens":9000,"cache_read_tokens":80000,"cache_write_tokens":10000,
//	            "cost_micro_usd":642000,"priced":true}]}
//
// CheckCounts is null for a transcript no measuring host wrote, where a count
// of zero would claim a measurement nobody took. Every other figure is read
// from entries every Belai has always written.
type Rollup struct {
	// ActiveMS is the time agent turns ran, less the time they spent waiting
	// for a person to answer.
	ActiveMS int64 `json:"active_ms"`
	// WaitMS is the time from an ask to its answer, with overlapping asks
	// counted once. A plan review does not block the agent and is not counted.
	WaitMS    int64 `json:"wait_ms"`
	ToolCalls int   `json:"tool_calls"`
	// CheckCounts is nil when the transcript predates check_run entries.
	CheckCounts *CheckCounts `json:"check_counts"`
	// FilesChanged counts distinct paths the session's tools changed;
	// Additions and Deletions add up the lines each change added and removed,
	// so a line edited twice counts twice.
	FilesChanged int          `json:"files_changed"`
	Additions    int          `json:"additions"`
	Deletions    int          `json:"deletions"`
	Models       []ModelUsage `json:"models"`
}

// RollupFile reads a session's JSONL and adds it up. A missing file is an
// error; a damaged line is skipped.
func RollupFile(path string) (*Rollup, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Compute(f)
}

type interval struct{ from, to int64 }

// rollLine is the part of a JSONL line the rollup reads.
type rollLine struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Timestamp int64           `json:"timestamp"`
	Meta      json.RawMessage `json:"meta"`
}

// Compute adds up the JSONL lines of one session.
func Compute(r io.Reader) (*Rollup, error) {
	var (
		out      = &Rollup{Models: []ModelUsage{}}
		counts   CheckCounts
		measured bool
		turns    []interval
		asks     = map[string]int64{}
		waits    []interval
		paths    = map[string]bool{}
		models   = map[[2]string]*modelAcc{}
	)
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var l rollLine
			if json.Unmarshal(line, &l) == nil {
				switch l.Type {
				case "turn_state":
					var m struct {
						State      string `json:"state"`
						StartedAt  int64  `json:"started_at"`
						DurationMS int64  `json:"duration_ms"`
					}
					if json.Unmarshal(l.Meta, &m) == nil && m.State != "started" && m.DurationMS > 0 {
						end := l.Timestamp
						start := end - m.DurationMS
						if m.StartedAt > 0 {
							start, end = m.StartedAt, m.StartedAt+m.DurationMS
						}
						turns = append(turns, interval{start, end})
					}
				case "ask", "ask_answer":
					var m struct {
						AskID string `json:"ask_id"`
						Kind  string `json:"kind"`
					}
					if json.Unmarshal(l.Meta, &m) != nil || m.AskID == "" || m.Kind == "plan_review" {
						break
					}
					if l.Type == "ask" {
						asks[m.AskID] = l.Timestamp
					} else if from, ok := asks[m.AskID]; ok && l.Timestamp >= from {
						waits = append(waits, interval{from, l.Timestamp})
						delete(asks, m.AskID)
					}
				case "tool":
					out.ToolCalls++
					var m struct {
						Diff *filediff.WireChange `json:"diff"`
					}
					if json.Unmarshal(l.Meta, &m) == nil && m.Diff != nil {
						for _, f := range m.Diff.Files {
							a, d := f.Stat()
							out.Additions += a
							out.Deletions += d
							if f.Path != "" {
								paths[f.Path] = true
							}
						}
					}
				case "assistant":
					var m struct {
						Provider         string `json:"provider"`
						Model            string `json:"model"`
						PromptTokens     int64  `json:"prompt_tokens"`
						CompletionTokens int64  `json:"completion_tokens"`
						CacheRead        *int64 `json:"cache_read_tokens"`
						CacheWrite       int64  `json:"cache_write_tokens"`
						Cost             *int64 `json:"cost_micro_usd"`
					}
					if json.Unmarshal(l.Meta, &m) != nil {
						break
					}
					if m.CacheRead != nil {
						measured = true
					}
					if m.PromptTokens == 0 && m.CompletionTokens == 0 && m.CacheRead == nil && m.Cost == nil {
						break // an entry with no usage, such as a restored turn
					}
					acc := models[[2]string{m.Provider, m.Model}]
					if acc == nil {
						acc = &modelAcc{ModelUsage: ModelUsage{Provider: m.Provider, Model: m.Model}, allPriced: true}
						models[[2]string{m.Provider, m.Model}] = acc
					}
					acc.PromptTokens += m.PromptTokens
					acc.CompletionTokens += m.CompletionTokens
					if m.CacheRead != nil {
						acc.CacheReadTokens += *m.CacheRead
					}
					acc.CacheWriteTokens += m.CacheWrite
					if m.Cost == nil {
						acc.allPriced = false
					} else {
						acc.cost += *m.Cost
					}
				case EntryCheckRun:
					measured = true
					var m struct {
						Kind Kind `json:"kind"`
					}
					if json.Unmarshal(l.Meta, &m) == nil {
						switch m.Kind {
						case KindBuild:
							counts.Build++
						case KindTest:
							counts.Test++
						case KindLint:
							counts.Lint++
						case KindGate:
							counts.Gate++
						}
					}
				case EntryCommit:
					measured = true
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
	}

	waits = merge(waits)
	for _, w := range waits {
		out.WaitMS += w.to - w.from
	}
	for _, t := range turns {
		out.ActiveMS += (t.to - t.from) - overlap(t, waits)
	}
	out.FilesChanged = len(paths)
	if measured {
		out.CheckCounts = &counts
	}
	for _, acc := range models {
		m := acc.ModelUsage
		if acc.allPriced {
			c := acc.cost
			m.CostMicroUSD, m.Priced = &c, true
		}
		out.Models = append(out.Models, m)
	}
	sort.Slice(out.Models, func(i, j int) bool {
		a, b := out.Models[i], out.Models[j]
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Model < b.Model
	})
	return out, nil
}

type modelAcc struct {
	ModelUsage
	cost      int64
	allPriced bool
}

// merge joins overlapping intervals so concurrent asks count their wait once.
func merge(in []interval) []interval {
	if len(in) == 0 {
		return nil
	}
	s := slices.Clone(in)
	sort.Slice(s, func(i, j int) bool { return s[i].from < s[j].from })
	out := []interval{s[0]}
	for _, iv := range s[1:] {
		last := &out[len(out)-1]
		if iv.from <= last.to {
			last.to = max(last.to, iv.to)
			continue
		}
		out = append(out, iv)
	}
	return out
}

// overlap is how much of t the merged waits cover.
func overlap(t interval, waits []interval) int64 {
	var n int64
	for _, w := range waits {
		lo, hi := max(t.from, w.from), min(t.to, w.to)
		if hi > lo {
			n += hi - lo
		}
	}
	return n
}
