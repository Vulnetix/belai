package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// MaxBundleBytes caps the git bundle a worker hands to the forge coordinator.
// A bigger branch is not handed over; the item gets a harness note instead.
const MaxBundleBytes = 16 << 20

// maxBundleBytes is the cap Escalate applies; a test lowers it.
var maxBundleBytes int64 = MaxBundleBytes

// Forge request kinds (BelaiForgeRequest.kind).
const (
	ForgeKindPush        = "push"
	ForgeKindPullRequest = "pull_request"
)

// forgeBranch is the only branch shape the coordinator takes: an item's own
// attempt branch.
var forgeBranch = regexp.MustCompile(`^belai/K-[0-9a-f]{6}/a[0-9]+$`)

var (
	hex16 = regexp.MustCompile(`^[0-9a-f]{16}$`)
	hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// forgeIdent is an item id, a worker id or a repository owner or name as a
	// forge request carries it.
	forgeIdent = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
)

// ValidRequestID reports whether id is a forge request id: 16 hex characters.
// The coordinator builds the bundle's path from it, so nothing else is one.
func ValidRequestID(id string) bool { return hex16.MatchString(id) }

// forgeFailures are the infrastructure failure words a request may carry.
var forgeFailures = map[string]bool{
	forge.FailureAuth: true, forge.FailureRateLimited: true, forge.FailureServer: true,
	forge.FailureNoGH: true, forge.FailurePushCredentials: true,
}

// ValidForgeRequest checks every field of a forge request against the
// contract's shapes. The relay checks a spool file with it before it posts one.
func ValidForgeRequest(r sessionsync.ForgeRequest) error {
	switch {
	case !ValidRequestID(r.RequestID):
		return errors.New("requestId is not 16 hex characters")
	case !forgeIdent.MatchString(r.ItemID):
		return errors.New("itemId is not an identifier")
	case !ValidID(r.WorkerID):
		return errors.New("workerId is not a worker id")
	case r.Kind != ForgeKindPush && r.Kind != ForgeKindPullRequest && r.Kind != "issue_sync" && r.Kind != "commit":
		return errors.New("kind is not a forge request kind")
	case !forgeIdent.MatchString(r.RepoOwner) || !forgeIdent.MatchString(r.RepoName):
		return errors.New("the repository is not an owner and a name")
	case !forgeBranch.MatchString(r.Branch):
		return errors.New("branch is not an item attempt branch")
	case !hex40.MatchString(r.BaseSHA) || !hex40.MatchString(r.HeadSHA):
		return errors.New("baseSha and headSha must be 40 hex characters")
	case r.BundleSHA256 != "" && !hex64.MatchString(r.BundleSHA256):
		return errors.New("bundleSha256 is not 64 hex characters")
	case r.BundleBytes < 0 || r.BundleBytes > MaxBundleBytes:
		return errors.New("bundleBytes is out of range")
	case len(r.Title) > 480 || r.Title != sanitize.Line(r.Title, 480):
		return errors.New("title is not one clean line")
	case !forgeFailures[r.Failure]:
		return errors.New("failure is not an infrastructure failure")
	}
	return nil
}

// Coordinator is what a workspace needs to hand its branch to the forge
// coordinator: where the spool lives, whose request it is, and the item's
// title for the pull request. Note, when set, writes a harness note on the
// item (a branch too big to hand over).
type Coordinator struct {
	Registry string
	Worker   string
	Item     string
	Title    string
	Note     func(string)
}

// pendingRequest is the last request a workspace filed.
type pendingRequest struct {
	id, kind, head string
}

// EscalatedError is a push or pull request that failed for the machine's or
// the forge's reason and was handed to the forge coordinator as request
// RequestID. It is tools.PublishHandoff, so PublishBranch answers the model
// with the harness's sentence instead of the failure's text.
type EscalatedError struct {
	RequestID string
	Failure   string
	Kind      string
	cause     error
}

func (e *EscalatedError) Error() string {
	return "publishing handed to the coordinator (request " + e.RequestID + ")"
}

// Unwrap is the push failure the request stands in for.
func (e *EscalatedError) Unwrap() error { return e.cause }

// HandoffRequest is the coordinator request id.
func (e *EscalatedError) HandoffRequest() string { return e.RequestID }

// ErrBundleTooLarge is a branch whose bundle is over MaxBundleBytes.
var ErrBundleTooLarge = errors.New("the branch's bundle is over 16 MiB, too large to hand to the forge coordinator")

// SetCoordinator lets the workspace hand its branch to the forge coordinator
// when a push fails for an infrastructure reason. nil turns that off.
func (w *Workspace) SetCoordinator(c *Coordinator) { w.coord = c }

// Filed reports whether request id is one this workspace filed.
func (w *Workspace) Filed(id string) bool {
	if w == nil {
		return false
	}
	w.filedMu.Lock()
	defer w.filedMu.Unlock()
	return w.filed[id]
}

// handOff hands the branch to the forge coordinator when err is an
// infrastructure fault and a coordinator is set, and returns the
// *EscalatedError; otherwise, a work fault included, it returns err.
func (w *Workspace) handOff(ctx context.Context, kind string, err error) error {
	f := forge.ClassifyPushError(err)
	if !f.Infra() || w.coord == nil {
		return err
	}
	id, eerr := w.Escalate(ctx, kind, f.Failure)
	if eerr != nil {
		if errors.Is(eerr, ErrBundleTooLarge) && w.coord.Note != nil {
			w.coord.Note("publishing was not handed to the forge coordinator: " + ErrBundleTooLarge.Error())
		}
		return err
	}
	return &EscalatedError{RequestID: id, Failure: f.Failure, Kind: kind, cause: err}
}

// Escalate hands the item's branch to the forge coordinator: it writes the git
// bundle of base..refs/heads/<branch> to <state dir>/forge/<requestId>.bundle,
// where the coordinator reads it, and spools the request for belai rc to file
// (<registry>/<worker>.forge/<requestId>.json, the contract's POST body). Both
// files are 0600 and written atomically. A branch already handed over at the
// same commit returns the same request id without filing again.
func (w *Workspace) Escalate(ctx context.Context, kind, failure string) (string, error) {
	c := w.coord
	switch {
	case c == nil:
		return "", errors.New("no forge coordinator is set")
	case !w.Worktree:
		return "", errors.New("escalating needs a worktree branch")
	case !forgeBranch.MatchString(w.Branch):
		return "", fmt.Errorf("branch %s is not an item attempt branch", w.Branch)
	case !forgeFailures[failure]:
		return "", errors.New("not an infrastructure failure")
	}
	if err := w.intact(); err != nil {
		return "", err
	}
	rem, err := w.originRemote(ctx)
	if err != nil {
		return "", err
	}
	if rem.Kind != forge.KindGitHub {
		return "", errors.New("the forge coordinator publishes to GitHub only")
	}
	if err := w.absorbObjects(); err != nil {
		return "", err
	}
	ref := "refs/heads/" + w.Branch
	head, err := git(ctx, w.run, w.Dir, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	if !hex40.MatchString(head) || !hex40.MatchString(w.Base) {
		return "", errors.New("the branch's commits are not SHA-1 ids")
	}
	if p := w.pending; p != nil && p.head == head && p.kind == kind {
		return p.id, nil
	}

	id := randHex(8)
	dir, err := config.ForgeDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	bundle := filepath.Join(dir, id+".bundle")
	tmp := filepath.Join(dir, "."+id+".bundle.tmp")
	if _, err := git(ctx, w.run, w.Dir, "bundle", "create", tmp, w.Base+".."+ref); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("bundle the branch: %w", err)
	}
	size, sum, err := hashFile(tmp, maxBundleBytes)
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, bundle); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}

	req := sessionsync.ForgeRequest{
		RequestID: id, ItemID: c.Item, WorkerID: c.Worker, Kind: kind,
		RepoOwner: rem.Owner, RepoName: rem.Repo, Branch: w.Branch,
		BaseSHA: w.Base, HeadSHA: head, BundleSHA256: sum, BundleBytes: size,
		Title: ForgeTitle(c.Title), Failure: failure,
	}
	if err := ValidForgeRequest(req); err != nil {
		_ = os.Remove(bundle)
		return "", fmt.Errorf("forge request: %w", err)
	}
	if err := writeSpool(c.Registry, c.Worker, req); err != nil {
		_ = os.Remove(bundle)
		return "", err
	}
	w.pending = &pendingRequest{id: id, kind: kind, head: head}
	w.filedMu.Lock()
	if w.filed == nil {
		w.filed = map[string]bool{}
	}
	w.filed[id] = true
	w.filedMu.Unlock()
	return id, nil
}

// ForgeTitle is the pull request title a forge request carries: the item's
// title, cleaned to one line of at most 120 characters.
func ForgeTitle(title string) string {
	t := sanitize.Line(kanban.CleanTitle(title), 480)
	if r := []rune(t); len(r) > 120 {
		t = strings.TrimSpace(string(r[:120]))
	}
	return t
}

// hashFile returns a file's size and SHA-256, refusing one over max bytes.
func hashFile(path string, max int64) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, max+1))
	if err != nil {
		return 0, "", err
	}
	if n > max {
		return 0, "", ErrBundleTooLarge
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// SpoolDir is where worker id's forge requests wait for belai rc to file them.
func SpoolDir(registry, worker string) string {
	return filepath.Join(registry, worker+".forge")
}

// writeSpool writes a request to the worker's spool, atomically and 0600.
func writeSpool(registry, worker string, req sessionsync.ForgeRequest) error {
	if !ValidID(worker) || registry == "" {
		return errors.New("no spool for that worker")
	}
	dir := SpoolDir(registry, worker)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return writeAtomic(dir, req.RequestID+".json", data)
}

// writeAtomic writes data to dir/name through a temporary file and a rename,
// mode 0600.
func writeAtomic(dir, name string, data []byte) error {
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}
