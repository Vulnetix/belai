package budget

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/signet/internal/config"
)

// LedgerFile is the usage ledger's name under the global state directory.
const LedgerFile = "usage.json"

// Retention of ledger entries: day totals older than dayRetention are pruned
// on every write, so a month budget always has its whole month and a year of
// history stays inspectable; a session total is pruned once the session has
// not been touched for the session retention.
const dayRetention = 13 * 31 * 24 * time.Hour

const dayLayout = "2006-01-02"

// ledgerData is usage.json. Day totals are keyed by provider/model then local
// date; session totals by session id then provider/model.
type ledgerData struct {
	Version  int                         `json:"version"`
	Days     map[string]map[string]int64 `json:"days,omitempty"`
	Sessions map[string]*sessionEntry    `json:"sessions,omitempty"`
	// Imported names the sessions whose transcript usage has been folded into
	// Days (value: the local date of the import), so a session is imported at
	// most once. A live session pruned from Sessions moves here too, so its
	// transcript is never imported on top of the usage it recorded live.
	Imported map[string]string `json:"imported,omitempty"`
}

type sessionEntry struct {
	Updated time.Time        `json:"updated"`
	Models  map[string]int64 `json:"models,omitempty"`
}

func newLedgerData() ledgerData {
	return ledgerData{Version: 1, Days: map[string]map[string]int64{}, Sessions: map[string]*sessionEntry{}, Imported: map[string]string{}}
}

// Recorder accumulates token usage and measures budgets against it. Add is
// cheap and never blocks on disk: totals update in memory at once and a
// background goroutine folds them into usage.json under the advisory lock,
// coalescing bursts. Reads see this process's usage immediately and other
// processes' usage as of the last flush or Refresh. Safe for concurrent use.
type Recorder struct {
	mu        sync.Mutex
	path      string
	session   string
	retention time.Duration
	now       func() time.Time

	disk    ledgerData                  // last state read from or written to disk
	pending map[string]map[string]int64 // unflushed day deltas: key → date → tokens
	pendSes map[string]map[string]int64 // unflushed session deltas: session → key → tokens
	// inflight holds the deltas a Flush is writing, so Used keeps counting
	// them until the write lands in disk.
	inflight    map[string]map[string]int64
	inflightSes map[string]map[string]int64
	flushMu     sync.Mutex
	notice      string
	loaded      time.Time

	kick chan struct{}
	done chan struct{}
	wg   sync.WaitGroup
	once sync.Once
}

// DefaultPath returns usage.json under the global state directory.
func DefaultPath() (string, error) {
	dir, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, LedgerFile), nil
}

// Open loads the ledger at path for the given session and starts the flusher.
// A corrupt ledger is moved aside and a fresh one started; Notice then says so.
// retentionDays bounds how long an idle session's total is kept (<= 0 means 28).
func Open(path, session string, retentionDays int) (*Recorder, error) {
	if retentionDays <= 0 {
		retentionDays = 28
	}
	r := &Recorder{
		path:      path,
		session:   session,
		retention: time.Duration(retentionDays) * 24 * time.Hour,
		now:       time.Now,
		disk:      newLedgerData(),
		pending:   map[string]map[string]int64{},
		pendSes:   map[string]map[string]int64{},
		kick:      make(chan struct{}, 1),
		done:      make(chan struct{}),
	}
	if err := r.reload(); err != nil {
		return nil, err
	}
	r.wg.Add(1)
	go r.flusher()
	return r, nil
}

// Notice returns a one-line explanation of a recovery Open performed (a
// corrupt ledger moved aside), or "".
func (r *Recorder) Notice() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.notice
}

// SetSession switches the session whose total the session scope reads. A
// resumed session picks its stored total back up.
func (r *Recorder) SetSession(id string) {
	r.mu.Lock()
	r.session = id
	r.mu.Unlock()
}

// Add records tokens spent by one model call to provider/model.
func (r *Recorder) Add(provider, model string, tokens int64) {
	if tokens <= 0 {
		return
	}
	key := config.ModelKey(provider, model)
	r.mu.Lock()
	day := r.now().Format(dayLayout)
	if r.pending[key] == nil {
		r.pending[key] = map[string]int64{}
	}
	r.pending[key][day] += tokens
	if r.session != "" {
		if r.pendSes[r.session] == nil {
			r.pendSes[r.session] = map[string]int64{}
		}
		r.pendSes[r.session][key] += tokens
	}
	r.mu.Unlock()
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Used returns the tokens spent against budget b's scope at the current time:
// this session's total for a session budget, the local day's for a day
// budget, the local month's for a month budget.
func (r *Recorder) Used(b config.TokenBudget) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := b.Key()
	switch b.Scope {
	case config.BudgetScopeSession:
		var n int64
		if e := r.disk.Sessions[r.session]; e != nil {
			n += e.Models[key]
		}
		return n + r.inflightSes[r.session][key] + r.pendSes[r.session][key]
	case config.BudgetScopeDay:
		return r.sumDays(key, r.now().Format(dayLayout))
	case config.BudgetScopeMonth:
		return r.sumDays(key, r.now().Format("2006-01-"))
	}
	return 0
}

// sumDays totals key's day entries whose date starts with prefix across the
// disk snapshot, the in-flight write and pending usage. A full date is its own
// prefix, so it serves the day scope as well as the month.
func (r *Recorder) sumDays(key, prefix string) int64 {
	var n int64
	for _, m := range []map[string]int64{r.disk.Days[key], r.inflight[key], r.pending[key]} {
		for d, v := range m {
			if strings.HasPrefix(d, prefix) {
				n += v
			}
		}
	}
	return n
}

// Gauges measures every budget in bs against this recorder now.
func (r *Recorder) Gauges(bs []config.TokenBudget) []Gauge {
	now := r.now()
	out := make([]Gauge, 0, len(bs))
	for _, b := range bs {
		out = append(out, Status(b, r.Used(b), now))
	}
	return out
}

// Refresh re-reads the ledger when the last read is older than maxAge (or
// maxAge <= 0), so
// usage recorded by another signet process shows up. Pending local usage is
// kept.
func (r *Recorder) Refresh(maxAge time.Duration) {
	r.mu.Lock()
	age := r.now().Sub(r.loaded)
	r.mu.Unlock()
	// A negative age means the clock moved backwards; re-read rather than
	// trust a snapshot from the future.
	stale := maxAge <= 0 || age < 0 || age >= maxAge
	if stale {
		r.flushMu.Lock()
		_ = r.reload()
		r.flushMu.Unlock()
	}
}

// Flush folds pending usage into usage.json now.
func (r *Recorder) Flush() error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	r.mu.Lock()
	pending, pendSes := r.pending, r.pendSes
	if len(pending) == 0 && len(pendSes) == 0 {
		r.mu.Unlock()
		return nil
	}
	r.pending, r.pendSes = map[string]map[string]int64{}, map[string]map[string]int64{}
	r.inflight, r.inflightSes = pending, pendSes
	r.mu.Unlock()
	if err := r.write(pending, pendSes); err != nil {
		// Keep the usage: put the deltas back for the next attempt.
		r.mu.Lock()
		mergeDeltas(r.pending, pending)
		mergeDeltas(r.pendSes, pendSes)
		r.inflight, r.inflightSes = nil, nil
		r.mu.Unlock()
		return err
	}
	return nil
}

// Close flushes and stops the flusher. It is idempotent.
func (r *Recorder) Close() error {
	var err error
	r.once.Do(func() {
		close(r.done)
		r.wg.Wait()
		err = r.Flush()
	})
	return err
}

func (r *Recorder) flusher() {
	defer r.wg.Done()
	for {
		select {
		case <-r.done:
			return
		case <-r.kick:
			_ = r.Flush()
		}
	}
}

// write applies deltas to the on-disk ledger under the advisory lock:
// re-read, add, prune, write atomically.
func (r *Recorder) write(days, sessions map[string]map[string]int64) error {
	// The directory must exist before the lockfile can be created in it.
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	unlock, err := config.AcquireFileLock(r.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	data, notice, err := readLedger(r.path)
	if err != nil {
		return err
	}
	now := r.now()
	for key, byDay := range days {
		if data.Days[key] == nil {
			data.Days[key] = map[string]int64{}
		}
		for d, n := range byDay {
			data.Days[key][d] += n
		}
	}
	for id, byKey := range sessions {
		e := data.Sessions[id]
		if e == nil {
			e = &sessionEntry{Models: map[string]int64{}}
			data.Sessions[id] = e
		}
		if e.Models == nil {
			e.Models = map[string]int64{}
		}
		for key, n := range byKey {
			e.Models[key] += n
		}
		e.Updated = now
	}
	prune(&data, now, r.retention)
	buf, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := config.WriteGlobalFileAtomic(r.path, buf); err != nil {
		return err
	}
	r.mu.Lock()
	r.disk = data
	r.loaded = now
	r.inflight, r.inflightSes = nil, nil
	if notice != "" {
		r.notice = notice
	}
	r.mu.Unlock()
	return nil
}

// reload replaces the in-memory disk snapshot with the file's contents.
func (r *Recorder) reload() error {
	data, notice, err := readLedger(r.path)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.disk = data
	r.loaded = r.now()
	if notice != "" {
		r.notice = notice
	}
	r.mu.Unlock()
	return nil
}

// readLedger reads usage.json. A missing file is an empty ledger. A file that
// does not parse is moved aside to usage.json.corrupt-<unix> and an empty
// ledger returned with a notice naming where it went: losing a usage history
// must never stop the harness, and the file is kept for inspection.
func readLedger(path string) (ledgerData, string, error) {
	buf, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return newLedgerData(), "", nil
	}
	if err != nil {
		return ledgerData{}, "", err
	}
	data := newLedgerData()
	if err := json.Unmarshal(buf, &data); err != nil {
		aside := fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
		_ = os.Rename(path, aside)
		return newLedgerData(), fmt.Sprintf("token usage ledger was unreadable and was moved to %s; usage counts restart from zero", aside), nil
	}
	if data.Days == nil {
		data.Days = map[string]map[string]int64{}
	}
	if data.Sessions == nil {
		data.Sessions = map[string]*sessionEntry{}
	}
	if data.Imported == nil {
		data.Imported = map[string]string{}
	}
	return data, "", nil
}

// prune drops day totals older than dayRetention and sessions idle past the
// session retention.
func prune(d *ledgerData, now time.Time, sessionRetention time.Duration) {
	if d.Imported == nil {
		d.Imported = map[string]string{}
	}
	cutoff := now.Add(-dayRetention).Format(dayLayout)
	for key, byDay := range d.Days {
		for day := range byDay {
			if day < cutoff {
				delete(byDay, day)
			}
		}
		if len(byDay) == 0 {
			delete(d.Days, key)
		}
	}
	for id, e := range d.Sessions {
		if e == nil || now.Sub(e.Updated) > sessionRetention {
			delete(d.Sessions, id)
			// Its usage is already in Days: remember it so the history import
			// never counts its transcript again.
			day := now.Format(dayLayout)
			if e != nil {
				day = e.Updated.Format(dayLayout)
			}
			d.Imported[id] = day
		}
	}
	// An import marker outlives its usage by the same 13 months: once the days
	// it fed are pruned, re-importing could only add days that are pruned too.
	for id, day := range d.Imported {
		if day < cutoff {
			delete(d.Imported, id)
		}
	}
}

func mergeDeltas(dst, src map[string]map[string]int64) {
	for k, inner := range src {
		if dst[k] == nil {
			dst[k] = map[string]int64{}
		}
		for k2, n := range inner {
			dst[k][k2] += n
		}
	}
}
