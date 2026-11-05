package agent

import (
	"crypto/sha256"
	"strconv"
)

// stuckDetector watches the calls inside one pass for a loop the model cannot
// see from the inside: the same call returning the same result again and
// again, or two calls trading places with unchanged results. It is decided
// from observed calls only. No model and no classifier is asked, and the
// detector approves nothing. Only a digest of each call is kept, never its
// arguments or its output.
type stuckDetector struct {
	repeat int
	size   int
	window [][8]byte
	nudged bool
}

type stuckVerdict int

const (
	stuckNone stuckVerdict = iota
	stuckNudge
	stuckStop
)

// stuckAlternation is how many strictly alternating entries make a loop. It
// is a constant rather than a setting: the repeat count is the sensitivity.
const stuckAlternation = 6

func newStuckDetector(repeat, size int) *stuckDetector {
	return &stuckDetector{repeat: repeat, size: size}
}

// stuckFingerprint reduces a call and its result to eight digest bytes. The
// arguments come from workArgs, so a changed free-text description does not
// hide a repeat.
func stuckFingerprint(name, args, result string) [8]byte {
	h := sha256.New()
	for _, part := range []string{name, args, result} {
		h.Write([]byte(strconv.Itoa(len(part))))
		h.Write([]byte{0})
		h.Write([]byte(part))
	}
	var out [8]byte
	copy(out[:], h.Sum(nil))
	return out
}

// reset forgets the window. A file change is progress, so what came before it
// no longer counts toward a loop.
func (d *stuckDetector) reset() { d.window = d.window[:0] }

// observe records one call's fingerprint and reports whether the window now
// holds a loop. The first hit is a nudge and clears the window; a second hit
// after that is a stop.
func (d *stuckDetector) observe(fp [8]byte) stuckVerdict {
	d.window = append(d.window, fp)
	if len(d.window) > d.size {
		d.window = d.window[len(d.window)-d.size:]
	}
	if !d.looping(fp) {
		return stuckNone
	}
	d.window = d.window[:0]
	if d.nudged {
		return stuckStop
	}
	d.nudged = true
	return stuckNudge
}

func (d *stuckDetector) looping(fp [8]byte) bool {
	n := 0
	for _, w := range d.window {
		if w == fp {
			n++
		}
	}
	if n >= d.repeat {
		return true
	}
	w := d.window
	if len(w) < stuckAlternation {
		return false
	}
	tail := w[len(w)-stuckAlternation:]
	if tail[0] == tail[1] {
		return false
	}
	for i := 2; i < len(tail); i++ {
		if tail[i] != tail[i-2] {
			return false
		}
	}
	return true
}

const stuckLoopDirective = "The last several tool calls repeated the same calls with the same results. Repeating them again will not change the outcome. Change the approach: read a different file, run a different command, correct the input, or say what is blocking you."

// observeStuck feeds one executed call to the detector. Bookkeeping calls, a
// read the harness answered from its index, and a call whose arguments did
// not parse say nothing about progress, so they are skipped. A call that
// changed a file is progress and clears the window.
func (s *Session) observeStuck(d *stuckDetector, u callUnit, result string, changed bool) stuckVerdict {
	if d == nil {
		return stuckNone
	}
	if changed {
		d.reset()
		return stuckNone
	}
	if u.parseErr != nil || u.repeat != "" {
		return stuckNone
	}
	switch u.call.Name {
	case "Todo", "update_plan", "AskUserQuestion", "ExitPlanMode":
		return stuckNone
	}
	return d.observe(stuckFingerprint(u.call.Name, workArgs(u.call), result))
}
