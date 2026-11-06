package review

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sanitize"
)

// maxStored bounds a stored review read back.
const maxStored = 1 << 20

var (
	repoRE = regexp.MustCompile(`^[0-9a-f]{16}$`)
	idRE   = regexp.MustCompile(`^(nohead|[0-9a-f]{40}|[0-9a-f]{64})(-[0-9a-f]{12})?$`)
	fileRE = regexp.MustCompile(`^[0-9a-f]{16}-(nohead|[0-9a-f]{40}|[0-9a-f]{64})(-[0-9a-f]{12})?\.json$`)
)

// ID names the state a result covers: the head commit, and for a working tree
// the digest of its changes. It is the store's key beside the repository.
func (r Result) ID() string { return idOf(r.HeadSHA, r.Digest) }

func idOf(head, digest string) string {
	id := head
	if id == "" {
		id = "nohead"
	}
	if digest != "" {
		id += "-" + digest
	}
	return id
}

// Store keeps finished reviews, one JSON file for each repository and head
// state, under the state directory (config.ReviewsDir), which the OS sandbox
// hides from every command. Files are mode 0600 and written atomically; the
// newest Keep are kept. A file read back is not trusted: it is checked field by
// field and one that fails is used for nothing.
type Store struct{ Dir string }

// OpenStore returns the store in the state directory.
func OpenStore() (*Store, error) {
	dir, err := config.ReviewsDir()
	if err != nil {
		return nil, err
	}
	return &Store{Dir: dir}, nil
}

func (s *Store) file(repo, id string) (string, error) {
	if s == nil || s.Dir == "" || !repoRE.MatchString(repo) || !idRE.MatchString(id) {
		return "", errors.New("review store: not a valid key")
	}
	return filepath.Join(s.Dir, repo+"-"+id+".json"), nil
}

// ensureDir makes the directory and refuses one that is a symbolic link.
func (s *Store) ensureDir() error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(s.Dir)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return errors.New("review store: the directory is not a plain directory")
	}
	return nil
}

// Load returns the stored review for a repository and head state, if there is
// one that passes every check.
func (s *Store) Load(repo, id string) (Result, bool) {
	path, err := s.file(repo, id)
	if err != nil {
		return Result{}, false
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxStored {
		return Result{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Result{}, false
	}
	var r Result
	if json.Unmarshal(b, &r) != nil || !r.valid(repo, id) {
		return Result{}, false
	}
	r.Cached = true
	return r, true
}

// valid is the check a stored result passes before it is believed.
func (r *Result) valid(repo, id string) bool {
	if r.Version != ResultVersion || r.Repo != repo || r.ID() != id || len(r.Findings) > MaxFindings {
		return false
	}
	switch r.Target.Kind {
	case KindWorkdir, KindBranch, KindPR:
	default:
		return false
	}
	ok := false
	for _, v := range Verdicts {
		ok = ok || r.Verdict == v
	}
	if !ok || r.PR < 0 {
		return false
	}
	for i := range r.Findings {
		f := &r.Findings[i]
		if !PlainPath(f.File) || f.Line < 1 || f.Severity.rank() < 0 || !validRubric(f.Rubric) ||
			f.Body == "" || len(f.Body) > MaxBodyBytes || len(f.Fix) > MaxFixBytes {
			return false
		}
		f.Body, f.Fix = sanitize.Line(f.Body, 0), sanitize.Line(f.Fix, 0)
	}
	r.Target.Ref, r.Target.Base = sanitize.Line(r.Target.Ref, 200), sanitize.Line(r.Target.Base, 200)
	return true
}

// Save writes r and prunes the store to the newest Keep reviews.
func (s *Store) Save(r Result) error {
	r.Version = ResultVersion
	if r.Created.IsZero() {
		r.Created = time.Now().UTC()
	}
	path, err := s.file(r.Repo, r.ID())
	if err != nil {
		return err
	}
	if err := s.ensureDir(); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmp := filepath.Join(s.Dir, ".tmp-"+hex.EncodeToString(rnd[:]))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	s.prune()
	return nil
}

// MarkPosted records that the review of repo and id went to pull request pr.
func (s *Store) MarkPosted(repo, id string, pr int) error {
	r, ok := s.Load(repo, id)
	if !ok {
		return errors.New("review store: the review to mark is not stored")
	}
	r.Posted = append(r.Posted, Posted{PR: pr, At: time.Now().UTC()})
	return s.Save(r)
}

// WasPosted reports whether the review of repo and id was already posted to
// pull request pr from this host.
func (s *Store) WasPosted(repo, id string, pr int) bool {
	r, ok := s.Load(repo, id)
	if !ok {
		return false
	}
	for _, p := range r.Posted {
		if p.PR == pr {
			return true
		}
	}
	return false
}

// prune removes all but the newest Keep review files. It touches only names
// this package writes, and never follows a link.
func (s *Store) prune() {
	ents, err := os.ReadDir(s.Dir)
	if err != nil {
		return
	}
	type item struct {
		name string
		mod  time.Time
	}
	var items []item
	for _, e := range ents {
		if !fileRE.MatchString(e.Name()) {
			continue
		}
		fi, err := os.Lstat(filepath.Join(s.Dir, e.Name()))
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		items = append(items, item{e.Name(), fi.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].mod.Equal(items[j].mod) {
			return items[i].mod.After(items[j].mod)
		}
		return items[i].name < items[j].name
	})
	for i := Keep; i < len(items); i++ {
		_ = os.Remove(filepath.Join(s.Dir, items[i].name))
	}
}
