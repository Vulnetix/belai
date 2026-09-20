package agentstore

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"regexp"
)

// maxMemoryFileBytes is the largest memory file read whole (SearchMemory with
// file, or a single-file scan). Larger files are streamed line by line.
const maxMemoryFileBytes = 64 * 1024

// MemoryFiles returns the discovered memory/rules files for one agent ("" for
// every present agent), in registry order.
func (r *Registry) MemoryFiles(agent string) ([]string, error) {
	r.probeOnce()
	var out []string
	for _, st := range r.status {
		if agent != "" && st.Name != agent {
			continue
		}
		out = append(out, st.Memory...)
	}
	return out, nil
}

// SearchMemory searches discovered memory files for a regex. With File set it
// returns that whole file (capped); otherwise it returns path:line: text hits
// with surrounding context lines.
func (r *Registry) SearchMemory(ctx context.Context, q MemoryQuery) (MemorySearch, error) {
	files, err := r.MemoryFiles(q.Agent)
	if err != nil {
		return MemorySearch{}, err
	}
	if q.File != "" {
		if !containsString(files, q.File) {
			return MemorySearch{}, fmt.Errorf("file %q is not in the probed memory set", q.File)
		}
		data, err := os.ReadFile(q.File)
		if err != nil {
			return MemorySearch{}, err
		}
		content := string(data)
		truncated := false
		if len(content) > maxMemoryFileBytes {
			content = content[:maxMemoryFileBytes]
			truncated = true
		}
		return MemorySearch{Files: []string{q.File}, Content: content, Truncated: truncated}, nil
	}

	maxMatches := q.MaxMatches
	if maxMatches <= 0 {
		maxMatches = DefaultCaps().MaxMatches
	}
	ctxLines := q.ContextLines
	if ctxLines < 0 {
		ctxLines = 0
	}
	if ctxLines > 20 {
		ctxLines = 20
	}

	res := MemorySearch{Files: files}
	var hits []MemoryHit
	truncated := false
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return MemorySearch{}, err
		}
		if len(hits) >= maxMatches {
			truncated = true
			break
		}
		fileHits, trunc, err := searchMemoryFile(ctx, path, q.Re, ctxLines, maxMatches-len(hits))
		if err != nil {
			continue
		}
		hits = append(hits, fileHits...)
		truncated = truncated || trunc
	}
	res.Hits = hits
	res.Truncated = truncated
	return res, nil
}

// searchMemoryFile searches one memory file, returning up to max matches with
// surrounding context lines.
func searchMemoryFile(ctx context.Context, path string, re *regexp.Regexp, ctxLines, max int) ([]MemoryHit, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, false, err
	}

	var hits []MemoryHit
	truncated := false
	emitted := map[int]bool{}
	for i, line := range lines {
		if err := ctx.Err(); err != nil {
			return hits, false, err
		}
		if re.FindStringIndex(line) == nil {
			continue
		}
		start := i - ctxLines
		if start < 0 {
			start = 0
		}
		end := i + ctxLines
		if end >= len(lines) {
			end = len(lines) - 1
		}
		for ln := start; ln <= end; ln++ {
			if emitted[ln] {
				continue
			}
			if len(hits) >= max {
				truncated = true
				return hits, truncated, nil
			}
			emitted[ln] = true
			hits = append(hits, MemoryHit{Path: path, Line: ln + 1, Text: lines[ln]})
		}
	}
	return hits, truncated, nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
