package plans

import (
	"regexp"
	"strconv"
)

// Progress tracks plan step completion via [DONE:n] markers emitted by the
// agent during execution.
type Progress struct {
	Total int
	done  map[int]bool
}

// NewProgress returns a Progress for a plan with total steps.
func NewProgress(total int) *Progress {
	return &Progress{Total: total, done: map[int]bool{}}
}

// MarkDone records step n as complete (1-indexed, clamped to [1, Total]).
func (p *Progress) MarkDone(n int) {
	if n >= 1 && n <= p.Total {
		p.done[n] = true
	}
}

var doneRe = regexp.MustCompile(`\[DONE:(\d+)\]`)

// Apply scans text for [DONE:n] markers and marks those steps complete.
func (p *Progress) Apply(text string) {
	for _, m := range doneRe.FindAllStringSubmatch(text, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil {
			p.MarkDone(n)
		}
	}
}

// Completed returns the number of completed steps.
func (p *Progress) Completed() int { return len(p.done) }

// Remaining returns the 1-indexed steps not yet completed, in order.
func (p *Progress) Remaining() []int {
	var out []int
	for i := 1; i <= p.Total; i++ {
		if !p.done[i] {
			out = append(out, i)
		}
	}
	return out
}

// Done reports whether every step is complete.
func (p *Progress) Done() bool { return len(p.done) == p.Total }
