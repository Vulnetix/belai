package tts

import (
	"context"
	"strings"
	"sync"
)

// Workers is how many pieces are synthesised at once.
const Workers = 4

// Result is one piece of a message, in order. Cached means the whole message
// came from the cache, as a single piece.
type Result struct {
	Index, Total int
	PCM          []byte
	Err          error
	Cached       bool
}

// Synthesize reads groups (from Prepare) aloud: it answers from the cache when
// the whole message was read before, and otherwise runs up to Workers requests
// at once and delivers the pieces in order, so playback can start after the
// first. A failed piece ends the stream with its error. A message read to the
// end is stored in the cache. The channel closes when the work is done or ctx
// is cancelled.
func Synthesize(ctx context.Context, eng Engine, cache *Cache, voice string, groups []string) <-chan Result {
	out := make(chan Result)
	go func() {
		defer close(out)
		if len(groups) == 0 {
			return
		}
		key := Key(eng.Name(), voice, strings.Join(groups, "\n"))
		if pcm, ok := cache.Get(key); ok {
			send(ctx, out, Result{Index: 0, Total: 1, PCM: pcm, Cached: true})
			return
		}
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		slots := make([]chan Result, len(groups))
		for i := range slots {
			slots[i] = make(chan Result, 1)
		}
		sem := make(chan struct{}, Workers)
		var wg sync.WaitGroup
		launched := make(chan struct{})
		go func() {
			defer close(launched)
			for i, g := range groups {
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() { <-sem }()
					pcm, err := eng.Synthesize(ctx, g, voice)
					slots[i] <- Result{Index: i, Total: len(groups), PCM: pcm, Err: err}
				}()
			}
		}()
		var all []byte
		for i := range slots {
			var r Result
			select {
			case r = <-slots[i]:
			case <-ctx.Done():
				return
			}
			if !send(ctx, out, r) || r.Err != nil {
				cancel()
				<-launched
				wg.Wait()
				return
			}
			all = append(all, r.PCM...)
		}
		<-launched
		wg.Wait()
		_ = cache.Put(key, all)
	}()
	return out
}

func send(ctx context.Context, out chan<- Result, r Result) bool {
	select {
	case out <- r:
		return true
	case <-ctx.Done():
		return false
	}
}
