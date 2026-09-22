package lsp

import (
	"context"
	"os/exec"
	"sort"
	"sync"
)

// Probe reports whether a binary is present and where it is.
type Probe interface {
	Lookup(ctx context.Context, binary string) (string, bool)
}

// realProbe uses exec.LookPath.
type realProbe struct{}

func (realProbe) Lookup(ctx context.Context, binary string) (string, bool) {
	p, err := exec.LookPath(binary)
	if err != nil {
		return "", false
	}
	return p, true
}

// Detect returns the resolved path for one language's server, preferring
// configured overrides and then Server followed by Alts.
func Detect(ctx context.Context, probe Probe, server string, alts []string) (string, bool) {
	if probe == nil {
		probe = realProbe{}
	}
	cands := make([]string, 0, 1+len(alts))
	if server != "" {
		cands = append(cands, server)
	}
	cands = append(cands, alts...)
	if len(cands) == 0 {
		return "", false
	}
	result := make(chan string, 1)
	var once sync.Once
	var wg sync.WaitGroup
	for _, c := range cands {
		c := c
		wg.Add(1)
		go func() {
			defer wg.Done()
			if path, ok := probe.Lookup(ctx, c); ok {
				once.Do(func() { result <- path })
			}
		}()
	}
	go func() {
		wg.Wait()
		once.Do(func() { close(result) })
	}()
	select {
	case path, ok := <-result:
		return path, ok
	case <-ctx.Done():
		return "", false
	}
}

// DetectAll returns every detected server paired with its absolute path.
func DetectAll(ctx context.Context, overrides map[string]string) map[string]string {
	probe := realProbe{}
	out := make(map[string]string)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, l := range languages {
		l := l
		if override := overrides[l.ID]; override != "" {
			if fi, err := exec.LookPath(override); err == nil {
				mu.Lock()
				out[l.ID] = fi
				mu.Unlock()
			}
			continue
		}
		// Limit concurrency to avoid a PATH storm on every UI render.
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p, ok := Detect(ctx, probe, l.Server, l.Alts); ok {
				mu.Lock()
				out[l.ID] = p
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return out
}

// DetectedLanguages returns a sorted list of detected language IDs.
func DetectedLanguages(detected map[string]string) []string {
	out := make([]string, 0, len(detected))
	for id := range detected {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
