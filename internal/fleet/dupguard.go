package fleet

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/forge"
)

// The duplicate guard (kanban/guard.go) needs to know which pull requests are
// still open. The forge is asked here, before a claim and never while the board
// lock is held, and the answer is kept for a couple of minutes: an open pull
// request changes state rarely, and the forge quota is the scarce resource.

// openPRTTL is how long a list of open pull requests is trusted. A failed
// listing is kept as long, so a forge outage is not asked about on every poll.
const openPRTTL = 2 * time.Minute

// listOpenCardPRs lists the repository's open pull requests from belai/
// branches. A test replaces it.
var listOpenCardPRs = func(ctx context.Context, repo string) ([]forge.PR, error) {
	repoRun := hardenedGit("", "")
	top, err := git(ctx, repoRun, repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	ident := identityEnv(ctx, repoRun, top)
	repoRun = hardenedGit("", "", ident...)
	origin, _ := git(ctx, repoRun, top, "remote", "get-url", "origin")
	rem, ok := forge.ParseRemote(origin)
	if !ok {
		return nil, errors.New("the repository has no GitHub or GitLab origin remote")
	}
	run := repoRun
	if env, _ := publishAuth(rem, time.Now()); len(env) > 0 {
		run = hardenedGit("", "", append(slices.Clone(ident), env...)...)
	}
	p, reason := forge.For(rem, run, exec.LookPath)
	if p == nil {
		return nil, errors.New(reason)
	}
	return p.OpenPRsWithPrefix(ctx, top, BranchPrefix)
}

// openPRList is one repository's last answer.
type openPRList struct {
	at   time.Time
	urls map[string]bool
	err  error
}

var (
	openPRMu    sync.Mutex
	openPRCache = map[string]openPRList{}
)

// openPRFunc reports whether a pull request url is still open, for the
// duplicate guard. It returns an error when the forge could not be asked, and
// the guard then holds a card back only for a claim.
func openPRFunc(ctx context.Context, repo string, now time.Time) (func(pr string) bool, error) {
	openPRMu.Lock()
	defer openPRMu.Unlock()
	l, ok := openPRCache[repo]
	if !ok || now.Sub(l.at) >= openPRTTL {
		fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		prs, err := listOpenCardPRs(fctx, repo)
		cancel()
		l = openPRList{at: now, err: err}
		if err == nil {
			l.urls = make(map[string]bool, len(prs))
			for _, pr := range prs {
				l.urls[pr.URL] = true
			}
		}
		openPRCache[repo] = l
	}
	if l.err != nil {
		return nil, l.err
	}
	return func(pr string) bool { return l.urls[pr] }, nil
}

// guardOpenPR is the guard's view of the forge for this worker: nil unless the
// worker publishes pull requests of its own, and nil when the forge could not
// be asked (logged once per failure).
func (w *Worker) guardOpenPR(ctx context.Context) func(pr string) bool {
	if !w.publishes() {
		return nil
	}
	open, err := openPRFunc(ctx, w.Repo, w.clock())
	if err != nil {
		if msg := err.Error(); msg != w.guardErr {
			w.guardErr = msg
			w.logf("duplicate guard: open pull requests unknown (%v); holding cards back for claims only", err)
		}
		return nil
	}
	w.guardErr = ""
	return open
}

// logHeld says, once per card, why a backlog card is not being taken.
func (w *Worker) logHeld(ctx context.Context) {
	held, err := w.Store.Held(w.guardOpenPR(ctx))
	if err != nil {
		return
	}
	for id, h := range held {
		if w.heldLogged[id] == h.By {
			continue
		}
		if w.heldLogged == nil {
			w.heldLogged = map[string]string{}
		}
		w.heldLogged[id] = h.By
		short := id
		if it, gerr := w.Store.Get(id); gerr == nil {
			short = it.Short()
		}
		w.logf("duplicate guard: %s waits, %s", short, h)
	}
	for id := range w.heldLogged {
		if _, still := held[id]; !still {
			delete(w.heldLogged, id)
		}
	}
}
