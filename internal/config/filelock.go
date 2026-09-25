package config

import (
	"fmt"
	"os"
	"time"
)

// Advisory lockfile timing. A lock older than LockStaleAfter is assumed to be
// left behind by a crashed process and is taken over; a waiter gives up after
// lockWait.
const (
	LockStaleAfter = 30 * time.Second
	lockWait       = 10 * time.Second
	lockPoll       = 200 * time.Millisecond
)

// AcquireFileLock takes the advisory lockfile at path, creating it exclusively
// and writing the holder's pid. A lockfile older than LockStaleAfter is stolen.
// It is the cross-process serialisation point for global state files that
// several signet processes read-modify-write (projects.json, usage.json); an
// in-process mutex must serialise first. The returned func releases the lock.
func AcquireFileLock(path string) (func(), error) {
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d", os.Getpid())
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			// Only a held lock is worth waiting for; a missing directory or a
			// permission error will not clear by itself.
			return nil, err
		}
		if fi, stErr := os.Stat(path); stErr == nil && time.Since(fi.ModTime()) > LockStaleAfter {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("could not acquire lock %s", path)
		}
		time.Sleep(lockPoll)
	}
}
