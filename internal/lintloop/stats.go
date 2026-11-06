package lintloop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/vulnetix/belai/internal/config"
)

// statsVersion is the file's format version.
const statsVersion = 1

// maxStatsLen bounds the file read back.
const maxStatsLen = 64 * 1024

// ToolStats are the numbers kept for one linter. They are counts, an exit code
// and a duration: no path, rule name or message.
type ToolStats struct {
	Runs         int   `json:"runs"`
	WithFindings int   `json:"with_findings"`
	Findings     int   `json:"findings"`
	Skipped      int   `json:"skipped"`
	LastExit     int   `json:"last_exit"`
	LastMS       int64 `json:"last_ms"`
}

// Stats is the persisted file, keyed by the table's tool name.
type Stats struct {
	Version int                  `json:"version"`
	Tools   map[string]ToolStats `json:"tools"`
}

var statsMu sync.Mutex

// Record adds one run to the stats file for root
// (config.ProjectLintLoopPath). A run with no tool (nothing was detected) is
// not recorded. It refuses a symlinked directory or file, writes whole or not
// at all, and returns an error the caller may ignore: the loop never depends
// on it.
func Record(root string, rep Report) error {
	if rep.Tool == "" {
		return nil
	}
	path := config.ProjectLintLoopPath(root)
	dir := filepath.Dir(path)
	for _, d := range []string{filepath.Join(root, ".vulnetix"), dir} {
		if fi, err := os.Lstat(d); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("lintloop: %s is a symlink", d)
		}
	}
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("lintloop: %s is not a regular file", path)
	}
	statsMu.Lock()
	defer statsMu.Unlock()
	st := load(path)
	t := st.Tools[rep.Tool]
	if rep.Status == Skipped {
		t.Skipped++
	} else {
		t.Runs++
		if n := len(rep.Rows) + rep.Other; n > 0 {
			t.WithFindings++
			t.Findings += n
		}
		t.LastExit, t.LastMS = rep.ExitCode, rep.Duration.Milliseconds()
	}
	st.Tools[rep.Tool] = t
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ReadStats returns the stats for root, or the zero value when there are none
// or the file does not parse.
func ReadStats(root string) Stats { return load(config.ProjectLintLoopPath(root)) }

func load(path string) Stats {
	st := Stats{Version: statsVersion, Tools: map[string]ToolStats{}}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > maxStatsLen {
		return st
	}
	var in Stats
	if json.Unmarshal(data, &in) != nil || in.Version != statsVersion {
		return st
	}
	if in.Tools != nil && len(in.Tools) <= 16 {
		st.Tools = in.Tools
	}
	return st
}
