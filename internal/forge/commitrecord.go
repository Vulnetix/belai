package forge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/vulnetix/belai/internal/sanitize"
)

// CommitRecord is what the commit workflow keeps about one commit it made or
// refused: identifiers, counts and cleaned paths, never the message.
type CommitRecord struct {
	// Mode is "auto", "ask" or "manual" (the /commit command).
	Mode string `json:"mode"`
	// Outcome is "committed" or "refused".
	Outcome string `json:"outcome"`
	// Commit is the full lowercase hex id; empty when refused.
	Commit string `json:"commit,omitempty"`
	Branch string `json:"branch,omitempty"`
	// Reason is a harness-worded refusal reason, cleaned and short.
	Reason    string    `json:"reason,omitempty"`
	At        time.Time `json:"at"`
	Committed int       `json:"committed"`
	// Left are the paths the workflow did not commit, by why.
	Left LeftBehind `json:"left"`
}

// LeftBehind lists paths the workflow left alone, cleaned and capped.
type LeftBehind struct {
	Mixed       []string `json:"mixed,omitempty"`
	Withheld    []string `json:"withheld,omitempty"`
	PreExisting []string `json:"pre_existing,omitempty"`
	Unrelated   []string `json:"unrelated,omitempty"`
}

// maxRecordPaths caps each left-behind list and maxRecordEntries the records
// one session file keeps.
const (
	maxRecordPaths   = 100
	maxRecordEntries = 200
)

var recordSessionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// LeftOf builds the left-behind lists of a split, cleaned and capped.
func LeftOf(s Split) LeftBehind {
	clean := func(l []string) []string {
		var out []string
		for _, p := range l {
			if len(out) == maxRecordPaths {
				break
			}
			if sanitize.PathText(p) != nil {
				continue
			}
			out = append(out, sanitize.Line(p, 300))
		}
		sort.Strings(out)
		return out
	}
	return LeftBehind{Mixed: clean(s.Mixed), Withheld: clean(s.Withheld), PreExisting: clean(s.PreExisting), Unrelated: clean(s.Unrelated)}
}

type commitRecordFile struct {
	Session string         `json:"session"`
	Commits []CommitRecord `json:"commits"`
}

// AppendCommitRecord adds rec to <dir>/<session>.json. The directory is made
// 0700 and the file written 0600 through a temporary name and a rename; a
// symlinked directory or file is refused, and a session id that is not a plain
// identifier names no file. The write is best effort for the caller: an error
// never undoes the commit it describes.
func AppendCommitRecord(dir, session string, rec CommitRecord) error {
	if !recordSessionRE.MatchString(session) {
		return errors.New("commit record: session id is not a plain identifier")
	}
	if rec.Commit != "" && !fullHex(rec.Commit) {
		return errors.New("commit record: not a full commit id")
	}
	rec.Reason = sanitize.Line(rec.Reason, 200)
	rec.Branch = sanitize.Ident(rec.Branch, 100)
	if rec.At.IsZero() {
		rec.At = time.Now().UTC()
	}
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("commit record: the records directory is a symlink")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file := filepath.Join(dir, session+".json")
	cur := commitRecordFile{Session: session}
	if fi, err := os.Lstat(file); err == nil {
		if !fi.Mode().IsRegular() {
			return errors.New("commit record: the record is not a regular file")
		}
		if data, rerr := os.ReadFile(file); rerr == nil {
			var old commitRecordFile
			if json.Unmarshal(data, &old) == nil && old.Session == session {
				cur = old
			}
		}
	}
	cur.Commits = append(cur.Commits, rec)
	if n := len(cur.Commits); n > maxRecordEntries {
		cur.Commits = cur.Commits[n-maxRecordEntries:]
	}
	data, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".record-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

// fullHex reports whether s is a 40 or 64 digit lowercase hex commit id.
func fullHex(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
