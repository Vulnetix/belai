// Package proc owns subprocess concerns shared across tools: process-group
// signalling for kill trees and line-tee streaming for live output.
package proc

import (
	"bytes"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultLineFlushEvery bounds how often a running command wakes the consumer.
// Without it, output like `find /` produces an event per line and floods the
// event channel with work the terminal cannot draw anyway.
const DefaultLineFlushEvery = 50 * time.Millisecond

// DefaultLineFlushLines flushes early when a burst arrives faster than the
// interval, so a fast command still streams rather than arriving all at once.
const DefaultLineFlushLines = 64

// LineTee accumulates a command's combined output, caps it at max bytes, and
// reports whole lines to a sink as they arrive. It is the single streaming
// writer shared by tools.Bash and vulnetixcli.
//
// It is written to by the goroutine os/exec uses to copy from the process, and
// read by the caller after Wait returns; the mutex covers that handover. The
// sink is called with the lock released — it may block, and blocking it must
// not also block Content.
type LineTee struct {
	sink       func(string)
	max        int
	flushEvery time.Duration
	flushLines int

	mu        sync.Mutex
	buf       []byte // full output, capped at max
	truncated bool
	partial   []byte // bytes since the last newline
	pending   []string
	lastFlush time.Time
}

// NewLineTee returns a LineTee capped at max bytes. A zero max uses 64 KiB.
func NewLineTee(max int, sink func(string)) *LineTee {
	if max <= 0 {
		max = 64 * 1024
	}
	return &LineTee{
		sink:       sink,
		max:        max,
		flushEvery: DefaultLineFlushEvery,
		flushLines: DefaultLineFlushLines,
	}
}

// Write appends p to the capped buffer and reports whole lines to the sink.
func (w *LineTee) Write(p []byte) (int, error) {
	w.mu.Lock()

	if !w.truncated {
		if room := w.max - len(w.buf); room > 0 {
			if len(p) <= room {
				w.buf = append(w.buf, p...)
			} else {
				w.buf = append(w.buf, p[:room]...)
				w.truncated = true
			}
		} else {
			w.truncated = true
		}
	}

	if w.sink == nil {
		w.mu.Unlock()
		return len(p), nil
	}

	w.partial = append(w.partial, p...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		w.pending = append(w.pending, string(bytes.TrimRight(w.partial[:i], "\r")))
		w.partial = w.partial[i+1:]
	}

	ready := w.takeLocked(false)
	w.mu.Unlock()

	w.emit(ready)
	return len(p), nil
}

// Flush reports any buffered lines, including a trailing line with no newline,
// which is how a prompt or a progress line without a terminator still reaches
// the consumer.
func (w *LineTee) Flush() {
	w.mu.Lock()
	if len(w.partial) > 0 {
		w.pending = append(w.pending, string(bytes.TrimRight(w.partial, "\r")))
		w.partial = nil
	}
	ready := w.takeLocked(true)
	w.mu.Unlock()
	w.emit(ready)
}

// takeLocked returns the buffered lines when they are due to be sent. Callers
// hold w.mu.
func (w *LineTee) takeLocked(force bool) []string {
	if len(w.pending) == 0 {
		return nil
	}
	if !force && w.flushEvery > 0 &&
		len(w.pending) < w.flushLines &&
		time.Since(w.lastFlush) < w.flushEvery {
		return nil
	}
	out := w.pending
	w.pending = nil
	w.lastFlush = time.Now()
	return out
}

func (w *LineTee) emit(lines []string) {
	if len(lines) == 0 || w.sink == nil {
		return
	}
	w.sink(strings.Join(lines, "\n"))
}

// Content returns the captured output, with the truncation notice appended if
// the cap was reached.
func (w *LineTee) Content() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.truncated {
		return string(w.buf) + "\n… truncated at " + strconv.Itoa(w.max) + " bytes"
	}
	return string(w.buf)
}
