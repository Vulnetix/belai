package a2a

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/gitsync"
)

// DefaultDebounce is the least time between two broadcasts of changed work.
const DefaultDebounce = 10 * time.Second

// Broadcaster composes a session's broadcasts from what the harness already
// knows: the repository, branch and pull request from gitsync.Info, the files
// the session's tools changed (the TUI's diff observer), and a ticket (a
// fleet worker's claim, or a reference in the branch). No model writes any
// of it. It re-broadcasts when the work changes, at most once per Debounce.
type Broadcaster struct {
	Debounce time.Duration

	mu      sync.Mutex
	root    string
	work    Work
	claimed string // a ticket set by the harness, which outranks the branch
	sent    *Work
	sentAt  time.Time
	dirty   bool
}

// SetGit takes the repository, branch and pull request from a git reading.
// A nil reading keeps what was known.
func (b *Broadcaster) SetGit(info *gitsync.Info) {
	if info == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	w := b.work
	b.root = info.Root
	w.Repo = RepoKey(info.Remote, info.Root)
	w.Branch = info.Branch
	w.PR = 0
	if info.PR != nil && info.PR.State != "closed" && info.PR.State != "merged" {
		w.PR = info.PR.Number
	}
	w.Ticket = b.claimed
	if w.Ticket == "" {
		w.Ticket = TicketFromBranch(info.Branch)
	}
	b.set(w)
}

// SetTicket names the ticket the session works (a fleet worker's claimed
// card). Empty falls back to a reference in the branch.
func (b *Broadcaster) SetTicket(ticket string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.claimed = ticket
	w := b.work
	w.Ticket = ticket
	if ticket == "" {
		w.Ticket = TicketFromBranch(w.Branch)
	}
	b.set(w)
}

// AddFiles records files the session changed, newest last. An absolute path
// under the repository root is made relative to it; anything else that is not
// a clean relative path is dropped. The most recent MaxFiles are kept.
func (b *Broadcaster) AddFiles(paths ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	w := b.work
	files := slices.Clone(w.Files)
	for _, p := range paths {
		p = filepath.ToSlash(p)
		if filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
			if b.root == "" {
				continue
			}
			rel, err := filepath.Rel(b.root, filepath.FromSlash(p))
			if err != nil {
				continue
			}
			p = filepath.ToSlash(rel)
		}
		p = CleanPath(p)
		if p == "" {
			continue
		}
		if i := slices.Index(files, p); i >= 0 {
			files = slices.Delete(files, i, i+1)
		}
		files = append(files, p)
	}
	if len(files) > MaxFiles {
		files = files[len(files)-MaxFiles:]
	}
	w.Files = files
	b.set(w)
}

func (b *Broadcaster) set(w Work) {
	w = w.Clean()
	if !sameWork(w, b.work) {
		b.work = w
		b.dirty = true
	}
}

// Work is the current work.
func (b *Broadcaster) Work() Work {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.work
}

// Files is the files the session changed.
func (b *Broadcaster) Files() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.work.Files)
}

// Due returns the work to broadcast now, and false when nothing changed since
// the last broadcast or the debounce has not passed. A session that knows no
// repository yet broadcasts nothing. Due marks what it returns as sent.
func (b *Broadcaster) Due(now time.Time) (Work, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.work.Repo == "" || !b.dirty {
		return Work{}, false
	}
	if b.sent != nil && sameWork(*b.sent, b.work) {
		b.dirty = false
		return Work{}, false
	}
	d := b.Debounce
	if d <= 0 {
		d = DefaultDebounce
	}
	if b.sent != nil && now.Sub(b.sentAt) < d {
		return Work{}, false
	}
	w := b.work
	w.Files = slices.Clone(w.Files)
	b.sent, b.sentAt, b.dirty = &w, now, false
	return w, true
}

// Reset forgets what was sent, so the next Due broadcasts the work again (a
// new session took over the card).
func (b *Broadcaster) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent, b.dirty = nil, b.work.Repo != ""
}

func sameWork(a, b Work) bool {
	return a.Repo == b.Repo && a.Branch == b.Branch && a.PR == b.PR && a.Ticket == b.Ticket && slices.Equal(a.Files, b.Files)
}

// RepoKey is the repository as broadcasts name it: the remote's owner/name,
// or "path:" and 16 hex of the root path's SHA-256 when there is no remote,
// so two checkouts of one remote match and a path is never sent.
func RepoKey(remote, root string) string {
	if r := strings.Trim(remote, "/"); r != "" && repoShape.MatchString(r) && !strings.HasPrefix(r, "path:") {
		return strings.ToLower(r)
	}
	if root == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return "path:" + hex.EncodeToString(sum[:8])
}

var branchTicket = regexp.MustCompile(`(?:^|[/_-])([A-Z][A-Z0-9]{1,9}-[0-9]{1,7})(?:$|[/_-])`)

// TicketFromBranch is a ticket reference in a branch name (ABC-123), or "".
func TicketFromBranch(branch string) string {
	if m := branchTicket.FindStringSubmatch(branch); m != nil {
		return m[1]
	}
	return ""
}
