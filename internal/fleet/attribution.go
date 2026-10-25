package fleet

import (
	"context"
	"strings"

	"github.com/vulnetix/belai/internal/attribution"
)

// ensureAttribution adds the attribution trailers to the commits this branch holds that lack them,
// before the branch is published. Inside a Pix sandbox a global commit-msg hook adds them to a
// model's own `git commit`, and the harness adds them to its own; this covers a repository that
// sets its own core.hooksPath (husky) and so bypasses that hook. It does nothing outside a sandbox.
//
// The rewrite is a rebase of the branch's own commits onto Base, so each amended commit is signed
// again by the sandbox's signer. A failed rewrite is aborted and the branch is published as it was:
// missing trailers never block the work.
func (w *Workspace) ensureAttribution(ctx context.Context) {
	c, ok := attribution.ForSession(w.Session)
	if !ok || w.Base == "" {
		return
	}
	shas, err := git(ctx, w.run, w.Dir, "rev-list", "--reverse", w.Base+"..HEAD")
	if err != nil || shas == "" {
		return
	}
	missing := false
	for _, sha := range strings.Fields(shas) {
		if msg, err := git(ctx, w.run, w.Dir, "log", "-1", "--format=%B", sha); err == nil && attribution.Missing(msg, c) {
			missing = true
			break
		}
	}
	if !missing {
		return
	}
	amend := "git log -1 --format=%B | git interpret-trailers --if-exists addIfDifferent " +
		attribution.ShellTrailerArgs(c) + " | git commit --amend --allow-empty -q -F -"
	if _, err := git(ctx, w.run, w.Dir, "rebase", "--rebase-merges", "--exec", amend, w.Base); err != nil {
		_, _ = git(ctx, w.run, w.Dir, "rebase", "--abort")
	}
}
