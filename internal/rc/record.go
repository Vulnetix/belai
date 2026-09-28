package rc

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/proc"
)

// Record is the running daemon's state file (~/.vulnetix/belai/rc/rc.json).
// The daemon rewrites it on every heartbeat; `belai rc --status`, `--stop`
// and the TUI's footer read it.
type Record struct {
	PID      int              `json:"pid"`
	Started  time.Time        `json:"started"`
	Beat     time.Time        `json:"beat"`
	HostID   string           `json:"host_id"`
	URL      string           `json:"url"`
	Max      int              `json:"max"`
	Dirs     []Dir            `json:"dirs"`
	Sessions []RunningSession `json:"sessions"`
	LogPath  string           `json:"log_path,omitempty"`
	// Online: the website accepted the last heartbeat.
	Online bool   `json:"online"`
	Error  string `json:"error,omitempty"`
}

// RunningSession is one session the daemon started.
type RunningSession struct {
	ID       string    `json:"id"`
	Dispatch string    `json:"dispatch"`
	Cwd      string    `json:"cwd"`
	PID      int       `json:"pid"`
	Started  time.Time `json:"started"`
}

// staleAfter: a record whose beat is older than this belongs to a daemon
// that died without cleaning up.
const staleAfter = 45 * time.Second

// Dir returns the rc state directory.
func StateDir() (string, error) {
	g, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(g, "rc"), nil
}

func recordPath() (string, error) {
	d, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "rc.json"), nil
}

// ReadRecord returns the daemon record and whether that daemon is running.
func ReadRecord() (Record, bool) {
	p, err := recordPath()
	if err != nil {
		return Record{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return Record{}, false
	}
	var r Record
	if json.Unmarshal(b, &r) != nil {
		return Record{}, false
	}
	return r, r.PID > 0 && proc.Alive(r.PID) && time.Since(r.Beat) < staleAfter
}

func writeRecord(r Record) error {
	p, err := recordPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func removeRecord(pid int) {
	if r, _ := ReadRecord(); r.PID == pid {
		if p, err := recordPath(); err == nil {
			_ = os.Remove(p)
		}
	}
}

// Stop asks the running daemon to stop and waits up to d for it to exit.
func Stop(d time.Duration) (Record, error) {
	r, live := ReadRecord()
	if !live {
		return r, errors.New("belai rc is not running")
	}
	if err := proc.Terminate(r.PID); err != nil {
		return r, err
	}
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !proc.Alive(r.PID) {
			return r, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return r, errors.New("belai rc did not stop in time")
}
