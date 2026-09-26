package agentstore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/proc"
	"github.com/vulnetix/belai/internal/session"
)

// adapterFor builds the dialect adapter for one registry agent.
func (r *Registry) adapterFor(a Agent) Adapter {
	switch a.Format {
	case FormatJSONLClaude:
		return claudeAdapter{}
	case FormatJSONLCodex:
		return codexAdapter{}
	case FormatJSONLPi:
		return piAdapter{}
	case FormatJSONLBelai:
		return belaiAdapter{store: session.NewStoreAt(filepath.Join(r.home, ".vulnetix", "belai", "sessions"))}
	case FormatJSONLPrompts:
		return promptsAdapter{}
	case FormatSQLiteGoose:
		return gooseAdapter{sqlite: r.sqlite3Path()}
	case FormatSQLiteOpenCode:
		return opencodeAdapter{sqlite: r.sqlite3Path()}
	case FormatJSONVSCode:
		return vscodeAdapter{}
	case FormatJSONGeneric:
		return genericAdapter{}
	default:
		return nil
	}
}

func agentByName(name string) (Agent, bool) {
	for _, a := range knownAgents {
		if a.Name == name {
			return a, true
		}
	}
	return Agent{}, false
}

// defaultProject returns the project scope for a query: the explicit project,
// or the session workdir unless all_projects is set.
func (r *Registry) defaultProject(q SessionQuery) string {
	if q.AllProjects {
		return ""
	}
	if q.Project != "" {
		return q.Project
	}
	return r.workdir
}

func sourceMatchesProject(src Source, project string) bool {
	if project == "" {
		return true
	}
	return src.Project != "" && strings.Contains(src.Project, project)
}

func sourceMatchesTime(src Source, since, until time.Time) bool {
	if !since.IsZero() && src.ModTime.Before(since) {
		return false
	}
	if !until.IsZero() && src.ModTime.After(until) {
		return false
	}
	return true
}

func hitMatchesProject(h Hit, project string) bool {
	if project == "" {
		return true
	}
	return h.Project != "" && strings.Contains(h.Project, project)
}

// SearchSessions searches present agents' transcript (or prompt-index) stores.
func (r *Registry) SearchSessions(ctx context.Context, q SessionQuery) (SessionSearch, error) {
	r.probeOnce()
	caps := DefaultCaps()
	if q.MaxMatches > 0 {
		caps.MaxMatches = q.MaxMatches
	}
	if caps.Deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, caps.Deadline)
		defer cancel()
	}

	res := SessionSearch{}
	for _, st := range r.status {
		if !st.Present {
			res.Skipped = append(res.Skipped, SkippedAgent{Agent: st.Name, Reason: st.Reason})
			continue
		}
		res.Sources = append(res.Sources, SourceCount{Agent: st.Name, Files: r.sourceCount(&st, q)})
	}

	var agents []*AgentStatus
	if q.Agent != "" {
		st := r.statusFor(q.Agent)
		if st == nil {
			return res, fmt.Errorf("unknown agent %q", q.Agent)
		}
		if !st.Present {
			return res, nil
		}
		agents = []*AgentStatus{st}
	} else {
		for i := range r.status {
			if r.status[i].Present {
				agents = append(agents, &r.status[i])
			}
		}
	}

	remaining := caps.MaxMatches
	remainingBytes := caps.MaxTotalBytes
	filesScanned := 0
	project := r.defaultProject(q)

	for _, st := range agents {
		if remaining <= 0 || remainingBytes <= 0 {
			res.Truncated = true
			break
		}
		a, ok := agentByName(st.Name)
		if !ok {
			continue
		}
		adapter := r.adapterFor(a)
		if adapter == nil {
			continue
		}

		if q.PromptsOnly {
			promptAdapter := promptsAdapter{}
			for _, path := range st.Prompts {
				if remaining <= 0 || remainingBytes <= 0 || filesScanned >= caps.MaxFiles {
					res.Truncated = true
					break
				}
				src := Source{Agent: st.Name, Format: FormatJSONLPrompts, Path: path}
				if fi, err := os.Stat(path); err == nil {
					src.ModTime = fi.ModTime()
				}
				if !sourceMatchesTime(src, q.Since, q.Until) {
					continue
				}
				filesScanned++
				hits, err := promptAdapter.Scan(ctx, src, q.Re, capsFor(remaining, remainingBytes, caps))
				if err != nil {
					continue
				}
				for _, h := range hits {
					if !hitMatchesProject(h, project) {
						continue
					}
					if roleMatches(q.Role, h.Role) {
						res.Hits = append(res.Hits, h)
						remaining--
						remainingBytes -= len(h.Snippet)
					}
				}
			}
			continue
		}

		sources := r.narrowedSources(st, a, adapter, q, caps.MaxFiles, &filesScanned, &res.Truncated)
		if len(sources) == 0 {
			continue
		}
		flagged := rgPrefilter(ctx, sources, q.Re)
		if flagged == nil {
			// rg absent or errored: scan every narrowed source.
			for _, src := range sources {
				if remaining <= 0 || remainingBytes <= 0 {
					res.Truncated = true
					break
				}
				hits, err := adapter.Scan(ctx, src, q.Re, capsFor(remaining, remainingBytes, caps))
				if err != nil {
					continue
				}
				res.Hits, remaining, remainingBytes = collectHits(res.Hits, hits, q, project, remaining, remainingBytes)
			}
		} else {
			for _, src := range sources {
				if !flagged[src.Path] {
					continue
				}
				if remaining <= 0 || remainingBytes <= 0 {
					res.Truncated = true
					break
				}
				hits, err := adapter.Scan(ctx, src, q.Re, capsFor(remaining, remainingBytes, caps))
				if err != nil {
					continue
				}
				res.Hits, remaining, remainingBytes = collectHits(res.Hits, hits, q, project, remaining, remainingBytes)
			}
		}
	}

	if ctx.Err() == context.DeadlineExceeded {
		res.DeadlineHit = true
	}
	res.FilesScanned = filesScanned
	return res, nil
}

// capsFor returns the scan caps reflecting the remaining global budget.
func capsFor(remainingMatches, remainingBytes int, base Caps) Caps {
	c := base
	if remainingMatches < c.MaxMatches {
		c.MaxMatches = remainingMatches
	}
	if remainingBytes < c.MaxTotalBytes {
		c.MaxTotalBytes = remainingBytes
	}
	if c.MaxMatches < 1 {
		c.MaxMatches = 1
	}
	if c.MaxTotalBytes < 1 {
		c.MaxTotalBytes = 1
	}
	return c
}

// collectHits filters and appends hits, returning updated budgets.
func collectHits(dst []Hit, hits []Hit, q SessionQuery, project string, remaining, remainingBytes int) ([]Hit, int, int) {
	for _, h := range hits {
		if !hitMatchesProject(h, project) {
			continue
		}
		if !roleMatches(q.Role, h.Role) {
			continue
		}
		dst = append(dst, h)
		remaining--
		remainingBytes -= len(h.Snippet)
		if remaining <= 0 || remainingBytes <= 0 {
			break
		}
	}
	return dst, remaining, remainingBytes
}

func roleMatches(want, got string) bool {
	if want == "" {
		return true
	}
	return strings.EqualFold(want, got)
}

// sourceCount returns the header file/session count for a present agent.
func (r *Registry) sourceCount(st *AgentStatus, q SessionQuery) int {
	if q.PromptsOnly {
		return len(st.Prompts)
	}
	if !isSQLiteFormat(agentFormat(st.Name)) {
		return len(st.Sessions)
	}
	// SQLite: count stored sessions, bounded.
	a, _ := agentByName(st.Name)
	adapter := r.adapterFor(a)
	if adapter == nil {
		return len(st.Sessions)
	}
	count := 0
	for _, path := range st.Sessions {
		srcs, err := adapter.Sources(path)
		if err != nil {
			continue
		}
		count += len(srcs)
	}
	return count
}

func agentFormat(name string) Format {
	a, ok := agentByName(name)
	if !ok {
		return ""
	}
	return a.Format
}

func isSQLiteFormat(f Format) bool {
	return f == FormatSQLiteGoose || f == FormatSQLiteOpenCode
}

// narrowedSources enumerates and narrows an agent's session sources by agent,
// project, and mtime, respecting the MaxFiles cap.
func (r *Registry) narrowedSources(st *AgentStatus, a Agent, adapter Adapter, q SessionQuery, maxFiles int, filesScanned *int, truncated *bool) []Source {
	project := r.defaultProject(q)
	var out []Source
	for _, path := range st.Sessions {
		if *filesScanned >= maxFiles {
			*truncated = true
			break
		}
		srcs, err := adapter.Sources(path)
		if err != nil {
			continue
		}
		*filesScanned += len(srcs)
		for _, src := range srcs {
			if *filesScanned > maxFiles {
				*truncated = true
				break
			}
			if !sourceMatchesTime(src, q.Since, q.Until) {
				continue
			}
			if !sourceMatchesProject(src, project) {
				continue
			}
			out = append(out, src)
		}
	}
	return out
}

// rgPrefilter narrows a source set to files rg flags as matching. It returns
// nil when rg is unavailable, errors, or the file set is too large to pass as
// argv, in which case the caller scans every source (the streaming fallback).
func rgPrefilter(ctx context.Context, sources []Source, re *regexp.Regexp) map[string]bool {
	if len(sources) == 0 || len(sources) > 500 {
		return nil
	}
	rg, err := exec.LookPath("rg")
	if err != nil {
		return nil
	}
	args := []string{"-l", "--", re.String()}
	for _, s := range sources {
		args = append(args, s.Path)
	}
	ec := exec.CommandContext(ctx, rg, args...)
	ec.Env = proc.ScrubbedEnv()
	out, err := ec.Output()
	if err != nil {
		return nil
	}
	matched := map[string]bool{}
	for _, p := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if p != "" {
			matched[p] = true
		}
	}
	return matched
}

// ReadSession resolves a session by agent + prefix and returns a turn range.
func (r *Registry) ReadSession(ctx context.Context, req ReadRequest) (SessionRead, error) {
	r.probeOnce()
	a, ok := agentByName(req.Agent)
	if !ok {
		return SessionRead{}, fmt.Errorf("unknown agent %q", req.Agent)
	}
	if isSQLiteFormat(a.Format) && r.sqlite3Path() == "" {
		return SessionRead{}, fmt.Errorf("%s is unavailable: sqlite3 not on PATH", req.Agent)
	}
	adapter := r.adapterFor(a)
	if adapter == nil {
		return SessionRead{}, fmt.Errorf("agent %q has no transcript adapter", req.Agent)
	}

	st := r.statusFor(req.Agent)
	if st == nil || len(st.Sessions) == 0 {
		return SessionRead{}, fmt.Errorf("agent %q has no sessions", req.Agent)
	}

	// Find the session by exact-or-prefix match across the agent's store.
	src, err := resolveSource(adapter, st.Sessions, req.SessionID)
	if err != nil {
		return SessionRead{}, err
	}

	turns, err := adapter.Turns(src, req.From, req.To)
	if err != nil {
		return SessionRead{}, err
	}

	// Cap the returned window at 40 turns / 64 KiB.
	const maxTurns = 40
	const maxBytes = 64 * 1024
	truncated := false
	if len(turns) > maxTurns {
		turns = turns[:maxTurns]
		truncated = true
	}
	total := 0
	kept := turns[:0]
	for _, t := range turns {
		total += len(t.Text)
		if total > maxBytes {
			truncated = true
			break
		}
		kept = append(kept, t)
	}

	return SessionRead{
		Agent:     req.Agent,
		SessionID: src.SessionID,
		Path:      src.Path,
		Project:   src.Project,
		Turns:     kept,
		Truncated: truncated,
	}, nil
}

// resolveSource finds a session source by exact-or-prefix id.
func resolveSource(adapter Adapter, paths []string, idOrPrefix string) (Source, error) {
	if idOrPrefix == "" {
		return Source{}, fmt.Errorf("session id is empty")
	}
	var exact, prefix []Source
	for _, path := range paths {
		srcs, err := adapter.Sources(path)
		if err != nil {
			continue
		}
		for _, src := range srcs {
			if src.SessionID == idOrPrefix {
				exact = append(exact, src)
			} else if strings.HasPrefix(src.SessionID, idOrPrefix) {
				prefix = append(prefix, src)
			}
		}
	}
	switch {
	case len(exact) == 1:
		return exact[0], nil
	case len(exact) > 1:
		return Source{}, fmt.Errorf("ambiguous session id %q matches %d sessions", idOrPrefix, len(exact))
	case len(prefix) == 1:
		return prefix[0], nil
	case len(prefix) > 1:
		return Source{}, fmt.Errorf("ambiguous session prefix %q matches %d sessions", idOrPrefix, len(prefix))
	default:
		return Source{}, fmt.Errorf("no session matching %q", idOrPrefix)
	}
}
