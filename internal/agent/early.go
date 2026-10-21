package agent

// early is a result started before the turn knows it needs it. Pre-prompt
// decision jobs that read only the cleaned prompt (the agent pick, the scale
// rating) start beside admission and mode detection rather than after them,
// so their round trips overlap; a job whose result goes unused is simply
// never read, and its activity record is never emitted.
type early[T any] struct {
	done   chan struct{}
	val    T
	record func()
}

// startEarly runs f in the background. f returns its value and the activity
// record to emit if the value is used.
func startEarly[T any](f func() (T, func())) *early[T] {
	e := &early[T]{done: make(chan struct{})}
	go func() {
		defer close(e.done)
		e.val, e.record = f()
	}()
	return e
}

// use waits for the value and emits its activity record. A nil early runs
// fallback instead, so a call site reads the same either way.
func (e *early[T]) use(fallback func() T) T {
	if e == nil {
		return fallback()
	}
	<-e.done
	if e.record != nil {
		e.record()
		e.record = nil
	}
	return e.val
}
