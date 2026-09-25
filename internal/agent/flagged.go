package agent

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/tools"
)

// flaggedFiles remembers, for one session, the files whose Read result the
// classifier did not clear. Grep is a shaped tool and stays sanitize-only, but
// its path:line:text rows carry the file's own lines: after a Read of a file
// was withheld, a Grep over the same file handed the matched lines back
// unclassified. Grep rows from a flagged file are therefore withheld too. Only
// files already flagged are covered; Grep over an unread file is unchanged.
type flaggedFiles struct {
	mu    sync.Mutex
	paths map[string]rolemanager.Sentinel // resolved absolute path → verdict
}

// flag records the verdict for a Read result that was not cleared. The path
// comes from the Read tool's render-only "abs" meta, which is the resolved,
// symlink-evaluated absolute path the tool opened.
func (f *flaggedFiles) flag(res tools.Result, verdict rolemanager.Sentinel) {
	if res.Kind != tools.KindRead {
		return
	}
	abs, _ := res.Meta["abs"].(string)
	if abs == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.paths == nil {
		f.paths = map[string]rolemanager.Sentinel{}
	}
	f.paths[abs] = verdict
}

// withholdGrep drops the Grep rows whose file is flagged and appends one line
// per such file naming the verdict. root and dir are the primary session root
// and the working directory a relative row path may be relative to.
func (f *flaggedFiles) withholdGrep(content, root, dir string) string {
	f.mu.Lock()
	flagged := len(f.paths) > 0
	f.mu.Unlock()
	if !flagged {
		return content
	}
	var kept []string
	dropped := map[string]int{}
	verdicts := map[string]rolemanager.Sentinel{}
	for _, line := range strings.Split(content, "\n") {
		p, _, ok := strings.Cut(line, ":")
		if ok && p != "" {
			if v, hit := f.lookup(p, root, dir); hit {
				dropped[p]++
				verdicts[p] = v
				continue
			}
		}
		kept = append(kept, line)
	}
	if len(dropped) == 0 {
		return content
	}
	names := make([]string, 0, len(dropped))
	for p := range dropped {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		kept = append(kept, fmt.Sprintf("[%d matching lines from %s withheld: an earlier read of this file was classified %s]", dropped[p], p, verdicts[p].Label()))
	}
	return strings.Join(kept, "\n")
}

// lookup resolves a Grep row path the way the Read tool resolved the flagged
// one — absolute, or relative to the session root or working directory, with
// symlinks evaluated — and reports its verdict.
func (f *flaggedFiles) lookup(p, root, dir string) (rolemanager.Sentinel, bool) {
	var candidates []string
	if filepath.IsAbs(p) {
		candidates = append(candidates, p)
	} else {
		candidates = append(candidates, filepath.Join(root, p), filepath.Join(dir, p))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range candidates {
		c = filepath.Clean(c)
		if real, err := filepath.EvalSymlinks(c); err == nil {
			c = real
		}
		if v, ok := f.paths[c]; ok {
			return v, true
		}
	}
	return "", false
}
