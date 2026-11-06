// Package checkpoint keeps bounded copies of the files a session is about to
// change, so the user can put them back with /undo and /rewind
// (docs/checkpoints.md).
//
// The store is the user's own and nothing reads it for a model. It lives under
// the state directory (config.CheckpointsDir), which the OS sandbox hides and
// the file tools refuse. A checkpoint is one user turn: the first time a turn
// touches a path the Tracker records what the path held (its bytes, its
// absence, or the reason its bytes were not kept), and later touches in the
// same turn record nothing. Restoring to a checkpoint puts every path touched
// since back to the state it had before that turn.
//
// A file is copied only when a model could have read it. A path a Read deny
// rule covers, a credential file by name, a binary, a symbolic link, a file
// over [MaxFileBytes] and anything that would overflow the store is recorded by
// name only and listed as not restorable. Restoring is the user's decision,
// writes through a temporary file and a rename, refuses any path that resolves
// outside the confinement roots or that a deny rule covers, and first takes a
// safety checkpoint of what it is about to overwrite, so a restore can itself
// be undone.
package checkpoint

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/locate"
	"github.com/vulnetix/belai/internal/sanitize"
)

// MaxFileBytes is the largest file a checkpoint copies. A larger file is
// recorded by name and is not restorable.
const MaxFileBytes = 20 << 20

// maxLabel caps the cleaned prompt line a checkpoint carries.
const maxLabel = 80

// Kinds of checkpoint. A turn checkpoint is made by a user's prompt; a restore
// checkpoint is the safety copy a restore takes of what it overwrites.
const (
	KindTurn    = "turn"
	KindRestore = "restore"
)

// Reasons a path was recorded by name only. They are harness text.
const (
	WhySymlink    = "symbolic link"
	WhySpecial    = "not a regular file"
	WhyLarge      = "over the per-file size limit"
	WhyBinary     = "binary file"
	WhyDenied     = "a Read rule withholds it"
	WhySensitive  = "credential file"
	WhyStoreFull  = "checkpoint store is full"
	WhyUnknown    = "its earlier content could not be captured"
	WhyOutside    = "outside the working directory"
	WhyRestoreDny = "a Write rule denies it"
)

// Limits bound one session's store.
type Limits struct {
	// MaxPerSession is the most checkpoints kept; the oldest are dropped.
	MaxPerSession int
	// MaxBytes is the most file bytes kept; the oldest checkpoints are dropped
	// to fit a new copy.
	MaxBytes int64
}

// Policy is what the session knows that this package cannot.
type Policy struct {
	// Within reports whether an absolute, symlink-resolved path lies in a
	// confinement root. A nil Within refuses every restore.
	Within func(abs string) bool
	// CopyDenied returns a reason when a file's bytes must not be copied (a
	// Read rule withholds it), or "". Nil allows.
	CopyDenied func(abs string) string
	// RestoreDenied reports whether a Write or Edit deny rule covers the path,
	// so a restore must not write it. Nil allows.
	RestoreDenied func(abs string) bool
}

// Tracker is one session's checkpoint store. It is safe for concurrent use,
// though mutating tools reach it one at a time.
type Tracker struct {
	mu   sync.Mutex
	dir  string
	lim  Limits
	pol  Policy
	cps  []*cp
	seq  int
	cur  *cp
	mark turnMark
	// touched holds the paths already recorded in the open checkpoint.
	touched map[string]bool
	// pending holds gap notes seen while no checkpoint was open. A turn that
	// changes nothing the store can see leaves no checkpoint, so a note waits
	// for the first file the turn does record.
	pending []string
	blobs   map[string]int64
	failed  error
	now     func() time.Time
}

type turnMark struct {
	entry, label string
	set          bool
}

// cp is one checkpoint in memory.
type cp struct {
	id, kind, entry, label string
	seq                    int
	at                     int64
	files                  []fileRec
	gaps                   []string
	undone                 bool
}

// fileRec is what one path held when a checkpoint first touched it.
type fileRec struct {
	Path  string
	State string // stateFile, stateAbsent or stateSkipped
	SHA   string
	Mode  uint32
	Size  int64
	Why   string
}

const (
	stateFile    = "file"
	stateAbsent  = "absent"
	stateSkipped = "skipped"
)

// Info describes one checkpoint for a picker.
type Info struct {
	ID    string
	Seq   int
	Kind  string
	Entry string // the session entry of the prompt that began the turn, or ""
	Label string // the cleaned first line of that prompt
	At    time.Time
	// Files is the number of paths the checkpoint touched, Restorable how many
	// of them it can put back, Gaps how many parts of a command's effects it
	// could not see.
	Files, Restorable, Gaps int
	Undone                  bool
}

// Open opens (creating it with mode 0700) the store at dir, which must come
// from config.CheckpointsDir. An index line that does not validate is skipped.
func Open(dir string, lim Limits, pol Policy) (*Tracker, error) {
	if dir == "" {
		return nil, errors.New("checkpoint: no store directory")
	}
	if lim.MaxPerSession < 1 || lim.MaxBytes < 1 {
		return nil, errors.New("checkpoint: limits must be positive")
	}
	t := &Tracker{dir: dir, lim: lim, pol: pol, touched: map[string]bool{}, blobs: map[string]int64{}, now: time.Now}
	if err := os.MkdirAll(t.blobDir(), 0o700); err != nil {
		return nil, fmt.Errorf("checkpoint: %w", err)
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return nil, errors.New("checkpoint: the store is not a directory")
	}
	if err := t.load(); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *Tracker) blobDir() string   { return filepath.Join(t.dir, "blobs") }
func (t *Tracker) indexPath() string { return filepath.Join(t.dir, "index.jsonl") }

// Dir is the store directory.
func (t *Tracker) Dir() string { return t.dir }

// SetPolicy replaces the policy; the session calls it when its roots or rules
// change.
func (t *Tracker) SetPolicy(p Policy) {
	t.mu.Lock()
	t.pol = p
	t.mu.Unlock()
}

// SetLimits replaces the limits.
func (t *Tracker) SetLimits(l Limits) {
	if l.MaxPerSession < 1 || l.MaxBytes < 1 {
		return
	}
	t.mu.Lock()
	t.lim = l
	t.mu.Unlock()
}

// ---- index ------------------------------------------------------------------

// rec is one index line.
type rec struct {
	T     string `json:"t"`
	CP    string `json:"cp,omitempty"`
	ID    string `json:"id,omitempty"`
	Seq   int    `json:"seq,omitempty"`
	Kind  string `json:"kind,omitempty"`
	Entry string `json:"entry,omitempty"`
	Label string `json:"label,omitempty"`
	At    int64  `json:"at,omitempty"`
	Path  string `json:"path,omitempty"`
	State string `json:"state,omitempty"`
	SHA   string `json:"sha,omitempty"`
	Mode  uint32 `json:"mode,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Why   string `json:"why,omitempty"`
}

func validID(s string) bool {
	if len(s) < 2 || len(s) > 12 || s[0] != 'c' {
		return false
	}
	_, err := strconv.Atoi(s[1:])
	return err == nil
}

func validSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && s == strings.ToLower(s)
}

// validEntryID accepts the shape of a session entry id and nothing else.
func validEntryID(s string) bool {
	if len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func validPath(p string) bool {
	return p != "" && filepath.IsAbs(p) && filepath.Clean(p) == p && sanitize.PathText(p) == nil
}

func (t *Tracker) load() error {
	f, err := os.Open(t.indexPath())
	if errors.Is(err, fs.ErrNotExist) {
		return t.scanBlobs()
	}
	if err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}
	defer f.Close()
	byID := map[string]*cp{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var r rec
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		switch r.T {
		case "cp":
			if !validID(r.ID) || (r.Kind != KindTurn && r.Kind != KindRestore) || r.Seq < 1 || byID[r.ID] != nil {
				continue
			}
			if r.Entry != "" && !validEntryID(r.Entry) {
				r.Entry = ""
			}
			c := &cp{id: r.ID, kind: r.Kind, entry: r.Entry, label: sanitize.Line(r.Label, maxLabel), seq: r.Seq, at: r.At}
			byID[r.ID] = c
			t.cps = append(t.cps, c)
			if r.Seq > t.seq {
				t.seq = r.Seq
			}
		case "f":
			c := byID[r.CP]
			if c == nil || !validPath(r.Path) || config.UnderCheckpoints(r.Path) {
				continue
			}
			switch r.State {
			case stateFile:
				if !validSHA(r.SHA) {
					continue
				}
			case stateAbsent, stateSkipped:
			default:
				continue
			}
			c.files = append(c.files, fileRec{Path: r.Path, State: r.State, SHA: r.SHA, Mode: r.Mode & 0o777, Size: r.Size, Why: sanitize.Line(r.Why, 80)})
		case "gap":
			if c := byID[r.CP]; c != nil {
				c.gaps = append(c.gaps, sanitize.Line(r.Why, 120))
			}
		case "undone":
			if c := byID[r.CP]; c != nil {
				c.undone = true
			}
		}
	}
	sort.SliceStable(t.cps, func(i, j int) bool { return t.cps[i].seq < t.cps[j].seq })
	return t.scanBlobs()
}

// scanBlobs learns the size of every blob on disk so the budget survives a
// restart.
func (t *Tracker) scanBlobs() error {
	ents, err := os.ReadDir(t.blobDir())
	if err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}
	for _, e := range ents {
		if !validSHA(e.Name()) {
			continue
		}
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			t.blobs[e.Name()] = info.Size()
		}
	}
	return nil
}

// appendRecs appends lines to the index with mode 0600.
func (t *Tracker) appendRecs(rs ...rec) error {
	var buf bytes.Buffer
	for _, r := range rs {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if fi, err := os.Lstat(t.indexPath()); err == nil && !fi.Mode().IsRegular() {
		return errors.New("the index is not a regular file")
	}
	f, err := os.OpenFile(t.indexPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(buf.Bytes())
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// rewrite replaces the index with the in-memory state, through a temporary file.
func (t *Tracker) rewrite() error {
	var buf bytes.Buffer
	put := func(r rec) {
		b, _ := json.Marshal(r)
		buf.Write(b)
		buf.WriteByte('\n')
	}
	for _, c := range t.cps {
		put(rec{T: "cp", ID: c.id, Seq: c.seq, Kind: c.kind, Entry: c.entry, Label: c.label, At: c.at})
		for _, f := range c.files {
			put(rec{T: "f", CP: c.id, Path: f.Path, State: f.State, SHA: f.SHA, Mode: f.Mode, Size: f.Size, Why: f.Why})
		}
		for _, g := range c.gaps {
			put(rec{T: "gap", CP: c.id, Why: g})
		}
		if c.undone {
			put(rec{T: "undone", CP: c.id})
		}
	}
	return atomicWrite(t.indexPath(), buf.Bytes(), 0o600)
}

// ---- turns ------------------------------------------------------------------

// BeginTurn marks the start of a user turn. No checkpoint exists until the turn
// first touches a file, so a turn that edits nothing leaves nothing behind.
// entry is the session entry of the prompt and label its text; both are cleaned.
func (t *Tracker) BeginTurn(entry, label string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cur = nil
	t.touched = map[string]bool{}
	t.pending = nil
	if !validEntryID(entry) {
		entry = ""
	}
	t.mark = turnMark{entry: entry, label: sanitize.Line(label, maxLabel), set: true}
}

// Seal closes the open checkpoint. Touches after it open a new one only if a
// turn is begun.
func (t *Tracker) Seal() {
	t.mu.Lock()
	t.cur = nil
	t.touched = map[string]bool{}
	t.pending = nil
	t.mark = turnMark{}
	t.mu.Unlock()
}

// Ready reports whether the store can take a copy now. A mutating call checks
// it before it runs, so a store that cannot be written withholds the call
// instead of letting it change files with nothing to restore them from.
func (t *Tracker) Ready() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failed != nil {
		return t.failed
	}
	probe := filepath.Join(t.dir, ".ready")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		t.failed = fmt.Errorf("checkpoint store is not writable: %w", err)
		return t.failed
	}
	_ = os.Remove(probe)
	return nil
}

// openLocked returns the open checkpoint, creating it from the turn mark.
func (t *Tracker) openLocked(kind, entry, label string) (*cp, error) {
	if t.cur != nil && t.cur.kind == kind {
		return t.cur, nil
	}
	if kind == KindTurn && !t.mark.set {
		// A touch with no turn begun (a tool run outside a prompt) still has a
		// checkpoint, with no conversation point to return to.
		t.mark = turnMark{set: true}
	}
	if kind == KindTurn {
		entry, label = t.mark.entry, t.mark.label
	}
	t.seq++
	c := &cp{id: "c" + strconv.Itoa(t.seq), kind: kind, entry: entry, label: sanitize.Line(label, maxLabel), seq: t.seq, at: t.now().UnixMilli()}
	if err := t.appendRecs(rec{T: "cp", ID: c.id, Seq: c.seq, Kind: c.kind, Entry: c.entry, Label: c.label, At: c.at}); err != nil {
		t.failed = fmt.Errorf("checkpoint store: %w", err)
		return nil, t.failed
	}
	t.cps = append(t.cps, c)
	t.cur = c
	if kind == KindTurn {
		pend := t.pending
		t.pending = nil
		if err := t.noteGapsLocked(c, pend); err != nil {
			return nil, err
		}
	}
	if err := t.enforceCountLocked(c); err != nil {
		return nil, err
	}
	return c, nil
}

// enforceCountLocked drops the oldest checkpoints beyond the limit, never the
// open one.
func (t *Tracker) enforceCountLocked(keep *cp) error {
	dropped := false
	for len(t.cps) > t.lim.MaxPerSession && t.cps[0] != keep {
		t.cps = t.cps[1:]
		dropped = true
	}
	if !dropped {
		return nil
	}
	return t.settleLocked()
}

// settleLocked rewrites the index and deletes blobs nothing references.
func (t *Tracker) settleLocked() error {
	if err := t.rewrite(); err != nil {
		t.failed = fmt.Errorf("checkpoint store: %w", err)
		return t.failed
	}
	t.gcLocked()
	return nil
}

func (t *Tracker) referencedLocked() map[string]bool {
	ref := map[string]bool{}
	for _, c := range t.cps {
		for _, f := range c.files {
			if f.State == stateFile {
				ref[f.SHA] = true
			}
		}
	}
	return ref
}

func (t *Tracker) gcLocked() {
	ref := t.referencedLocked()
	for sha := range t.blobs {
		if !ref[sha] {
			_ = os.Remove(filepath.Join(t.blobDir(), sha))
			delete(t.blobs, sha)
		}
	}
}

func (t *Tracker) usedLocked() int64 {
	var n int64
	for sha := range t.referencedLocked() {
		n += t.blobs[sha]
	}
	return n
}

// ---- saving -----------------------------------------------------------------

// Save records what abs holds now, the first time the open turn touches it. A
// non-nil error means the store could not keep the record; the caller must not
// let the change proceed.
func (t *Tracker) Save(abs string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.saveLocked(KindTurn, abs, nil)
}

// Baseline is the earlier state of a path a command changed, learned after the
// command ran from what the harness recorded before it.
type Baseline struct {
	// Path is absolute.
	Path string
	// Existed is false for a file the command created.
	Existed bool
	// Known is true when Body is the whole earlier content.
	Known bool
	Body  []byte
	// Mode is the earlier permission bits, 0 when unknown.
	Mode fs.FileMode
	// Why says why Body is unknown.
	Why string
}

// SaveBaselines records the earlier state of paths a command (Bash) changed.
// Unlike Save it runs after the command, from bytes held in memory. A path
// whose earlier content is unknown is recorded by name and not restorable.
// gaps are harness sentences for effects the command may have had that were not
// seen. The first record for a path in the open turn wins.
func (t *Tracker) SaveBaselines(bs []Baseline, gaps ...string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	odd := 0
	for i := range bs {
		if !validPath(filepath.Clean(bs[i].Path)) {
			odd++ // a name the tools themselves refuse; say so rather than fail
			continue
		}
		if err := t.saveLocked(KindTurn, bs[i].Path, &bs[i]); err != nil {
			return err
		}
	}
	if odd > 0 {
		gaps = append(gaps, fmt.Sprintf("%d paths with unusual characters are not tracked", odd))
	}
	if len(gaps) == 0 {
		return nil
	}
	if t.cur == nil || t.cur.kind != KindTurn {
		for _, g := range gaps {
			if g = sanitize.Line(g, 120); g != "" && !containsStr(t.pending, g) {
				t.pending = append(t.pending, g)
			}
		}
		return nil
	}
	return t.noteGapsLocked(t.cur, gaps)
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// noteGapsLocked records sentences about effects a checkpoint could not see.
func (t *Tracker) noteGapsLocked(c *cp, gaps []string) error {
	for _, g := range gaps {
		g = sanitize.Line(g, 120)
		if g == "" || containsStr(c.gaps, g) {
			continue
		}
		c.gaps = append(c.gaps, g)
		if err := t.appendRecs(rec{T: "gap", CP: c.id, Why: g}); err != nil {
			t.failed = fmt.Errorf("checkpoint store: %w", err)
			return t.failed
		}
	}
	return nil
}

func (t *Tracker) saveLocked(kind, abs string, b *Baseline) error {
	if t.failed != nil {
		return t.failed
	}
	abs = filepath.Clean(abs)
	if !validPath(abs) {
		return fmt.Errorf("checkpoint: %q is not an absolute path", sanitize.Line(abs, 80))
	}
	if config.UnderCheckpoints(abs) {
		return errors.New("checkpoint: the store is not a file to keep")
	}
	if kind == KindTurn && t.touched[abs] {
		return nil
	}
	c, err := t.openLocked(kind, "", "")
	if err != nil {
		return err
	}
	var fr fileRec
	if b == nil {
		fr, err = t.captureLocked(c, abs)
	} else {
		fr, err = t.fromBaselineLocked(c, abs, b)
	}
	if err != nil {
		return err
	}
	if err := t.appendRecs(rec{T: "f", CP: c.id, Path: fr.Path, State: fr.State, SHA: fr.SHA, Mode: fr.Mode, Size: fr.Size, Why: fr.Why}); err != nil {
		t.failed = fmt.Errorf("checkpoint store: %w", err)
		return t.failed
	}
	c.files = append(c.files, fr)
	if kind == KindTurn {
		t.touched[abs] = true
	}
	return nil
}

// copyDeniedLocked is the by-name policy shared by both capture paths.
func (t *Tracker) copyDeniedLocked(abs string) string {
	if why, ok := locate.EligibleFile(filepath.Base(abs), 0); !ok && (why == locate.SkipSensitive || why == locate.SkipBinary) {
		if why == locate.SkipSensitive {
			return WhySensitive
		}
		return WhyBinary
	}
	if t.pol.CopyDenied != nil {
		if why := t.pol.CopyDenied(abs); why != "" {
			return why
		}
	}
	return ""
}

func (t *Tracker) captureLocked(c *cp, abs string) (fileRec, error) {
	fr := fileRec{Path: abs}
	fi, err := os.Lstat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		fr.State = stateAbsent
		return fr, nil
	case err != nil:
		return fr, fmt.Errorf("checkpoint: %w", err)
	}
	fr.Mode = uint32(fi.Mode().Perm())
	fr.Size = fi.Size()
	skip := func(why string) (fileRec, error) {
		fr.State, fr.Why = stateSkipped, why
		return fr, nil
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return skip(WhySymlink)
	case !fi.Mode().IsRegular():
		return skip(WhySpecial)
	case fi.Size() > MaxFileBytes:
		return skip(WhyLarge)
	}
	if why := t.copyDeniedLocked(abs); why != "" {
		return skip(why)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return fr, fmt.Errorf("checkpoint: %w", err)
	}
	return t.keepBodyLocked(c, fr, body)
}

func (t *Tracker) fromBaselineLocked(c *cp, abs string, b *Baseline) (fileRec, error) {
	fr := fileRec{Path: abs, Mode: uint32(b.Mode.Perm())}
	if !b.Existed {
		fr.State = stateAbsent
		fr.Mode = 0
		return fr, nil
	}
	skip := func(why string) (fileRec, error) {
		fr.State, fr.Why = stateSkipped, why
		return fr, nil
	}
	if !b.Known {
		why := b.Why
		if why == "" {
			why = WhyUnknown
		}
		return skip(why)
	}
	if why := t.copyDeniedLocked(abs); why != "" {
		return skip(why)
	}
	if int64(len(b.Body)) > MaxFileBytes {
		return skip(WhyLarge)
	}
	fr.Size = int64(len(b.Body))
	return t.keepBodyLocked(c, fr, b.Body)
}

// keepBodyLocked stores body as a blob, making room by dropping the oldest
// checkpoints, and returns the record.
func (t *Tracker) keepBodyLocked(c *cp, fr fileRec, body []byte) (fileRec, error) {
	head := body
	if len(head) > 8000 {
		head = head[:8000]
	}
	if !locate.IsText(head) {
		fr.State, fr.Why = stateSkipped, WhyBinary
		return fr, nil
	}
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])
	fr.State, fr.SHA, fr.Size = stateFile, sha, int64(len(body))
	if _, have := t.blobs[sha]; have {
		return fr, nil
	}
	for t.usedLocked()+int64(len(body)) > t.lim.MaxBytes {
		if len(t.cps) == 0 || t.cps[0] == c {
			fr.State, fr.SHA, fr.Why = stateSkipped, "", WhyStoreFull
			return fr, nil
		}
		t.cps = t.cps[1:]
		if err := t.settleLocked(); err != nil {
			return fr, err
		}
	}
	if err := atomicWrite(filepath.Join(t.blobDir(), sha), body, 0o600); err != nil {
		t.failed = fmt.Errorf("checkpoint store: %w", err)
		return fr, t.failed
	}
	t.blobs[sha] = int64(len(body))
	return fr, nil
}

// ---- listing ----------------------------------------------------------------

// List returns the checkpoints, oldest first.
func (t *Tracker) List() []Info {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Info, 0, len(t.cps))
	for _, c := range t.cps {
		in := Info{ID: c.id, Seq: c.seq, Kind: c.kind, Entry: c.entry, Label: c.label, At: time.UnixMilli(c.at), Files: len(c.files), Gaps: len(c.gaps), Undone: c.undone}
		for _, f := range c.files {
			if f.State != stateSkipped {
				in.Restorable++
			}
		}
		out = append(out, in)
	}
	return out
}

// Latest returns the newest turn checkpoint that has not been undone.
func (t *Tracker) Latest() (Info, bool) {
	list := t.List()
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Kind == KindTurn && !list[i].Undone {
			return list[i], true
		}
	}
	return Info{}, false
}

// ---- restoring --------------------------------------------------------------

// Action is what restoring does to one path.
type Action string

// Actions a Plan lists.
const (
	ActRestore Action = "restore" // write the earlier bytes
	ActDelete  Action = "delete"  // remove a file the turn created
	ActSkip    Action = "skip"    // leave it, and say why
)

// Change is one path in a Plan.
type Change struct {
	Path   string
	Action Action
	Why    string // for a skip, or a note
	Size   int64
	mode   uint32
	sha    string
}

// Plan is what restoring to a checkpoint would do. It changes nothing until
// [Tracker.Apply] is called with it.
type Plan struct {
	Target  Info
	Changes []Change
	// Gaps is how many command effects the covered checkpoints could not see.
	Gaps int
	ids  []string
}

// Count returns the paths a Plan would write or remove, and the paths it would
// leave.
func (p Plan) Count() (act, skip int) {
	for _, c := range p.Changes {
		if c.Action == ActSkip {
			skip++
		} else {
			act++
		}
	}
	return
}

// Plan computes the restore of every path touched since checkpoint id (that
// checkpoint included) to the state it had before. A path already in that state
// is left out. The disk is only read.
func (t *Tracker) Plan(id string) (Plan, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var target *cp
	for _, c := range t.cps {
		if c.id == id {
			target = c
		}
	}
	if target == nil {
		return Plan{}, fmt.Errorf("no checkpoint %q", sanitize.Line(id, 20))
	}
	p := Plan{Target: infoOf(target)}
	seen := map[string]bool{}
	for _, c := range t.cps {
		if c.seq < target.seq {
			continue
		}
		p.ids = append(p.ids, c.id)
		p.Gaps += len(c.gaps)
		for _, f := range c.files {
			if seen[f.Path] {
				continue
			}
			seen[f.Path] = true
			if ch, ok := t.planOneLocked(f); ok {
				p.Changes = append(p.Changes, ch)
			}
		}
	}
	sort.SliceStable(p.Changes, func(i, j int) bool { return p.Changes[i].Path < p.Changes[j].Path })
	return p, nil
}

func infoOf(c *cp) Info {
	in := Info{ID: c.id, Seq: c.seq, Kind: c.kind, Entry: c.entry, Label: c.label, At: time.UnixMilli(c.at), Files: len(c.files), Gaps: len(c.gaps), Undone: c.undone}
	return in
}

// planOneLocked decides one path; ok is false when nothing needs doing.
func (t *Tracker) planOneLocked(f fileRec) (Change, bool) {
	ch := Change{Path: f.Path, Size: f.Size, mode: f.Mode, sha: f.SHA}
	if f.State == stateSkipped {
		ch.Action, ch.Why = ActSkip, "not restorable: "+f.Why
		return ch, true
	}
	if t.pol.Within == nil {
		ch.Action, ch.Why = ActSkip, WhyOutside
		return ch, true
	}
	full, err := resolveTarget(f.Path)
	if err != nil || !t.pol.Within(full) {
		ch.Action, ch.Why = ActSkip, WhyOutside
		return ch, true
	}
	if t.pol.RestoreDenied != nil && t.pol.RestoreDenied(f.Path) {
		ch.Action, ch.Why = ActSkip, WhyRestoreDny
		return ch, true
	}
	cur, lerr := os.Lstat(f.Path)
	switch f.State {
	case stateAbsent:
		if errors.Is(lerr, fs.ErrNotExist) {
			return ch, false
		}
		if lerr != nil {
			ch.Action, ch.Why = ActSkip, "cannot be examined"
			return ch, true
		}
		if cur.IsDir() {
			ch.Action, ch.Why = ActSkip, "now a directory"
			return ch, true
		}
		ch.Action = ActDelete
		return ch, true
	default:
		if lerr == nil {
			if cur.Mode()&fs.ModeSymlink != 0 || cur.IsDir() || !cur.Mode().IsRegular() {
				ch.Action, ch.Why = ActSkip, "now "+kindWord(cur.Mode())
				return ch, true
			}
			if cur.Size() == f.Size {
				if body, err := os.ReadFile(f.Path); err == nil {
					sum := sha256.Sum256(body)
					if hex.EncodeToString(sum[:]) == f.SHA && uint32(cur.Mode().Perm()) == f.Mode {
						return ch, false
					}
				}
			}
		}
		ch.Action = ActRestore
		return ch, true
	}
}

func kindWord(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return "a symbolic link"
	case m.IsDir():
		return "a directory"
	}
	return "not a regular file"
}

// Result is what Apply did.
type Result struct {
	Restored, Deleted, Skipped int
	// Safety is the id of the checkpoint that holds what the restore overwrote.
	Safety string
}

// Apply carries out a Plan. It first takes a safety checkpoint of every path it
// will write or remove, and stops before touching anything if that fails. Each
// write goes through a temporary file and a rename; a path that no longer
// passes the plan's checks is skipped, never forced.
func (t *Tracker) Apply(p Plan) (Result, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var res Result
	if t.failed != nil {
		return res, t.failed
	}
	t.cur = nil
	saved := false
	for _, ch := range p.Changes {
		if ch.Action == ActSkip {
			continue
		}
		if !saved {
			if _, err := t.openLocked(KindRestore, "", "before restore to "+p.Target.ID); err != nil {
				return res, err
			}
			saved = true
		}
		if err := t.saveLocked(KindRestore, ch.Path, nil); err != nil {
			t.cur = nil
			return res, fmt.Errorf("the safety copy failed, so nothing was changed: %w", err)
		}
	}
	if saved {
		res.Safety = t.cur.id
	}
	t.cur = nil
	for _, ch := range p.Changes {
		switch ch.Action {
		case ActSkip:
			res.Skipped++
		case ActDelete:
			if t.deleteLocked(ch) {
				res.Deleted++
			} else {
				res.Skipped++
			}
		case ActRestore:
			if t.writeLocked(ch) {
				res.Restored++
			} else {
				res.Skipped++
			}
		}
	}
	// Every turn checkpoint covered is now undone, so a second /undo goes one
	// turn further back instead of repeating this one.
	for _, c := range t.cps {
		if c.kind != KindTurn || c.undone {
			continue
		}
		for _, id := range p.ids {
			if id == c.id {
				c.undone = true
				if err := t.appendRecs(rec{T: "undone", CP: c.id}); err != nil {
					t.failed = fmt.Errorf("checkpoint store: %w", err)
					return res, t.failed
				}
			}
		}
	}
	return res, nil
}

// recheckLocked re-applies the plan's checks at the moment of writing.
func (t *Tracker) recheckLocked(ch Change) bool {
	if t.pol.Within == nil {
		return false
	}
	full, err := resolveTarget(ch.Path)
	if err != nil || !t.pol.Within(full) {
		return false
	}
	return t.pol.RestoreDenied == nil || !t.pol.RestoreDenied(ch.Path)
}

func (t *Tracker) deleteLocked(ch Change) bool {
	if !t.recheckLocked(ch) {
		return false
	}
	fi, err := os.Lstat(ch.Path)
	if err != nil || fi.IsDir() {
		return false
	}
	return os.Remove(ch.Path) == nil
}

func (t *Tracker) writeLocked(ch Change) bool {
	if !t.recheckLocked(ch) || !validSHA(ch.sha) {
		return false
	}
	body, err := os.ReadFile(filepath.Join(t.blobDir(), ch.sha))
	if err != nil {
		return false
	}
	if sum := sha256.Sum256(body); hex.EncodeToString(sum[:]) != ch.sha {
		return false
	}
	if fi, err := os.Lstat(ch.Path); err == nil && (fi.Mode()&fs.ModeSymlink != 0 || !fi.Mode().IsRegular()) {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(ch.Path), 0o755); err != nil {
		return false
	}
	// The directory may have been created through a link since the plan.
	if !t.recheckLocked(ch) {
		return false
	}
	mode := fs.FileMode(ch.mode & 0o777)
	if mode == 0 {
		mode = 0o644
	}
	return atomicWrite(ch.Path, body, mode) == nil
}

// resolveTarget resolves the directory part of abs through symlinks and
// re-appends the final name, so a path is judged by where it really lands.
func resolveTarget(abs string) (string, error) {
	dir, base := filepath.Split(abs)
	existing := filepath.Clean(dir)
	var tail []string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", errors.New("no existing ancestor")
		}
		tail = append([]string{filepath.Base(existing)}, tail...)
		existing = parent
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	parts := append([]string{real}, tail...)
	parts = append(parts, base)
	return filepath.Join(parts...), nil
}

// atomicWrite writes data to path through a temporary file in the same
// directory and a rename.
func atomicWrite(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".belai-ckpt-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// ---- retention --------------------------------------------------------------

// Sweep deletes the session stores under root (config.CheckpointsRoot) whose
// index has not changed for retainDays, and reports how many it removed. The
// session named by keep is never removed. It follows no link: an entry that is
// not a plain directory is left alone.
func Sweep(root string, retainDays int, now time.Time, keep string) int {
	if retainDays < 1 {
		return 0
	}
	cutoff := now.Add(-time.Duration(retainDays) * 24 * time.Hour)
	removed := 0
	projects, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	for _, p := range projects {
		pdir := filepath.Join(root, p.Name())
		if fi, err := os.Lstat(pdir); err != nil || !fi.IsDir() {
			continue
		}
		sessions, err := os.ReadDir(pdir)
		if err != nil {
			continue
		}
		for _, s := range sessions {
			if s.Name() == keep {
				continue
			}
			sdir := filepath.Join(pdir, s.Name())
			fi, err := os.Lstat(sdir)
			if err != nil || !fi.IsDir() {
				continue
			}
			last := fi.ModTime()
			if ix, err := os.Lstat(filepath.Join(sdir, "index.jsonl")); err == nil && ix.ModTime().After(last) {
				last = ix.ModTime()
			}
			if last.Before(cutoff) && os.RemoveAll(sdir) == nil {
				removed++
			}
		}
		if rest, err := os.ReadDir(pdir); err == nil && len(rest) == 0 {
			_ = os.Remove(pdir)
		}
	}
	return removed
}

// Unavailable returns a Tracker that refuses every save with err. The session
// uses it when the store cannot be opened, so file checkpoints stay fail closed:
// a mutating call is withheld, not run with nothing to restore it from.
func Unavailable(err error) *Tracker {
	if err == nil {
		err = errors.New("the checkpoint store is not available")
	}
	return &Tracker{
		failed:  fmt.Errorf("checkpoint store: %w", err),
		touched: map[string]bool{},
		blobs:   map[string]int64{},
		now:     time.Now,
	}
}
