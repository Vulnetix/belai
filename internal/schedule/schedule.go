// Package schedule keeps the agents a host runs on a cron schedule. A
// schedule is a worker profile, a cron expression and a directory the host
// offers, and the `belai rc` daemon fires each one when it is due (see
// internal/rc, docs/remote-control.md "Scheduled agents").
//
// Schedules are mirrored to the website the way the kanban board is: the host
// keeps the durable copy in a local file, the website lists and edits it, and
// each side's change reaches the other through a version cursor. The
// definition (profile, cron, dir, enabled) resolves last-writer-wins. The run
// record (last run, last status, next run) belongs to the host alone: a pulled
// record never replaces it, so a run is never lost to a web edit.
//
// Everything pulled came from the website, so the store cleans every string
// before it is kept. It does not decide whether a schedule may run: the
// daemon checks the profile, the directory and the cron against its own
// catalogue and parser each time, and disables a schedule it cannot accept.
package schedule

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/sanitize"
)

// Record is one stored schedule.
type Record struct {
	// ID is the schedule's UUID, minted by whichever side created it.
	ID string `json:"id"`
	// Profile is a worker profile name from this host's catalogue.
	Profile string `json:"profile"`
	// Cron is a five-field expression or @hourly, @daily, @weekly, @monthly,
	// in the host's local time.
	Cron string `json:"cron"`
	// Dir is one of the directories the daemon offers.
	Dir     string `json:"dir"`
	Enabled bool   `json:"enabled"`
	// The run record, in unix milliseconds. Zero means none. It is written
	// only on this host.
	LastRunAt  int64  `json:"last_run_at,omitempty"`
	LastStatus string `json:"last_status,omitempty"`
	NextRunAt  int64  `json:"next_run_at,omitempty"`
	Created    int64  `json:"created"`
	// Updated is when the definition last changed, on either side.
	Updated int64 `json:"updated"`
	// ServerVersion is the backend's version of the last write seen.
	ServerVersion int64 `json:"server_version,omitempty"`
	Deleted       bool  `json:"deleted,omitempty"`
	// Dirty marks a change not yet pushed. Rev counts local changes, so a
	// push that was in flight while the record changed again leaves it dirty.
	Dirty bool `json:"dirty,omitempty"`
	Rev   int  `json:"rev,omitempty"`
}

// Run outcomes a host reports (the server accepts exactly these).
const (
	StatusStarted         = "started"
	StatusSkippedBusy     = "skipped_busy"
	StatusRefusedCap      = "refused_cap"
	StatusRefusedDir      = "refused_dir"
	StatusRefusedProfile  = "refused_profile"
	StatusRefusedCron     = "refused_cron"
	StatusRefusedDisabled = "refused_disabled"
	StatusError           = "error"
)

// ValidStatus reports whether s is a status a host may report.
func ValidStatus(s string) bool {
	switch s {
	case "", StatusStarted, StatusSkippedBusy, StatusRefusedCap, StatusRefusedDir,
		StatusRefusedProfile, StatusRefusedCron, StatusRefusedDisabled, StatusError:
		return true
	}
	return false
}

// Limits on a record's strings.
const (
	MaxProfile = 64
	MaxCron    = 100
	MaxDir     = 4096
)

var (
	idPattern   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
)

// ValidID reports whether s is a lowercase UUID.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// ValidProfileName reports whether s can name a worker profile: it starts with
// a letter or digit, so it can never be read as a flag.
func ValidProfileName(s string) bool { return namePattern.MatchString(s) }

// clean returns r with every string reduced to one clean line within its cap.
// A value that was too long or held markup is cut or stripped, not rejected,
// and the daemon's own checks then refuse a result that is no longer a valid
// profile, cron or directory.
func (r Record) clean() Record {
	r.ID = strings.ToLower(strings.TrimSpace(r.ID))
	r.Profile = sanitize.Line(r.Profile, MaxProfile)
	r.Cron = sanitize.Line(r.Cron, MaxCron)
	r.Dir = cleanDir(r.Dir)
	if !ValidStatus(r.LastStatus) {
		r.LastStatus = StatusError
	}
	return r
}

// cleanDir keeps a path only when it is lexically sound; a path that is not
// becomes empty, which the daemon refuses as an unoffered directory.
func cleanDir(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > MaxDir || !utf8.ValidString(s) || sanitize.PathText(s) != nil {
		return ""
	}
	return s
}

// sameDefinition reports whether two records define the same schedule.
func sameDefinition(a, b Record) bool {
	return a.Profile == b.Profile && a.Cron == b.Cron && a.Dir == b.Dir && a.Enabled == b.Enabled
}
