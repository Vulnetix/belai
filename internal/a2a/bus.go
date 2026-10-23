package a2a

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/proc"
)

// The same-host bus. There is no socket IPC between Belai processes, so it
// follows the file patterns already in use (fleet's registry, crewlog,
// config.AcquireFileLock):
//
//   - cards/<session>.json is each live session's Agent Card and last
//     broadcast, written atomically. A card whose process is gone or whose
//     beat is stale is removed by the next reader, like fleet's markDead.
//   - bus.jsonl is an append-only log of envelopes, appended under a lockfile
//     and rotated by size to bus.jsonl.1. Each process tails it from its own
//     offset into its mailbox.
const (
	busFile   = "bus.jsonl"
	busLock   = "bus.lock"
	cardsDir  = "cards"
	cardStale = 2 * time.Minute
	// maxBusLine bounds one line; a longer one is refused or skipped.
	maxBusLine = 64 << 10
	seenCap    = 4096
)

// busRotateAt is the size past which bus.jsonl rotates; tests lower it.
var busRotateAt int64 = 4 << 20

// Dir is the A2A directory under the global directory.
func Dir(globalDir string) string { return filepath.Join(globalDir, "a2a") }

// DefaultDir is Dir under config.GlobalDir.
func DefaultDir() (string, error) {
	g, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return Dir(g), nil
}

// Bus is one process's handle on the same-host bus. Any number of Buses, in
// any number of processes, may share a directory.
type Bus struct {
	dir string
	now func() time.Time

	mu      sync.Mutex
	offset  int64
	info    os.FileInfo // the bus file the offset is into
	pending []Envelope  // lines read while appending, not yet polled
	seen    map[string]bool
	order   []string
}

// OpenBus creates the directory (0700) if needed and returns a Bus that will
// see envelopes appended from now on.
func OpenBus(dir string) (*Bus, error) {
	if err := os.MkdirAll(filepath.Join(dir, cardsDir), 0o700); err != nil {
		return nil, fmt.Errorf("a2a: create bus dir: %w", err)
	}
	b := &Bus{dir: dir, now: time.Now, seen: map[string]bool{}}
	if info, err := os.Stat(b.path()); err == nil {
		b.offset, b.info = info.Size(), info
	}
	return b, nil
}

// Dir is the bus directory.
func (b *Bus) Dir() string { return b.dir }

func (b *Bus) path() string { return filepath.Join(b.dir, busFile) }

// Append writes env (already cleaned) as one line, unless an envelope with
// its messageId is already on the bus as far as this process has read. It
// reports whether it wrote. Lines other processes wrote since the last poll
// are kept for the next Poll.
func (b *Bus) Append(env Envelope) (bool, error) {
	line, err := json.Marshal(env)
	if err != nil {
		return false, err
	}
	if len(line)+1 > maxBusLine {
		return false, fmt.Errorf("a2a: envelope is %d bytes, over the %d byte cap", len(line), maxBusLine)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	release, err := config.AcquireFileLock(filepath.Join(b.dir, busLock))
	if err != nil {
		return false, err
	}
	defer release()
	got, err := b.readLocked()
	if err != nil {
		return false, err
	}
	b.pending = append(b.pending, got...)
	if b.seen[env.MessageID] {
		return false, nil
	}
	if err := b.rotateLocked(); err != nil {
		return false, err
	}
	f, err := os.OpenFile(b.path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return false, err
	}
	_, werr := f.Write(append(line, '\n'))
	cerr := f.Close()
	if werr != nil {
		return false, werr
	}
	if cerr != nil {
		return false, cerr
	}
	b.markSeen(env.MessageID)
	// Our own line is behind the offset now: the next read starts after it.
	if info, err := os.Stat(b.path()); err == nil {
		b.info, b.offset = info, info.Size()
	}
	return true, nil
}

// Poll returns every envelope appended since the last Poll, oldest first. A
// line that does not parse or clean is skipped.
func (b *Bus) Poll() ([]Envelope, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	got, err := b.readLocked()
	out := append(b.pending, got...)
	b.pending = nil
	return out, err
}

// readLocked reads from the offset to the end of the bus file, following a
// rotation: the rest of the rotated file first, then the new one from the
// start. The caller holds b.mu.
func (b *Bus) readLocked() ([]Envelope, error) {
	var out []Envelope
	info, err := os.Stat(b.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if b.info != nil && !os.SameFile(b.info, info) {
		// Rotated: finish the old file where it now lives.
		old := b.path() + ".1"
		if oi, err := os.Stat(old); err == nil && os.SameFile(b.info, oi) {
			envs, _, _ := b.readFrom(old, b.offset)
			out = append(out, envs...)
		}
		b.offset = 0
	}
	if info.Size() < b.offset {
		b.offset = 0 // truncated
	}
	envs, next, err := b.readFrom(b.path(), b.offset)
	out = append(out, envs...)
	b.offset, b.info = next, info
	return out, err
}

// readFrom reads complete lines of path from offset and returns the envelopes
// and the offset after the last complete line.
func (b *Bus) readFrom(path string, offset int64) ([]Envelope, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	r := bufio.NewReaderSize(f, 64<<10)
	var out []Envelope
	for {
		line, err := r.ReadBytes('\n')
		if len(line) == 0 || line[len(line)-1] != '\n' {
			// A partial last line is left for the next read.
			return out, offset, nil
		}
		offset += int64(len(line))
		line = bytes.TrimSpace(line)
		if len(line) > 0 && len(line) <= maxBusLine {
			var env Envelope
			if json.Unmarshal(line, &env) == nil {
				if clean, cerr := env.Clean(); cerr == nil && !b.seen[clean.MessageID] {
					b.markSeen(clean.MessageID)
					out = append(out, clean)
				}
			}
		}
		if err != nil {
			return out, offset, nil
		}
	}
}

func (b *Bus) markSeen(id string) {
	if b.seen[id] {
		return
	}
	b.seen[id] = true
	b.order = append(b.order, id)
	if len(b.order) > seenCap {
		drop := b.order[:len(b.order)-seenCap]
		for _, d := range drop {
			delete(b.seen, d)
		}
		b.order = append([]string(nil), b.order[len(b.order)-seenCap:]...)
	}
}

// rotateLocked moves bus.jsonl to bus.jsonl.1 once it has grown past
// busRotateAt. The caller holds the lockfile.
func (b *Bus) rotateLocked() error {
	info, err := os.Stat(b.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() < busRotateAt {
		return nil
	}
	return os.Rename(b.path(), b.path()+".1")
}

// CardRecord is one live session's entry in cards/: its Agent Card, the
// process that holds it, the last beat (unix ms) and its last broadcast.
type CardRecord struct {
	Card AgentCard `json:"card"`
	PID  int       `json:"pid"`
	Beat int64     `json:"beat"`
	Last *Envelope `json:"last,omitempty"`
}

func (b *Bus) cardPath(sessionID string) (string, bool) {
	if !uuidShape.MatchString(sessionID) {
		return "", false
	}
	return filepath.Join(b.dir, cardsDir, sessionID+".json"), true
}

// PutCard writes a card atomically, stamping the beat.
func (b *Bus) PutCard(rec CardRecord) error {
	path, ok := b.cardPath(rec.Card.SessionID)
	if !ok {
		return invalid("card session")
	}
	if rec.PID == 0 {
		rec.PID = os.Getpid()
	}
	rec.Beat = b.now().UnixMilli()
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".card-*")
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
	return os.Rename(tmp, path)
}

// RemoveCard deletes a session's card.
func (b *Bus) RemoveCard(sessionID string) {
	if path, ok := b.cardPath(sessionID); ok {
		_ = os.Remove(path)
	}
}

// Cards returns every live card. A card whose process is gone, or whose beat
// is older than cardStale, is removed.
func (b *Bus) Cards() []CardRecord {
	entries, err := os.ReadDir(filepath.Join(b.dir, cardsDir))
	if err != nil {
		return nil
	}
	now := b.now().UnixMilli()
	var out []CardRecord
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(b.dir, cardsDir, name)
		data, err := os.ReadFile(path)
		if err != nil || len(data) > maxBusLine {
			continue
		}
		var rec CardRecord
		if json.Unmarshal(data, &rec) != nil {
			continue
		}
		card, err := rec.Card.clean()
		if err != nil || card.SessionID+".json" != name {
			continue
		}
		rec.Card = card
		if !proc.Alive(rec.PID) || now-rec.Beat > cardStale.Milliseconds() {
			_ = os.Remove(path)
			continue
		}
		if rec.Last != nil {
			if last, err := rec.Last.Clean(); err == nil && last.Agent.ID == card.ID {
				rec.Last = &last
			} else {
				rec.Last = nil
			}
		}
		out = append(out, rec)
	}
	return out
}
