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

	"github.com/vulnetix/belai/internal/config"
)

// LedgerFile is the usage ledger's name under the global state directory.
const LedgerFile = "usage.json"

// ledgerVersion is the format written today. Version 1 files (days, sessions
// and imported only) load unchanged; hours and limits start empty.
const ledgerVersion = 2

// Retention of ledger entries: day totals older than dayRetention are pruned
// on every write, so a month budget always has its whole month and a year of
// history stays inspectable; a session total is pruned once the session has
// not been touched for the session retention; hour buckets and plan-limit
// readings are kept for hourRetention, enough for pace, the 24-hour sparkline
// and the seven-day heatmap.
const (
	dayRetention  = 13 * 31 * 24 * time.Hour
	hourRetention = 8 * 24 * time.Hour
)

const (
	dayLayout  = "2006-01-02"
	hourLayout = "2006-01-02T15"
)

// hourBucket is one model's usage in one local hour: tokens and completed
// calls.
type hourBucket struct {
	T int64 `json:"t"`
	C int   `json:"c"`
}

// ledgerData is usage.json. Day totals are keyed by provider/model then local
// date; hour buckets by provider/model then local hour; session totals by
// session id then provider/model.
type ledgerData struct {
	Version  int                         `json:"version"`
	Days     map[string]map[string]int64 `json:"days,omitempty"`
	Sessions map[string]*sessionEntry    `json:"sessions,omitempty"`
	// Hours is what pace, the sparkline, the heatmap and call counts read.
	// Imported transcripts know only days, so they never appear here.
	Hours map[string]map[string]hourBucket `json:"hours,omitempty"`
	// Limits holds the latest plan-limit reading per provider and window.
	Limits map[string]PlanLimit `json:"limits,omitempty"`
	// Imported names the sessions whose transcript usage has been folded into
	// Days (value: the local date the session was last active), so a session is
	// imported at most once. A live session pruned from Sessions moves here
	// too, so its transcript is never imported on top of the usage it recorded
	// live.
	Imported map[string]string `json:"imported,omitempty"`
}

type sessionEntry struct {
	Updated time.Time        `json:"updated"`
	Models  map[string]int64 `json:"models,omitempty"`
}

func newLedgerData() ledgerData {
	return ledgerData{
		Version:  ledgerVersion,
		Days:     map[string]map[string]int64{},
		Sessions: map[string]*sessionEntry{},
		Hours:    map[string]map[string]hourBucket{},
		Limits:   map[string]PlanLimit{},
		Imported: map[string]string{},
	}
}

// delta is one flush's worth of unwritten usage.
type delta struct {
	days   map[string]map[string]int64
	ses    map[string]map[string]int64
	hours  map[string]map[string]hourBucket
	limits map[string]PlanLimit
}

func newDelta() delta {
	return delta{
		days:   map[string]map[string]int64{},
		ses:    map[string]map[string]int64{},
		hours:  map[string]map[string]hourBucket{},
		limits: map[string]PlanLimit{},
	}
}

func (d delta) empty() bool {
	return len(d.days) == 0 && len(d.ses) == 0 && len(d.hours) == 0 && len(d.limits) == 0
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

	disk ledgerData // last state read from or written to disk
	// pending is unflushed usage; inflight is what a Flush is writing, so reads
	// keep counting it until the write lands in disk.
	pending  delta
	inflight delta
	// roles tallies this process's tokens by usage role. It is memory only: the
	// ledger keeps no role, so the roles view speaks for this session.
	roles map[string]int64

	flushMu sync.Mutex
	notice  string
	loaded  time.Time

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
		pending:   newDelta(),
		roles:     map[string]int64{},
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
	r.AddCall(provider, model, "", tokens)
}

// AddCall records one completed model call: its tokens against provider/model
// in the day, hour and session totals, and against role in this process's role
// tally. An empty role is not tallied.
func (r *Recorder) AddCall(provider, model, role string, tokens int64) {
	if tokens <= 0 {
		return
	}
	key := config.ModelKey(provider, model)
	r.mu.Lock()
	now := r.now()
	day, hour := now.Format(dayLayout), now.Format(hourLayout)
	if r.pending.days[key] == nil {
		r.pending.days[key] = map[string]int64{}
	}
	r.pending.days[key][day] += tokens
	if r.pending.hours[key] == nil {
		r.pending.hours[key] = map[string]hourBucket{}
	}
	hb := r.pending.hours[key][hour]
	hb.T += tokens
	hb.C++
	r.pending.hours[key][hour] = hb
	if r.session != "" {
		if r.pending.ses[r.session] == nil {
			r.pending.ses[r.session] = map[string]int64{}
		}
		r.pending.ses[r.session][key] += tokens
	}
	if role != "" {
		r.roles[role] += tokens
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
	return r.usedLocked(b)
}

// usedLocked is Used for a caller that already holds r.mu.
func (r *Recorder) usedLocked(b config.TokenBudget) int64 {
	key := b.Key()
	switch b.Scope {
	case config.BudgetScopeSession:
		var n int64
		if e := r.disk.Sessions[r.session]; e != nil {
			n += e.Models[key]
		}
		return n + r.inflight.ses[r.session][key] + r.pending.ses[r.session][key]
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
	for _, m := range []map[string]int64{r.disk.Days[key], r.inflight.days[key], r.pending.days[key]} {
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
// usage recorded by another belai process shows up. Pending local usage is
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
	pending := r.pending
	if pending.empty() {
		r.mu.Unlock()
		return nil
	}
	r.pending = newDelta()
	r.inflight = pending
	r.mu.Unlock()
	if err := r.write(pending); err != nil {
		// Keep the usage: put the deltas back for the next attempt.
		r.mu.Lock()
		mergeDelta(&r.pending, pending)
		r.inflight = delta{}
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
func (r *Recorder) write(d delta) error {
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
	for key, byDay := range d.days {
		if data.Days[key] == nil {
			data.Days[key] = map[string]int64{}
		}
		for day, n := range byDay {
			data.Days[key][day] += n
		}
	}
	for key, byHour := range d.hours {
		if data.Hours[key] == nil {
			data.Hours[key] = map[string]hourBucket{}
		}
		for hour, b := range byHour {
			cur := data.Hours[key][hour]
			cur.T += b.T
			cur.C += b.C
			data.Hours[key][hour] = cur
		}
	}
	for k, l := range d.limits {
		if cur, ok := data.Limits[k]; !ok || !cur.ObservedAt.After(l.ObservedAt) {
			data.Limits[k] = l
		}
	}
	for id, byKey := range d.ses {
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
	data.Version = ledgerVersion
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
	r.inflight = delta{}
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
	if data.Hours == nil {
		data.Hours = map[string]map[string]hourBucket{}
	}
	if data.Limits == nil {
		data.Limits = map[string]PlanLimit{}
	}
	if data.Imported == nil {
		data.Imported = map[string]string{}
	}
	return data, "", nil
}

// prune drops day totals older than dayRetention, hour buckets and limit
// readings older than hourRetention, and sessions idle past the session
// retention.
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
	hourCutoff := now.Add(-hourRetention).Format(hourLayout)
	for key, byHour := range d.Hours {
		for hour := range byHour {
			if hour < hourCutoff {
				delete(byHour, hour)
			}
		}
		if len(byHour) == 0 {
			delete(d.Hours, key)
		}
	}
	for k, l := range d.Limits {
		if now.Sub(l.ObservedAt) > limitRetention {
			delete(d.Limits, k)
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

// mergeDelta adds src into dst; a limit keeps the later observation.
func mergeDelta(dst *delta, src delta) {
	for k, inner := range src.days {
		if dst.days[k] == nil {
			dst.days[k] = map[string]int64{}
		}
		for k2, n := range inner {
			dst.days[k][k2] += n
		}
	}
	for k, inner := range src.ses {
		if dst.ses[k] == nil {
			dst.ses[k] = map[string]int64{}
		}
		for k2, n := range inner {
			dst.ses[k][k2] += n
		}
	}
	for k, inner := range src.hours {
		if dst.hours[k] == nil {
			dst.hours[k] = map[string]hourBucket{}
		}
		for k2, b := range inner {
			cur := dst.hours[k][k2]
			cur.T += b.T
			cur.C += b.C
			dst.hours[k][k2] = cur
		}
	}
	for k, l := range src.limits {
		if cur, ok := dst.limits[k]; !ok || !cur.ObservedAt.After(l.ObservedAt) {
			dst.limits[k] = l
		}
	}
}
