package changes

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/vulnetix/belai/internal/forge"
)

// BranchPrefix is where a teleport branch lives on the forge. The branch is the
// origin host's alone, named for the teleport, and is never a user's branch.
const BranchPrefix = "belai/teleport/"

var teleportID = regexp.MustCompile(`^[0-9a-f]{8}`)

// Pushed is what a push put on the forge: a commit holding the origin's final
// state (its tree, on top of its HEAD) under a branch the target can fetch.
type Pushed struct {
	Branch string
	Commit string
}

// ErrNoRemote means the repository has no origin remote that names a forge, so
// there is nowhere to coordinate.
var ErrNoRemote = errors.New("the repository has no origin remote on a forge")

// Push commits the snapshot's tree on top of its head, without touching the
// checkout, and pushes that one commit to origin as the new branch
// belai/teleport/<id>, by an explicit refspec. Everything it needs is the
// snapshot and the teleport's id; nothing a caller sends is a refspec. A push
// the forge refuses returns its (cleaned) reason. The caller has already decided
// the push is allowed (docs/teleport.md): this function never asks.
func Push(ctx context.Context, mk Runners, dir string, s Snapshot, teleport string) (Pushed, error) {
	if mk == nil {
		mk = Hardened
	}
	id := teleportID.FindString(strings.ToLower(teleport))
	if id == "" {
		return Pushed{}, errors.New("not a teleport id")
	}
	if !shaShape.MatchString(s.Head) || !shaShape.MatchString(s.Tree) {
		return Pushed{}, errors.New("the snapshot has no commit or tree")
	}
	g := mk()
	root, err := run(ctx, g, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return Pushed{}, fmt.Errorf("not a git repository: %s", forge.CleanErr(err))
	}
	url, err := run(ctx, g, root, "config", "--get", "remote.origin.url")
	if err != nil {
		return Pushed{}, ErrNoRemote
	}
	if rem, ok := forge.ParseRemote(url); !ok || rem.Slug() == "" {
		return Pushed{}, ErrNoRemote
	}
	branch := BranchPrefix + id
	if err := forge.ValidBranchName(ctx, g, root, branch); err != nil {
		return Pushed{}, err
	}
	// The commit's author is Belai's, so it is plain that the harness made it.
	ident := mk("GIT_AUTHOR_NAME=Belai teleport", "GIT_AUTHOR_EMAIL=teleport@vulnetix.invalid",
		"GIT_COMMITTER_NAME=Belai teleport", "GIT_COMMITTER_EMAIL=teleport@vulnetix.invalid")
	commit, err := run(ctx, ident, root, "commit-tree", s.Tree, "-p", s.Head, "-m", "belai teleport "+id)
	if err != nil || !shaShape.MatchString(commit) {
		return Pushed{}, fmt.Errorf("commit the changes: %s", forge.CleanErr(err))
	}
	if _, err := run(ctx, g, root, "push", "--porcelain", "--no-follow-tags", "--quiet", "origin", commit+":refs/heads/"+branch); err != nil {
		return Pushed{}, fmt.Errorf("the push was refused: %s", forge.CleanErr(err))
	}
	return Pushed{Branch: branch, Commit: commit}, nil
}
