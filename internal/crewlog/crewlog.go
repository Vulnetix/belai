// Package crewlog is the durable local record of a crew run: two append-only
// JSONL files under <global>/crews/<run>/, one of worker faults and one a
// crew-scoped mirror of the audit facts. The audit stream is deleted once it
// has uploaded, so this is what `belai crew log` reads after the fact.
//
// It records the same kind of thing the audit does. An Entry is an audit.Kind
// with identifiers reduced by audit.Clean and Data keys from audit.DataKeys(),
// chained by SHA-256 like an audit stream. A Fault adds one human-readable
// field, Detail, which Append passes through sanitize.Line and caps at 240
// bytes; it stays on this machine and never reaches the audit, telemetry or a
// notification.
//
// Several processes (the crew's workers and the harness) append to one run.
// Each append takes an in-process mutex and then the run directory's advisory
// lockfile (config.AcquireFileLock), reads the chain tail, rotates a file past
// 4 MiB to .1 (keeping three generations) and writes the line with one
// O_APPEND write.
package crewlog

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sanitize"
)

// File names inside a run directory.
const (
	FaultsFile = "faults.jsonl"
	AuditFile  = "audit.jsonl"
	lockFile   = ".lock"
)

// Caps and rotation.
const (
	// MaxDetail bounds a Fault's Detail, in bytes.
	MaxDetail = 240
	// Generations is how many rotated files (.1 … .3) are kept.
	Generations = 3
	maxRunID    = 64
	maxData     = 16
	// maxLine bounds one line; a longer one is refused, and the tail read
	// that finds the chain's last entry reads at most twice this.
	maxLine = 16 << 10
)

// rotateAt is the size past which a file rotates; tests lower it.
var rotateAt int64 = 4 << 20

// Fault is one worker or item failure. Class is a fault code (auth,
// rate_limited, …); Detail is the only free text and is cleaned by Append.
type Fault struct {
	At       int64  `json:"at,omitempty"`
	Run      string `json:"run,omitempty"`
	Crew     string `json:"crew,omitempty"`
	Worker   string `json:"worker,omitempty"`
	Profile  string `json:"profile,omitempty"`
	Item     string `json:"item,omitempty"`
	Session  string `json:"session,omitempty"`
	Repo     string `json:"repo,omitempty"`
	Class    string `json:"class,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Status   int    `json:"status,omitempty"`
	Retries  int    `json:"retries,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// Entry is one crew-scoped audit fact. Seq, Prev and Hash are the Log's: Hash
// is the SHA-256 of the entry's JSON with Hash empty, and Prev the previous
// line's Hash, so an edited, dropped or reordered line breaks the chain.
type Entry struct {
	At        int64             `json:"at,omitempty"`
	Kind      string            `json:"kind,omitempty"`
	Actor     string            `json:"actor,omitempty"`
	ActorKind string            `json:"actorKind,omitempty"`
	Worker    string            `json:"worker,omitempty"`
	Profile   string            `json:"profile,omitempty"`
	Item      string            `json:"item,omitempty"`
	Session   string            `json:"session,omitempty"`
	Repo      string            `json:"repo,omitempty"`
	Outcome   string            `json:"outcome,omitempty"`
	Data      map[string]string `json:"data,omitempty"`
	Seq       int64             `json:"seq"`
	Prev      string            `json:"prev,omitempty"`
	Hash      string            `json:"hash,omitempty"`
}

// Filter narrows a read. Zero fields match everything; Limit keeps the newest
// Limit records.
type Filter struct {
	Since   int64
	Class   string
	Worker  string
	Profile string
	Repo    string
	Kind    string
	Limit   int
}

// Dir is the log directory of one crew run under the global directory. The
// run id is reduced to an identifier, so it cannot name another directory.
func Dir(globalDir, run string) string {
	id := sanitize.Ident(run, maxRunID)
	if id == "" || id == "." || id == ".." {
		id = "_"
	}
	return filepath.Join(globalDir, "crews", id)
}

// Runs lists the run directories under the global directory, oldest first by
// modification time. No crews directory is no runs.
func Runs(globalDir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(globalDir, "crews"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	type run struct {
		name string
		mod  time.Time
	}
	var runs []run
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		runs = append(runs, run{e.Name(), info.ModTime()})
	}
	sort.SliceStable(runs, func(i, j int) bool {
		if !runs[i].mod.Equal(runs[j].mod) {
			return runs[i].mod.Before(runs[j].mod)
		}
		return runs[i].name < runs[j].name
	})
	out := make([]string, len(runs))
	for i, r := range runs {
		out[i] = r.name
	}
	return out, nil
}

// dirLocks serialises appends within this process; the lockfile serialises
// processes.
var (
	dirLocksMu sync.Mutex
	dirLocks   = map[string]*sync.Mutex{}
)

func dirLock(dir string) *sync.Mutex {
	dirLocksMu.Lock()
	defer dirLocksMu.Unlock()
	m := dirLocks[dir]
	if m == nil {
		m = &sync.Mutex{}
		dirLocks[dir] = m
	}
	return m
}

// Log appends to one run directory. It holds no open files between appends,
// so any number of Logs, in any number of processes, may share a directory.
type Log struct {
	dir string
	mu  *sync.Mutex
	now func() time.Time
}

// Open creates the run directory (0700) if needed and returns its Log.
func Open(dir string) (*Log, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("create crew log dir: %w", err)
	}
	return &Log{dir: abs, mu: dirLock(abs), now: time.Now}, nil
}

// Dir is the directory the Log writes.
func (l *Log) Dir() string { return l.dir }

// Close releases the Log. Every append is already on disk.
func (l *Log) Close() error { return nil }

// AppendFault cleans f and appends it to faults.jsonl, stamping At when zero.
func (l *Log) AppendFault(f Fault) error {
	f = cleanFault(f)
	if f.At == 0 {
		f.At = l.now().UnixMilli()
	}
	line, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return l.locked(func() error { return l.write(FaultsFile, line) })
}

// AppendEntry cleans e and appends it to audit.jsonl, stamping At when zero
// and setting Seq, Prev and Hash from the file's last line. An unknown kind is
// refused.
func (l *Log) AppendEntry(e Entry) error {
	e, err := cleanEntry(e)
	if err != nil {
		return err
	}
	if e.At == 0 {
		e.At = l.now().UnixMilli()
	}
	return l.locked(func() error {
		path := filepath.Join(l.dir, AuditFile)
		last, err := lastEntry(path)
		if err != nil {
			return err
		}
		e.Seq, e.Prev = 0, ""
		if last != nil {
			e.Seq, e.Prev = last.Seq+1, last.Hash
		} else if older, _ := lastEntry(generation(path, 1)); older != nil {
			// The file was just rotated: Seq keeps counting, the chain restarts.
			e.Seq = older.Seq + 1
		}
		rotated, err := l.rotate(AuditFile)
		if err != nil {
			return err
		}
		if rotated {
			// A new file starts a new chain; Seq keeps counting.
			e.Prev = ""
		}
		e.Hash = ""
		e.Hash, err = hashOf(e)
		if err != nil {
			return err
		}
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		return l.write(AuditFile, line)
	})
}

func (l *Log) locked(fn func() error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	release, err := config.AcquireFileLock(filepath.Join(l.dir, lockFile))
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// write appends one line with a single O_APPEND write, rotating first when
// the file has grown past rotateAt. The caller holds the lock.
func (l *Log) write(name string, line []byte) error {
	if len(line)+1 > maxLine {
		return fmt.Errorf("crew log line is %d bytes, over the %d byte cap", len(line), maxLine)
	}
	if _, err := l.rotate(name); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(l.dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// rotate moves name to name.1 (and each older generation up one, dropping the
// last) when it has reached rotateAt. It reports whether it rotated.
func (l *Log) rotate(name string) (bool, error) {
	path := filepath.Join(l.dir, name)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Size() < rotateAt {
		return false, nil
	}
	_ = os.Remove(generation(path, Generations))
	for g := Generations - 1; g >= 1; g-- {
		if err := os.Rename(generation(path, g), generation(path, g+1)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	if err := os.Rename(path, generation(path, 1)); err != nil {
		return false, err
	}
	return true, nil
}

func generation(path string, g int) string {
	if g == 0 {
		return path
	}
	return path + "." + strconv.Itoa(g)
}

// cleanFault reduces every identifier with audit.Clean and Detail to one
// sanitized line of at most MaxDetail bytes.
func cleanFault(f Fault) Fault {
	f.Run, f.Crew, f.Worker, f.Profile = audit.Clean(f.Run), audit.Clean(f.Crew), audit.Clean(f.Worker), audit.Clean(f.Profile)
	f.Item, f.Session, f.Repo = audit.Clean(f.Item), audit.Clean(f.Session), audit.Clean(f.Repo)
	f.Class, f.Provider, f.Model, f.Outcome = audit.Clean(f.Class), audit.Clean(f.Provider), audit.Clean(f.Model), audit.Clean(f.Outcome)
	if f.Status < 0 {
		f.Status = 0
	}
	if f.Retries < 0 {
		f.Retries = 0
	}
	f.Detail = capBytes(sanitize.Line(f.Detail, 0), MaxDetail)
	return f
}

// capBytes cuts s to at most max bytes on a rune boundary, ending in an
// ellipsis when it cut.
func capBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const ellipsis = "…"
	cut := max - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}

var actorKinds = map[string]bool{audit.ActorAgent: true, audit.ActorHuman: true, audit.ActorWeb: true, audit.ActorHarness: true}

// cleanEntry refuses an unknown kind, keeps an actor kind only from its enum,
// reduces every identifier with audit.Clean and keeps only allowlisted Data
// keys (at most 16, by key order).
func cleanEntry(e Entry) (Entry, error) {
	if !audit.Kind(e.Kind).Valid() {
		return Entry{}, fmt.Errorf("unknown audit kind %q", audit.Clean(e.Kind))
	}
	if !actorKinds[e.ActorKind] {
		e.ActorKind = ""
	}
	e.Actor, e.Worker, e.Profile = audit.Clean(e.Actor), audit.Clean(e.Worker), audit.Clean(e.Profile)
	e.Item, e.Session, e.Repo, e.Outcome = audit.Clean(e.Item), audit.Clean(e.Session), audit.Clean(e.Repo), audit.Clean(e.Outcome)
	allowed := map[string]bool{}
	for _, k := range audit.DataKeys() {
		allowed[k] = true
	}
	var keys []string
	for k := range e.Data {
		if allowed[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > maxData {
		keys = keys[:maxData]
	}
	var data map[string]string
	if len(keys) > 0 {
		data = make(map[string]string, len(keys))
		for _, k := range keys {
			data[k] = audit.Clean(e.Data[k])
		}
	}
	e.Data = data
	return e, nil
}

// hashOf is the SHA-256 of e's JSON with Hash empty (and so omitted).
// encoding/json writes struct fields in order and map keys sorted, so the
// text is canonical.
func hashOf(e Entry) (string, error) {
	e.Hash = ""
	raw, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// lastEntry reads the last complete line of path; nil for no file or an empty
// one.
func lastEntry(path string) (*Entry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	start := size - 2*maxLine
	if start < 0 {
		start = 0
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	// Drop a trailing newline, then take what follows the one before it.
	end := len(buf)
	for end > 0 && buf[end-1] == '\n' {
		end--
	}
	if end == 0 {
		return nil, nil
	}
	begin := end - 1
	for begin >= 0 && buf[begin] != '\n' {
		begin--
	}
	var e Entry
	if err := json.Unmarshal(buf[begin+1:end], &e); err != nil {
		return nil, fmt.Errorf("crew audit log's last line is not an entry: %w", err)
	}
	return &e, nil
}

// readLines calls fn for each line of every generation of name, oldest
// generation first.
func readLines(dir, name string, fn func([]byte)) error {
	path := filepath.Join(dir, name)
	for g := Generations; g >= 0; g-- {
		f, err := os.Open(generation(path, g))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64<<10), maxLine)
		for sc.Scan() {
			if len(sc.Bytes()) > 0 {
				fn(sc.Bytes())
			}
		}
		err = sc.Err()
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// ReadFaults returns the run's faults matching f, newest last, from every
// generation. A line that does not parse is skipped.
func ReadFaults(dir string, f Filter) ([]Fault, error) {
	var out []Fault
	err := readLines(dir, FaultsFile, func(line []byte) {
		var x Fault
		if json.Unmarshal(line, &x) != nil {
			return
		}
		if x.At < f.Since || !match(f.Class, x.Class) || !match(f.Worker, x.Worker) ||
			!match(f.Profile, x.Profile) || !match(f.Repo, x.Repo) {
			return
		}
		out = append(out, x)
	})
	return limit(out, f.Limit), err
}

// ReadEntries returns the run's audit entries matching f, newest last, from
// every generation. A line that does not parse is skipped.
func ReadEntries(dir string, f Filter) ([]Entry, error) {
	var out []Entry
	err := readLines(dir, AuditFile, func(line []byte) {
		var x Entry
		if json.Unmarshal(line, &x) != nil {
			return
		}
		if x.At < f.Since || !match(f.Kind, x.Kind) || !match(f.Worker, x.Worker) ||
			!match(f.Profile, x.Profile) || !match(f.Repo, x.Repo) {
			return
		}
		out = append(out, x)
	})
	return limit(out, f.Limit), err
}

func match(want, got string) bool { return want == "" || want == got }

func limit[T any](xs []T, n int) []T {
	if n > 0 && len(xs) > n {
		return xs[len(xs)-n:]
	}
	return xs
}

// Verify walks the current audit.jsonl and returns the Seq of the first line
// that does not continue the chain (a line that does not parse, a Seq that is
// not one more than the last, a Prev that is not the last Hash, or a Hash that
// does not recompute), or -1 when it is intact. The first line of the file
// starts the chain: its Prev is empty, since rotation starts a new chain.
// Rotated generations are not walked.
func Verify(dir string) (int64, error) {
	f, err := os.Open(filepath.Join(dir, AuditFile))
	if errors.Is(err, os.ErrNotExist) {
		return -1, nil
	}
	if err != nil {
		return -1, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	var prev *Entry
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		want := int64(0)
		if prev != nil {
			want = prev.Seq + 1
		}
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			return want, nil
		}
		h, err := hashOf(e)
		if err != nil {
			return -1, err
		}
		switch {
		case prev == nil && e.Prev != "",
			prev != nil && (e.Seq != want || e.Prev != prev.Hash),
			h != e.Hash:
			return e.Seq, nil
		}
		prev = &e
	}
	return -1, sc.Err()
}
