package run

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
)

// UsageSummary folds UsageEvents into per-role and per-model totals plus the
// summed request composition. It is what `belai -prompt -usage-json` writes, so
// a benchmark can price a run and see where its tokens went. It carries counts
// only: no prompt, reply, argument or path ever reaches it.
type UsageSummary struct {
	mu sync.Mutex
	s  usageSummaryJSON
}

// usageSummaryJSON is the file format. Field names are part of the benchmark
// contract (bench/harbor reads them).
type usageSummaryJSON struct {
	Calls      int                    `json:"calls"`
	Tokens     int                    `json:"tokens"`
	Prompt     int                    `json:"prompt_tokens"`
	Completion int                    `json:"completion_tokens"`
	CacheRead  int                    `json:"cache_read_tokens"`
	CacheWrite int                    `json:"cache_write_tokens"`
	Estimated  int                    `json:"estimated_calls"`
	ByRole     map[string]*usageTotal `json:"by_role"`
	ByModel    map[string]*usageTotal `json:"by_model"`
	// Request sums the estimated request composition over agent calls only:
	// role-manager calls are counted in ByRole.
	Request requestTotals `json:"agent_request"`
}

type usageTotal struct {
	Calls      int `json:"calls"`
	Tokens     int `json:"tokens"`
	Prompt     int `json:"prompt_tokens"`
	Completion int `json:"completion_tokens"`
	CacheRead  int `json:"cache_read_tokens"`
	CacheWrite int `json:"cache_write_tokens"`
}

type requestTotals struct {
	System      int            `json:"system"`
	ToolDefs    int            `json:"tool_defs"`
	History     int            `json:"history"`
	ToolResults map[string]int `json:"tool_results"`
}

func (t *usageTotal) add(ev UsageEvent) {
	t.Calls++
	t.Tokens += ev.Tokens
	t.Prompt += ev.Prompt
	t.Completion += ev.Completion
	t.CacheRead += ev.CacheRead
	t.CacheWrite += ev.CacheWrite
}

// Add folds one event in. It is safe for concurrent use.
func (u *UsageSummary) Add(ev UsageEvent) {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := &u.s
	s.Calls++
	s.Tokens += ev.Tokens
	s.Prompt += ev.Prompt
	s.Completion += ev.Completion
	s.CacheRead += ev.CacheRead
	s.CacheWrite += ev.CacheWrite
	if ev.Estimated {
		s.Estimated++
	}
	if s.ByRole == nil {
		s.ByRole = map[string]*usageTotal{}
		s.ByModel = map[string]*usageTotal{}
	}
	role := ev.Role
	if role == "" {
		role = RoleAgent
	}
	if s.ByRole[role] == nil {
		s.ByRole[role] = &usageTotal{}
	}
	s.ByRole[role].add(ev)
	model := ev.Provider + "/" + ev.Model
	if s.ByModel[model] == nil {
		s.ByModel[model] = &usageTotal{}
	}
	s.ByModel[model].add(ev)
	if role != RoleAgent {
		return
	}
	s.Request.System += ev.Request.System
	s.Request.ToolDefs += ev.Request.ToolDefs
	s.Request.History += ev.Request.History
	for name, n := range ev.Request.ToolResults {
		if s.Request.ToolResults == nil {
			s.Request.ToolResults = map[string]int{}
		}
		s.Request.ToolResults[name] += n
	}
}

// Roles returns the roles seen so far, sorted.
func (u *UsageSummary) Roles() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]string, 0, len(u.s.ByRole))
	for r := range u.s.ByRole {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// MarshalJSON renders the summary.
func (u *UsageSummary) MarshalJSON() ([]byte, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return json.Marshal(u.s)
}

// WriteFile writes the summary as indented JSON to path.
func (u *UsageSummary) WriteFile(path string) error {
	b, err := json.MarshalIndent(u, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}
